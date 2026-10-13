package gateway

import (
	"net/http"
	"testing"

	"gpt-load/internal/health"
	"gpt-load/internal/telemetry"
)

// An exhausted pre-content truncation is delivered to the client as raw upstream
// bytes over HTTP 200, but it is NOT a success: the client received a half answer.
// It must be recorded as incomplete with the truncation reason, not as success.
func TestCompleteTruncatedRecordsIncomplete(t *testing.T) {
	recorder := &requestRecorder{
		stream: true,
		attempts: []telemetry.Attempt{{
			ErrorSummary: "Upstream stream ended before any visible content.",
		}},
	}
	result := UpstreamResult{
		StatusCode:            http.StatusOK,
		Header:                http.Header{"Content-Type": {"text/event-stream"}},
		Body:                  []byte("event: message_start\n\n"),
		TruncatedBeforeCommit: true,
		ErrorSummary:          "Upstream stream ended before any visible content.",
	}

	recorder.completeTruncated(result, health.Decision{Category: health.FailureCategoryAmbiguous}, "claude-opus-5-5", 0)

	if recorder.outcome.status == telemetry.RequestStatusSuccess {
		t.Fatalf("status = %v, want incomplete (a half answer is not a success)", recorder.outcome.status)
	}
	if recorder.outcome.status != telemetry.RequestStatusIncomplete {
		t.Fatalf("status = %v, want %v", recorder.outcome.status, telemetry.RequestStatusIncomplete)
	}
	if recorder.outcome.statusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, want %d", recorder.outcome.statusCode, http.StatusOK)
	}
	if recorder.outcome.errorCode != health.StreamTruncatedBeforeContentCode {
		t.Fatalf("errorCode = %q, want %q", recorder.outcome.errorCode, health.StreamTruncatedBeforeContentCode)
	}
	if recorder.outcome.errorSummary == "" {
		t.Fatal("errorSummary is empty, want the truncation reason preserved")
	}
}
