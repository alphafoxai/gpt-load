package mirasim

import (
	"encoding/json"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestClaudeJSONMessageBecomesResponsesOutput(t *testing.T) {
	message := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"look","signature":"sig"},{"type":"text","text":"READY"},{"type":"tool_use","id":"toolu_1","name":"read","input":{"path":"a"}}],"stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":7}}`)
	translated, err := translateNonStream(t.Context(), sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, "claude-opus-5-5", []byte(`{"model":"mira/claude-opus-5-5"}`), nil, message)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(translated, &parsed) != nil {
		t.Fatalf("translated = %s", translated)
	}
	if parsed.Status != "completed" || parsed.Usage.InputTokens != 11 || parsed.Usage.OutputTokens != 7 {
		t.Fatalf("status=%s usage=%+v", parsed.Status, parsed.Usage)
	}
	if len(parsed.Output) != 3 || parsed.Output[0].Type != "reasoning" || parsed.Output[0].Summary[0].Text != "look" {
		t.Fatalf("reasoning = %+v", parsed.Output)
	}
	if parsed.Output[1].Type != "message" || parsed.Output[1].Content[0].Text != "READY" {
		t.Fatalf("message = %+v", parsed.Output[1])
	}
	if parsed.Output[2].Type != "function_call" || parsed.Output[2].Name != "read" || parsed.Output[2].Arguments != `{"path":"a"}` {
		t.Fatalf("tool = %+v", parsed.Output[2])
	}
}

func TestClaudeSSEResponseIsLeftUntouched(t *testing.T) {
	body := []byte("data: {\"type\":\"message_stop\"}\n")
	if got := claudeJSONMessageAsSSE(body); string(got) != string(body) {
		t.Fatalf("sse body changed: %s", got)
	}
}
