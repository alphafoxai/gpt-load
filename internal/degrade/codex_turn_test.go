package degrade

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCodexTurnCarriesTheCapturedContext(t *testing.T) {
	body, err := CodexTurn("gpt-6-luna", "写出数字", time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CodexTurn() error = %v", err)
	}
	var payload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
		Input  []struct {
			Type string `json:"type"`
			Role string `json:"role"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode turn: %v", err)
	}
	if payload.Model != "gpt-6-luna" || !payload.Stream {
		t.Fatalf("turn = %+v, want a streaming gpt-6-luna request", payload)
	}
	if len(payload.Input) < 4 || payload.Input[0].Type != "additional_tools" {
		t.Fatalf("input = %+v, want the Codex tools prefix", payload.Input)
	}
	text := string(body)
	for _, want := range []string{codexContext.BaseInstructions[:40], "写出数字", "2026-10-07", `"type":"input_text"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("turn missing %q", want)
		}
	}
	if strings.Contains(text, `\u003c`) {
		t.Fatal("turn escaped HTML, Codex sends it literally")
	}
}
