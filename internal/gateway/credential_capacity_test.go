package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/channel"
	"gpt-load/internal/dialect"
	"gpt-load/internal/execution"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/state"
	providerobservation "gpt-load/internal/subscription/providers/observation"
)

func credentialCapacityRequest(engine http.Handler, key, model string, stream bool, ctx context.Context) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, model, stream))).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+key)
	out := httptest.NewRecorder()
	engine.ServeHTTP(out, req)
	return out
}

func TestCredentialCapacityHTTPSharedAcrossKeysModelsAndStreamLifetime(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			started, finish := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(finish) })
			defer unblock()
			var calls atomic.Int32
			f := concurrencyForwarder{call: func(ctx context.Context, _ ForwardInput, writer http.ResponseWriter) UpstreamResult {
				if calls.Add(1) == 1 {
					if writer != nil {
						writer.Header().Set("Content-Type", "text/event-stream")
						_, _ = writer.Write([]byte("data: {}\n\n"))
						writer.(http.Flusher).Flush()
					}
					close(started)
					select {
					case <-finish:
					case <-ctx.Done():
					}
				}
				return UpstreamResult{StatusCode: 200, Header: make(http.Header), Body: []byte(`{"choices":[]}`), DispatchState: execution.DispatchMaybeSent}
			}}
			h, m, _ := newHandlerForTest(t, f, "synthetic")
			input := handlerTestCompileInput(h.encryption, 1)
			input.SystemSettings = config.Settings{state.SettingCredentialConcurrencyLimit: 1, state.SettingCredentialRPMLimit: 2}
			input.Groups[0].Models = append(input.Groups[0].Models, state.ModelConfig{ID: "gpt-4o-mini"})
			secondKey := input.AccessKeys[0]
			secondKey.ID, secondKey.Name, secondKey.KeyHash = 2, "second", h.encryption.Hash("gl-second")
			input.AccessKeys = append(input.AccessKeys, secondKey)
			if _, err := m.Publish(input); err != nil {
				t.Fatal(err)
			}
			engine := gin.New()
			bindGatewayRoutesForTest(t, engine, h)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- credentialCapacityRequest(engine, "gl-client", "gpt-4o", stream, ctx) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("first dispatch did not start")
			}
			blocked := credentialCapacityRequest(engine, "gl-second", "gpt-4o-mini", false, context.Background())
			if blocked.Code != 429 || !strings.Contains(blocked.Body.String(), "credential_concurrency_limit_exceeded") || calls.Load() != 1 {
				t.Fatalf("cross-key admission: %d %s calls=%d", blocked.Code, blocked.Body, calls.Load())
			}
			cancel()
			unblock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation retained dispatch")
			}
			allowed := credentialCapacityRequest(engine, "gl-second", "gpt-4o-mini", false, context.Background())
			if allowed.Code != 200 {
				t.Fatalf("cancel/rejection retained capacity or RPM: %d %s", allowed.Code, allowed.Body)
			}
			limited := credentialCapacityRequest(engine, "gl-client", "gpt-4o", false, context.Background())
			if limited.Code != 429 || limited.Header().Get("Retry-After") == "" || !strings.Contains(limited.Body.String(), "credential_rate_limit_exceeded") || calls.Load() != 2 {
				t.Fatalf("RPM cap: %d %s calls=%d", limited.Code, limited.Body, calls.Load())
			}
		})
	}
}

