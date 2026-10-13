package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gpt-load/internal/dialect"
	"gpt-load/internal/execution"
	"gpt-load/internal/health"
	"gpt-load/internal/protocol"
)

// The production cut arrives as an ADAPTER-reported terminal error (transport),
// not as a gateway-side validateEOF failure. Both shapes must be treated as a
// replayable truncation when no visible content has been produced yet.
func TestTruncationFromAdapterErrorIsReplayable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		evidence *execution.ErrorEvidence
		want     bool
	}{
		{
			name:     "adapter transport error",
			evidence: &execution.ErrorEvidence{Kind: execution.ErrorKindTransport, OriginHint: execution.ErrorOriginUpstream, ScopeHint: execution.ErrorScopeRequest, Summary: "read upstream stream: unexpected eof"},
			want:     true,
		},
		{
			name:     "adapter provider stream terminated",
			evidence: &execution.ErrorEvidence{Kind: execution.ErrorKindProvider, Code: "upstream_stream_terminated", OriginHint: execution.ErrorOriginUpstream, Summary: "stream terminated"},
			want:     true,
		},
		{
			name:     "real business rejection must not be replayed",
			evidence: &execution.ErrorEvidence{Kind: execution.ErrorKindHTTP, StatusCode: http.StatusForbidden, Hint: execution.FailureHintCandidateUnavailable, Summary: "denied"},
			want:     false,
		},
		{
			name:     "rate limit must not be replayed as truncation",
			evidence: &execution.ErrorEvidence{Kind: execution.ErrorKindProvider, Code: "rate_limit_exceeded", Hint: execution.FailureHintRateLimited, Summary: "rate limited"},
			want:     false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := fakeExecutionExecutor{stream: func(
				_ context.Context, _ execution.AttemptSpec, sink execution.StreamSink,
			) execution.StreamResult {
				for _, e := range []execution.StreamEvent{
					{Sequence: 1, Kind: execution.StreamEventReady, StatusCode: http.StatusOK,
						Header: http.Header{"Content-Type": {"text/event-stream"}}},
					{Sequence: 2, Kind: execution.StreamEventData, Data: []byte(
						"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n",
					)},
					{Sequence: 3, Kind: execution.StreamEventData, Data: []byte(
						"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n",
					)},
					{Sequence: 4, Kind: execution.StreamEventData, Data: []byte(
						"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"x\"}}\n\n",
					)},
				} {
					if err := sink(e); err != nil {
						t.Errorf("sink: %v", err)
					}
				}
				return execution.StreamResult{
					DispatchState: execution.DispatchMaybeSent, ResponseStarted: true,
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
					Error: tc.evidence,
				}
			}}
			input := executionForwardInput()
			input.Dialect = dialect.NewAnthropic()
			input.ClientProtocol = protocol.Anthropic
			input.Request = &dialect.ParsedRequest{Method: http.MethodPost, Path: "/v1/messages"}
			input.EmptyResponseRetry = true
			recorder := httptest.NewRecorder()

			result := NewExecutionForwarder(executor).ForwardStream(context.Background(), input, recorder)
			t.Logf("committed=%v truncated=%v bodyLen=%d", result.Committed, result.TruncatedBeforeCommit, recorder.Body.Len())
			if result.TruncatedBeforeCommit != tc.want {
				t.Errorf("TruncatedBeforeCommit = %v, want %v", result.TruncatedBeforeCommit, tc.want)
			}
			if tc.want {
				if result.Committed {
					t.Errorf("committed a replayable truncation")
				}
				if recorder.Body.Len() != 0 {
					t.Errorf("wrote %d bytes downstream, want 0", recorder.Body.Len())
				}
				if result.ExecutionError == nil || result.ExecutionError.Code != health.StreamTruncatedBeforeContentCode {
					t.Errorf("evidence = %+v, want code %q", result.ExecutionError, health.StreamTruncatedBeforeContentCode)
				}
			}
		})
	}
}
