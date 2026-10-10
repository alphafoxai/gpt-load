package control

import "testing"

// Codex streams the answer as output_text deltas and only closes the message
// item once the turn ends. A stream that dies mid-answer — upstream timeout,
// reset connection, gateway cancellation — therefore carries every number it
// already produced in deltas and none in a finished item. Reading only the
// finished item turns a substantially correct answer into "no numbers".
func TestDegradeStreamTextKeepsDeltasWhenTheStreamIsCut(t *testing.T) {
	events := []byte("" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"12, 34, 56\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\", 78, 90\"}\n\n" +
		"data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"output\":[],\"usage\":{\"output_tokens\":9}}}\n\n")
	want := "12, 34, 56, 78, 90"
	if got := degradeStreamText(events); got != want {
		t.Fatalf("degradeStreamText() = %q, want the numbers streamed before the cut %q", got, want)
	}
}

// The finished item is authoritative: it is the only place a refusal or a
// tool call is distinguished from an answer, so deltas must never override it.
func TestDegradeStreamTextPrefersFinishedItemOverDeltas(t *testing.T) {
	events := []byte("" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"1 2 3\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"7 8 9\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n")
	if got := degradeStreamText(events); got != "7 8 9" {
		t.Fatalf("degradeStreamText() = %q, want the finished item %q", got, "7 8 9")
	}
}

// Reasoning summaries stream their own deltas; they are not the answer and
// their numbers must not be attributed to the model's choice sequence.
func TestDegradeStreamTextIgnoresReasoningDeltas(t *testing.T) {
	events := []byte("" +
		"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"999 999 999\"}\n\n" +
		"data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"888 888\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"4 5 6\"}\n\n" +
		"data: {\"type\":\"response.incomplete\",\"response\":{\"output\":[]}}\n\n")
	if got := degradeStreamText(events); got != "4 5 6" {
		t.Fatalf("degradeStreamText() = %q, want only the answer delta %q", got, "4 5 6")
	}
}