func TestCredentialCapacityHTTPReleaseOnErrorPanicAndRetry(t *testing.T) {
	for _, mode := range []string{"error", "panic", "retry", "not-sent"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			f := concurrencyForwarder{call: func(_ context.Context, _ ForwardInput, _ http.ResponseWriter) UpstreamResult {
				calls++
				if calls == 1 {
					switch mode {
					case "panic":
						panic("synthetic failure")
					case "error":
						return UpstreamResult{Err: errors.New("synthetic transport error"), DispatchState: execution.DispatchMaybeSent}
					case "retry":
						return UpstreamResult{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"60"}}, DispatchState: execution.DispatchMaybeSent, RequestWritten: true, ExecutionError: &execution.ErrorEvidence{Kind: execution.ErrorKindHTTP, StatusCode: http.StatusTooManyRequests, Hint: execution.FailureHintRateLimited, OriginHint: execution.ErrorOriginUpstream, ScopeHint: execution.ErrorScopeCredential, ReplaySafety: execution.ReplaySafetyRejectedBeforeProcessing}}
					case "not-sent":
						return UpstreamResult{Err: errors.New("local preparation"), DispatchState: execution.DispatchNotSent}
					}
				}
				result := successfulAffinityResult()
				result.DispatchState = execution.DispatchMaybeSent
				return result
			}}
			keys := []string{"synthetic"}
			if mode == "retry" {
				keys = append(keys, "backup")
			}
			h, m, _ := newHandlerForTest(t, f, keys...)
			settings := config.Settings{state.SettingCredentialConcurrencyLimit: 1, state.SettingRetryCount: 0}
			if mode == "retry" {
				settings[state.SettingRetryCount] = 1
			}
			if mode == "not-sent" {
				settings[state.SettingCredentialRPMLimit] = 1
			}
			publishHandlerPolicySettings(t, h, m, len(keys), settings, nil)
			engine := gin.New()
			bindGatewayRoutesForTest(t, engine, h)
			func() {
				defer func() {
					recovered := recover()
					if mode == "panic" && recovered == nil {
						t.Error("panic fixture did not run")
					}
					if mode != "panic" && recovered != nil {
						panic(recovered)
					}
				}()
				concurrencyRequest(engine, false)
			}()
			response := concurrencyRequest(engine, false)
			if response.Code != 200 {
				t.Fatalf("%s leaked admission: %d %s", mode, response.Code, response.Body)
			}
			if mode == "retry" && calls != 3 {
				t.Fatalf("retry dispatch count = %d, want 3", calls)
			}
		})
	}
}

func TestCredentialCapacityContinuationDoesNotMigrate(t *testing.T) {
	f := &scriptedForwarder{results: []UpstreamResult{storedResponse("first"), storedResponse("second")}}
	h, engine, sink := newContinuationFixture(t, f)
	h.manager.Current().Settings.CredentialRPMLimit = 1
	serveContinuation(t, engine, "gl-client", `{"model":"gpt-4o","input":"initial"}`, 200)
	response := serveContinuation(t, engine, "gl-client", `{"model":"gpt-4o","input":"next","previous_response_id":"first"}`, 429)
	if !strings.Contains(response.Body.String(), "credential_rate_limit_exceeded") || len(f.inputs) != 1 {
		t.Fatalf("continuation switched credential: %s calls=%d", response.Body, len(f.inputs))
	}
	events := sink.snapshot()
	if len(events) != 2 || len(events[1].Attempts) != 0 {
		t.Fatalf("local admission recorded upstream attempts: %+v", events)
	}
	serveContinuation(t, engine, "gl-client", `{"model":"gpt-4o","input":"independent"}`, 200)
	if f.inputs[1].Credential.ID == f.inputs[0].Credential.ID {
		t.Fatal("unrelated credential did not retain independent capacity")
	}
}

type capacityMutationRegistry struct {
	*state.CredentialRegistry
	calls  int
	mutate func()
}

func (r *capacityMutationRegistry) ActiveEncryptedCredentialDataIfMatch(ref state.CredentialRef) (string, bool) {
	r.calls++
	if r.calls == 2 {
		r.mutate()
	}
	return r.CredentialRegistry.ActiveEncryptedCredentialDataIfMatch(ref)
}

func TestCredentialCapacityImmediateSuspensionRecheck(t *testing.T) {
	f := &scriptedForwarder{}
	h, m, registry := newHandlerForTest(t, f, "synthetic", "backup")
	publishHandlerPolicySettings(t, h, m, 2, config.Settings{state.SettingCredentialConcurrencyLimit: 1}, nil)
	h.registry = &capacityMutationRegistry{CredentialRegistry: registry, mutate: func() {
		if err := registry.SetCredentialStatus(1, state.CredentialStatusSuspended); err != nil {
			t.Fatal(err)
		}
	}}
	engine := gin.New()
	bindGatewayRoutesForTest(t, engine, h)
	response := concurrencyRequest(engine, false)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "configuration_changed") || len(f.inputs) != 0 {
		t.Fatalf("suspended selection dispatched or migrated: %d %s calls=%d", response.Code, response.Body, len(f.inputs))
	}
}

