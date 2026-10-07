package control

import "testing"

func TestDegradeStreamTextUsesCompletedItemsWhenOutputIsEmpty(t *testing.T) {
	events := []byte("" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"12, 34, 56\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":3}}}\n\n")
	if got := degradeStreamText(events); got != "12, 34, 56" {
		t.Fatalf("degradeStreamText() = %q, want the completed item", got)
	}
}

func TestDegradeStreamTextPrefersTheFinalResponse(t *testing.T) {
	events := []byte("" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"stale\"}]}}\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"7 8 9\"}]}]}}\n")
	if got := degradeStreamText(events); got != "7 8 9" {
		t.Fatalf("degradeStreamText() = %q, want the final response", got)
	}
}

func TestDegradeStreamTextIgnoresToolCalls(t *testing.T) {
	events := []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"content\":[{\"type\":\"output_text\",\"text\":\"999\"}]}}\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n")
	if got := degradeStreamText(events); got != "" {
		t.Fatalf("degradeStreamText() = %q, want no tool text", got)
	}
}
