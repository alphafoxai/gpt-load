package cpa

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/subscription/providers/mirasim"
)

func TestAdapterValidatesAllDeclaredMirasimRoutes(t *testing.T) {
	registry := channel.NewRegistry()
	adapter := NewAdapter(nil, registry)
	descriptor, ok := registry.Get(channel.Mirasim)
	if !ok {
		t.Fatal("Mirasim channel is missing")
	}
	for _, route := range descriptor.Routes {
		if err := adapter.ValidateRouteCapability(channel.ProviderMirasim, route); err != nil {
			t.Fatalf("ValidateRouteCapability(%#v) error = %v", route, err)
		}
		for _, mode := range route.PossibleModes {
			candidate := route
			candidate.RouteMode = mode
			if err := adapter.ValidateRouteCapability(channel.ProviderMirasim, candidate); err != nil {
				t.Fatalf("ValidateRouteCapability(%#v) error = %v", candidate, err)
			}
		}
	}
	if err := adapter.ValidateRouteCapability(channel.ProviderMirasim, channel.RouteDescriptor{
		ClientProtocol: protocol.OpenAIResponses,
		Operation:      execution.OperationResponsesCompact,
		RouteMode:      execution.RouteNative,
	}); err == nil {
		t.Fatal("compact route was accepted")
	}
}

func TestMirasimUpstreamProtocolUsesObservedPath(t *testing.T) {
	if got := mirasimUpstreamProtocol("/v1/messages"); got != protocol.Anthropic {
		t.Fatalf("messages protocol = %q", got)
	}
	if got := mirasimUpstreamProtocol("/v1/responses"); got != protocol.OpenAIResponses {
		t.Fatalf("responses protocol = %q", got)
	}
}

// Preserve the relay reason without guessing model scope from arbitrary 503
// messages. Platform busy and model availability responses remain host errors
// until the provider exposes a stable machine-readable scope contract.
func TestMirasimClassifyErrorKeepsTheRelayReason(t *testing.T) {
	bridge := newMirasimProviderBridge()
	credential := mirasimProviderCredential{value: mirasim.Storage{Type: "mirasim", AccessToken: "access-secret"}}
	tests := []struct {
		name        string
		status      int
		body        string
		wantHint    execution.FailureHint
		wantSummary string
		absent      string
	}{
		{
			name: "platform says the model is unavailable", status: http.StatusServiceUnavailable,
			body:     `{"error":{"message":"平台暂时无法提供 claude-opus-5-5，请稍后重试或换用其他模型。 claude-opus-5-5 is temporarily unavailable on the platform; retry shortly or switch models.","type":"api_error"},"type":"error"}`,
			wantHint: execution.FailureHintHostError,
			// The operator needs the model name and the reason, not just the status.
			wantSummary: "claude-opus-5-5 is temporarily unavailable on the platform",
		},
		{
			name: "host failure without a reason stays a host error", status: http.StatusServiceUnavailable,
			body:        `<html>bad gateway</html>`,
			wantHint:    execution.FailureHintHostError,
			wantSummary: "Mirasim upstream service failed",
		},
		{
			// The relay's wording is not stable: "temporarily unavailable"
			// earlier, "platform is busy" minutes later. What matters is that
			// it answered with a reason of its own.
			name: "platform is busy", status: http.StatusServiceUnavailable,
			body:        `{"error":{"message":"平台繁忙，请约 23 秒后重试。 The platform is busy; retry in about 23s.","type":"api_error"},"type":"error"}`,
			wantHint:    execution.FailureHintHostError,
			wantSummary: "The platform is busy; retry in about 23s.",
		},
		{
			name: "a reason-less relay error still reports the host", status: http.StatusBadGateway,
			body:        `{"error":{"type":"api_error"}}`,
			wantHint:    execution.FailureHintHostError,
			wantSummary: "Mirasim upstream service failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := mirasim.NewStatusError(test.status, []byte(test.body), http.Header{})
			status, evidence := bridge.ClassifyError(context.Background(), err, credential)
			if status != test.status || evidence == nil {
				t.Fatalf("status = %d evidence = %#v", status, evidence)
			}
			if evidence.Hint != test.wantHint {
				t.Fatalf("hint = %q, want %q", evidence.Hint, test.wantHint)
			}
			if !strings.Contains(evidence.Summary, test.wantSummary) {
				t.Fatalf("summary = %q, want it to contain %q", evidence.Summary, test.wantSummary)
			}
			if test.absent != "" && strings.Contains(evidence.Summary, test.absent) {
				t.Fatalf("summary = %q carries %q", evidence.Summary, test.absent)
			}
			if test.wantHint == execution.FailureHintModelUnavailable &&
				evidence.ScopeHint != execution.ErrorScopeModel {
				t.Fatalf("scope = %q, want the model rather than the credential", evidence.ScopeHint)
			}
			if test.wantHint == execution.FailureHintHostError &&
				evidence.ScopeHint == execution.ErrorScopeModel {
				t.Fatalf("a host fault was blamed on the model: %#v", evidence)
			}
		})
	}
}

func TestMirasimRelayMessageRedactsBeforeTruncation(t *testing.T) {
	token := "credential-crosses-the-old-message-truncation-boundary"
	key := "-----BEGIN PRIVATE KEY-----\nsynthetic-private-value\n-----END PRIVATE KEY-----"
	credential := mirasimProviderCredential{value: mirasim.Storage{AccessToken: token, RefreshToken: "refresh-secret", DevicePrivateKey: key}}
	text := strings.Repeat("中", 1355) + token + " refresh-secret " + key
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"message": text}})
	_, evidence := newMirasimProviderBridge().ClassifyError(context.Background(), mirasim.NewStatusError(503, body, nil), credential)
	if strings.Contains(evidence.Summary, token) || strings.Contains(evidence.Summary, "credential-crosses-") || strings.Contains(evidence.Summary, "refresh-secret") || strings.Contains(evidence.Summary, "synthetic-private-value") {
		t.Fatal("credential fragment survived redaction")
	}
	if !utf8.ValidString(evidence.Summary) || utf8.RuneCountInString(evidence.Summary) > execution.MaxErrorSummaryLength {
		t.Fatal("invalid bounded summary")
	}
}