func TestCredentialCapacityClaudeQuotaSelectionAndImmediateRecheck(t *testing.T) {
	for _, mode := range []string{"missing", "stale", "threshold", "invalidated-after-selection"} {
		t.Run(mode, func(t *testing.T) {
			f := &scriptedForwarder{}
			h, m, registry := newHandlerForTest(t, f, "synthetic")
			h.dialects = dialect.NewSet(dialect.NewAnthropic())
			input := handlerTestCompileInput(h.encryption, 1)
			input.Groups[0].ChannelID, input.Groups[0].ConnectionType = channel.Claude, "subscription"
			input.Groups[0].Models = []state.ModelConfig{{ID: "claude-sonnet-4-5", Alias: "gpt-4o"}}
			input.SystemSettings = config.Settings{state.SettingQuotaBudget: map[string]any{"enabled": true, "five_hour_percent": 50, "seven_day_percent": 80, "observation_max_age_seconds": 300}}
			if _, err := m.Publish(input); err != nil {
				t.Fatal(err)
			}
			canonical := `{"type":"claude","access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_uuid":"synthetic-account","organization_uuid":"synthetic-org","expired":"2030-01-01T00:00:00Z"}`
			encrypted, err := h.encryption.Encrypt(canonical)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.ReplaceCredentials([]state.CredentialEntry{{ID: 1, GroupID: 1, Version: 1, IdentityGeneration: 1, Fingerprint: "credential-1", Status: state.CredentialStatusActive, EncryptedValue: encrypted}}); err != nil {
				t.Fatal(err)
			}
			ref, _ := registry.CredentialRef(1)
			now := time.Now().Add(-time.Second)
			h.now = func() time.Time { return now }
			observed := now.UnixMilli()
			reset := now.Add(time.Hour).UnixMilli()
			used := 0.1
			if mode == "stale" {
				observed = now.Add(-301 * time.Second).UnixMilli()
			}
			if mode == "threshold" {
				used = 0.5
			}
			if mode != "missing" {
				registry.ApplyQuotaBudgetWindows(ref, []providerobservation.QuotaWindow{{ID: "five_hour", ObservedAtMS: &observed, ResetAtMS: &reset, Utilization: &used}, {ID: "seven_day", ObservedAtMS: &observed, ResetAtMS: &reset, Utilization: &used}})
			}
			if mode == "invalidated-after-selection" {
				h.registry = &capacityMutationRegistry{CredentialRegistry: registry, mutate: func() { registry.InvalidateQuotaBudget(1, now) }}
			}
			engine := gin.New()
			bindGatewayRoutesForTest(t, engine, h)
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-4o","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`))
			request.Header.Set("x-api-key", "gl-client")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			code := "quota_observation_unavailable"
			if mode == "threshold" {
				code = "quota_budget_exceeded"
			}
			if response.Code != 429 || !strings.Contains(response.Body.String(), code) || len(f.inputs) != 0 {
				t.Fatalf("quota %s: %d %s calls=%d", mode, response.Code, response.Body, len(f.inputs))
			}
		})
	}
}

func TestCredentialCapacityAuditRejectsWithoutBusinessFallback(t *testing.T) {
	f := &scriptedForwarder{}
	h, engine := auditEngine(t, f)
	h.manager.Current().Settings.CredentialConcurrencyLimit = 1
	ref, exists := h.registry.CredentialRef(3)
	if !exists {
		t.Fatal("missing audit credential")
	}
	lease, failure, _ := h.acquireCredentialCapacity(ref)
	if failure != nil {
		t.Fatal(failure)
	}
	defer lease.Release()
	response := sendAuditRequest(engine, `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	if response.Code != 429 || !strings.Contains(response.Body.String(), "credential_concurrency_limit_exceeded") || len(f.inputs) != 0 {
		t.Fatalf("audit capacity bypassed: %d %s calls=%d", response.Code, response.Body, len(f.inputs))
	}
}
