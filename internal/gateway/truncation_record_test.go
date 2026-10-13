package gateway

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"gpt-load/internal/execution"
	"gpt-load/internal/health"
	"gpt-load/internal/telemetry"
)

// 从真实转发与 Handler 重试入口验证请求终态，防止只有 attempt 失败而请求误记成功。
func TestTruncationRetryRequestOutcome(t *testing.T) {
	const preamble = "data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"
	for _, tc := range []struct {
		name       string
		failure    execution.ErrorEvidence
		recover    bool
		wantCalls  int64
		wantStatus telemetry.RequestStatus
		wantCode   string
		wantBody   string
	}{
		{name: "second candidate completes", failure: execution.ErrorEvidence{Kind: execution.ErrorKindTransport, Summary: "upstream closed"}, recover: true,
			wantCalls: 2, wantStatus: telemetry.RequestStatusSuccess, wantBody: contentStreamBody},
		{name: "both candidates truncate", failure: execution.ErrorEvidence{Kind: execution.ErrorKindTransport, Summary: "upstream closed"},
			wantCalls: 2, wantStatus: telemetry.RequestStatusIncomplete, wantCode: health.StreamTruncatedBeforeContentCode, wantBody: preamble},
		{name: "adapter cancellation is not retried", failure: execution.ErrorEvidence{Kind: execution.ErrorKindCanceled, Summary: "stream consumer stopped"},
			wantCalls: 1, wantStatus: telemetry.RequestStatusCanceled, wantCode: "client_canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			executor := fakeExecutionExecutor{stream: func(_ context.Context, _ execution.AttemptSpec, sink execution.StreamSink) execution.StreamResult {
				call := calls.Add(1)
				if err := sink(execution.StreamEvent{Sequence: 1, Kind: execution.StreamEventReady, StatusCode: http.StatusOK,
					Header: http.Header{"Content-Type": {"text/event-stream"}}}); err != nil {
					t.Fatal(err)
				}
				body := preamble
				var failure *execution.ErrorEvidence
				if tc.recover && call == 2 {
					body = contentStreamBody
				} else {
					copy := tc.failure.Clone()
					failure = &copy
				}
				if err := sink(execution.StreamEvent{Sequence: 2, Kind: execution.StreamEventData, Data: []byte(body)}); err != nil {
					t.Fatal(err)
				}
				return execution.StreamResult{DispatchState: execution.DispatchMaybeSent, ResponseStarted: true, StatusCode: http.StatusOK,
					Header: http.Header{"Content-Type": {"text/event-stream"}}, Error: failure}
			}}
			sink := &recordingRequestLogSink{}
			engine, _, manager, _ := newRequestLogHandlerTestRuntime(t, NewExecutionForwarder(executor), &recordingAccessKeyRPMLimiter{}, sink, "sk-a", "sk-b")
			group := manager.Current().Groups[1]
			group.EmptyResponseRetry = true
			manager.Current().Groups[1] = group
			response := performStreamingRequest(engine)
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("upstream attempts = %d, want %d", got, tc.wantCalls)
			}
			if got := response.Body.String(); got != tc.wantBody {
				t.Fatalf("downstream body = %q, want %q (failed candidate bytes must not leak into the recovered answer)", got, tc.wantBody)
			}
			events := sink.snapshot()
			if len(events) != 1 {
				t.Fatalf("request log events = %d, want 1", len(events))
			}
			event := events[0]
			if event.Status != tc.wantStatus || event.ErrorCode != tc.wantCode {
				t.Fatalf("request outcome = %s/%s, want %s/%s", event.Status, event.ErrorCode, tc.wantStatus, tc.wantCode)
			}
			if len(event.Attempts) != int(tc.wantCalls) {
				t.Fatalf("recorded attempts = %d, want %d", len(event.Attempts), tc.wantCalls)
			}
			if tc.wantCalls == 2 {
				first, last := event.Attempts[0], event.Attempts[1]
				if first.ErrorCode != health.StreamTruncatedBeforeContentCode || first.Committed || !first.WillRetry ||
					last.WillRetry || first.CredentialID == last.CredentialID {
					t.Fatalf("attempt transitions = %+v, want a precommit truncation retried on a different credential", event.Attempts)
				}
				if event.StatusCode != http.StatusOK {
					t.Fatalf("logged HTTP status = %d, want 200 without confusing HTTP success with stream completion", event.StatusCode)
				}
			}
		})
	}
}
