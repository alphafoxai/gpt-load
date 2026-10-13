package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"gpt-load/internal/dialect"
	"gpt-load/internal/execution"
	"gpt-load/internal/health"
	"gpt-load/internal/protocol"
)

// The production cut arrives as an adapter-reported terminal error, not always as
// a gateway-side validateEOF failure. Safety comes from the observable state, not
// the error type: nothing committed, no visible content, 2xx, no error event.
func TestTruncationBeforeVisibleContent(t *testing.T) {
	const (
		msgStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n"
		blockTht = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n"
		thinkDel = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"x\"}}\n\n"
		textDel  = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"
		errEvent = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n"
	)
	transportErr := &execution.ErrorEvidence{
		Kind: execution.ErrorKindTransport, OriginHint: execution.ErrorOriginUpstream,
		ScopeHint: execution.ErrorScopeRequest, Summary: "read upstream stream: unexpected eof",
	}
	denied := &execution.ErrorEvidence{
		Kind: execution.ErrorKindHTTP, StatusCode: http.StatusForbidden,
		Hint: execution.FailureHintCandidateUnavailable, Summary: "denied",
	}
	overloaded := &execution.ErrorEvidence{
		Kind: execution.ErrorKindProvider, Code: "overloaded_error",
		Hint: execution.FailureHintHostError, Summary: "overloaded",
	}

	for _, tc := range []struct {
		name       string
		events     []string
		terminal   *execution.ErrorEvidence
		status     int
		wantReplay bool
	}{
		{
			name:       "adapter transport cut after thinking",
			events:     []string{msgStart, blockTht, thinkDel},
			terminal:   transportErr,
			status:     http.StatusOK,
			wantReplay: true,
		},
		{
			name:       "cut with only preamble, no content at all",
			events:     []string{msgStart},
			terminal:   transportErr,
			status:     http.StatusOK,
			wantReplay: true,
		},
		{
			name:       "redacted thinking block is buffered before a cut",
			events:     []string{msgStart, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"opaque\"}}\n\n"},
			terminal:   transportErr,
			status:     http.StatusOK,
			wantReplay: true,
		},
		{
			name:       "thinking block with initial content is buffered before a cut",
			events:     []string{msgStart, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"working\"}}\n\n"},
			terminal:   transportErr,
			status:     http.StatusOK,
			wantReplay: true,
		},
		{
			name:   "explained refusal without message stop is not replayable",
			events: []string{msgStart, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"}}\n\n"},
			status: http.StatusOK,
		},
		{
			name:     "adapter cancellation must retain its evidence",
			events:   []string{msgStart, blockTht, thinkDel},
			terminal: &execution.ErrorEvidence{Kind: execution.ErrorKindCanceled, Summary: "stream consumer stopped"},
			status:   http.StatusOK,
		},
		{
			name:     "internal failure must retain its evidence",
			events:   []string{msgStart, blockTht, thinkDel},
			terminal: &execution.ErrorEvidence{Kind: execution.ErrorKindInternal, OriginHint: execution.ErrorOriginInternal, Code: "attempt_result_contract_invalid", Summary: "invalid stream result"},
			status:   http.StatusOK,
		},
		{
			name:     "business rejection after ready must retain its evidence",
			events:   []string{msgStart, blockTht, thinkDel},
			terminal: &execution.ErrorEvidence{Kind: execution.ErrorKindProvider, Hint: execution.FailureHintRequestRejected, Summary: "request rejected"},
			status:   http.StatusOK,
		},
		{
			name:     "completed thinking is not a truncation",
			events:   []string{msgStart, blockTht, thinkDel, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
			terminal: transportErr,
			status:   http.StatusOK,
		},
		{
			name:       "cut after visible text must NOT replay",
			events:     []string{msgStart, blockTht, thinkDel, textDel},
			terminal:   transportErr,
			status:     http.StatusOK,
			wantReplay: false,
		},
		{
			name:       "explicit error event must NOT replay",
			events:     []string{msgStart, blockTht, thinkDel, errEvent},
			terminal:   overloaded,
			status:     http.StatusOK,
			wantReplay: false,
		},
		{
			name:       "business denial must NOT replay",
			events:     nil,
			terminal:   denied,
			status:     http.StatusForbidden,
			wantReplay: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := fakeExecutionExecutor{stream: func(
				_ context.Context, _ execution.AttemptSpec, sink execution.StreamSink,
			) execution.StreamResult {
				if tc.status == http.StatusOK {
					if err := sink(execution.StreamEvent{
						Sequence: 1, Kind: execution.StreamEventReady, StatusCode: http.StatusOK,
						Header: http.Header{"Content-Type": {"text/event-stream"}},
					}); err != nil {
						t.Errorf("sink ready: %v", err)
					}
				}
				for i, e := range tc.events {
					if err := sink(execution.StreamEvent{
						Sequence: uint64(i + 2), Kind: execution.StreamEventData, Data: []byte(e),
					}); err != nil {
						t.Errorf("sink data: %v", err)
					}
				}
				return execution.StreamResult{
					DispatchState:   execution.DispatchMaybeSent,
					ResponseStarted: true, StatusCode: tc.status,
					Header: http.Header{"Content-Type": {"text/event-stream"}},
					Error:  tc.terminal,
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
			if result.TruncatedBeforeCommit != tc.wantReplay {
				t.Fatalf("TruncatedBeforeCommit = %v, want %v", result.TruncatedBeforeCommit, tc.wantReplay)
			}
			if tc.wantReplay {
				if result.Committed {
					t.Errorf("committed a replayable truncation")
				}
				if recorder.Body.Len() != 0 {
					t.Errorf("wrote %d bytes downstream, want 0", recorder.Body.Len())
				}
				if result.ExecutionError == nil || result.ExecutionError.Code != health.StreamTruncatedBeforeContentCode {
					t.Errorf("evidence = %+v, want code %q", result.ExecutionError, health.StreamTruncatedBeforeContentCode)
				}
			} else if tc.terminal != nil && !result.Committed {
				if result.ExecutionError == nil || result.ExecutionError.Kind != tc.terminal.Kind || result.ExecutionError.Code != tc.terminal.Code {
					t.Fatalf("failure evidence = %+v, want original %+v", result.ExecutionError, tc.terminal)
				}
			}
		})
	}
}

// 阶段从前导切换到推理时，45s 上限仍从首个被压住的字节计算，而不是重新开始。
func TestReasoningPrereadDeadlineIncludesPreamble(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		committed := false
		executor := fakeExecutionExecutor{stream: func(_ context.Context, _ execution.AttemptSpec, sink execution.StreamSink) execution.StreamResult {
			for _, event := range []execution.StreamEvent{
				{Sequence: 1, Kind: execution.StreamEventReady, StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}},
				{Sequence: 2, Kind: execution.StreamEventData, Data: []byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")},
			} {
				if err := sink(event); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(4 * time.Second)
			if err := sink(execution.StreamEvent{Sequence: 3, Kind: execution.StreamEventData, Data: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"working\"}}\n\n")}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(41 * time.Second)
			synctest.Wait()
			if !committed {
				t.Error("thinking-phase transition extended the preread beyond 45s")
			}
			return execution.StreamResult{DispatchState: execution.DispatchMaybeSent, ResponseStarted: true, StatusCode: http.StatusOK,
				Error: &execution.ErrorEvidence{Kind: execution.ErrorKindTransport, Summary: "upstream closed"}}
		}}
		input := executionForwardInput()
		input.Dialect, input.ClientProtocol = dialect.NewAnthropic(), protocol.Anthropic
		input.Request = &dialect.ParsedRequest{Method: http.MethodPost, Path: "/v1/messages"}
		input.EmptyResponseRetry = true
		input.OnStreamReady = func() { committed = true }
		result := NewExecutionForwarder(executor).ForwardStream(t.Context(), input, httptest.NewRecorder())
		if !result.Committed || result.TruncatedBeforeCommit {
			t.Fatalf("result committed=%v truncated=%v, want the expired window to prohibit replay", result.Committed, result.TruncatedBeforeCommit)
		}
	})
}

func TestEmptyThinkingStillRetriesEmptyResponse(t *testing.T) {
	executor := fakeExecutionExecutor{stream: func(_ context.Context, _ execution.AttemptSpec, sink execution.StreamSink) execution.StreamResult {
		if err := sink(execution.StreamEvent{Sequence: 1, Kind: execution.StreamEventReady, StatusCode: http.StatusOK,
			Header: http.Header{"Content-Type": {"text/event-stream"}}}); err != nil {
			t.Fatal(err)
		}
		body := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"\"}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		if err := sink(execution.StreamEvent{Sequence: 2, Kind: execution.StreamEventData, Data: []byte(body)}); err != nil {
			t.Fatal(err)
		}
		return execution.StreamResult{DispatchState: execution.DispatchMaybeSent, ResponseStarted: true, StatusCode: http.StatusOK}
	}}
	input := executionForwardInput()
	input.Dialect, input.ClientProtocol = dialect.NewAnthropic(), protocol.Anthropic
	input.Request = &dialect.ParsedRequest{Method: http.MethodPost, Path: "/v1/messages"}
	input.EmptyResponseRetry = true
	downstream := httptest.NewRecorder()
	result := NewExecutionForwarder(executor).ForwardStream(t.Context(), input, downstream)
	if !result.EmptyResponseBeforeCommit || result.Committed || downstream.Body.Len() != 0 {
		t.Fatalf("empty thinking committed=%v empty=%v bytes=%d, want an uncommitted empty response", result.Committed, result.EmptyResponseBeforeCommit, downstream.Body.Len())
	}
}

func TestResponseBindingFailureDoesNotBecomeTruncation(t *testing.T) {
	input := responsesExecutionForwardInput()
	input.EmptyResponseRetry = true
	input.OnResponse = func([]byte) error { return errors.New("response ownership conflict") }
	executor := fakeExecutionExecutor{stream: func(_ context.Context, _ execution.AttemptSpec, sink execution.StreamSink) execution.StreamResult {
		if err := sink(execution.StreamEvent{Sequence: 1, Kind: execution.StreamEventReady, StatusCode: http.StatusOK,
			Header: http.Header{"Content-Type": {"text/event-stream"}}}); err != nil {
			t.Fatal(err)
		}
		if err := sink(execution.StreamEvent{Sequence: 2, Kind: execution.StreamEventData,
			Data: []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n")}); err == nil {
			t.Fatal("response ownership conflict was ignored")
		}
		return execution.StreamResult{DispatchState: execution.DispatchMaybeSent, ResponseStarted: true, StatusCode: http.StatusOK,
			Error: &execution.ErrorEvidence{Kind: execution.ErrorKindCanceled, Summary: "stream consumer stopped"}}
	}}
	downstream := httptest.NewRecorder()
	result := NewExecutionForwarder(executor).ForwardStream(t.Context(), input, downstream)
	if result.TruncatedBeforeCommit || result.Committed || downstream.Body.Len() != 0 || result.ExecutionError == nil ||
		result.ExecutionError.Code != "response_binding_conflict" || result.ExecutionError.OriginHint != execution.ErrorOriginInternal {
		t.Fatalf("binding conflict misclassified or written: %+v", result)
	}
	decision := judgeUpstreamResult(result, time.Now(), health.DecisionContext{Method: http.MethodPost})
	if decision.Retry != health.RetryNone || decision.Origin != execution.ErrorOriginInternal {
		t.Fatalf("binding conflict decision = %+v, want no upstream replay", decision)
	}
}
