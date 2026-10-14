package mirasim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestBuildProviderRequestSelectsModelWire(t *testing.T) {
	claudeBody, claudeRoute, err := buildProviderRequest(ExecuteRequest{
		Model: "claude-sonnet-5", Format: "claude",
		Payload: []byte(`{"model":"claude-sonnet-5","messages":[]}`),
	}, false)
	if err != nil || claudeRoute.Path != "/v1/messages" || !strings.Contains(string(claudeBody), `"stream":false`) {
		t.Fatalf("claude route = %#v body=%s err=%v", claudeRoute, claudeBody, err)
	}
	gptBody, gptRoute, err := buildProviderRequest(ExecuteRequest{
		Model: "gpt-5.6", Format: "openai-response",
		Payload: []byte(`{"model":"gpt-5.6","input":"hello"}`),
	}, true)
	if err != nil || gptRoute.Path != "/v1/responses" || !strings.Contains(string(gptBody), `"stream":true`) {
		t.Fatalf("gpt route = %#v body=%s err=%v", gptRoute, gptBody, err)
	}
	if strings.Contains(string(gptBody), "x-anthropic-billing-header") {
		t.Fatalf("gpt body gained a Claude billing header: %s", gptBody)
	}
	for _, model := range []string{"deepseek-flash", "glm-5.3-flash"} {
		body, route, err := buildProviderRequest(ExecuteRequest{
			Model: model, Format: "claude",
			Payload: []byte(`{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`),
		}, false)
		if err != nil || route.Path != "/v1/messages" {
			t.Fatalf("%s route = %#v err=%v", model, route, err)
		}
		if strings.Contains(string(body), "x-anthropic-billing-header") {
			t.Fatalf("%s body gained a Claude billing header: %s", model, body)
		}
		if !strings.Contains(string(body), `"stream":false`) {
			t.Fatalf("%s body = %s", model, body)
		}
	}
}

func TestBuildProviderRequestRestoresBuiltinEffort(t *testing.T) {
	for _, model := range []string{"deepseek-flash", "glm-5.3-flash"} {
		for _, effort := range []string{"low", "high", "max"} {
			body, route, err := buildProviderRequest(ExecuteRequest{
				Model: model, Format: "openai",
				Payload: []byte(`{"model":"` + model + `","reasoning_effort":"` + effort + `","messages":[{"role":"user","content":"hi"}]}`),
			}, false)
			if err != nil || route.Path != "/v1/messages" {
				t.Fatalf("%s %s route = %#v err=%v", model, effort, route, err)
			}
			if !strings.Contains(string(body), `"type":"adaptive"`) || !strings.Contains(string(body), `"effort":"`+effort+`"`) {
				t.Fatalf("%s %s body = %s", model, effort, body)
			}
		}
		body, _, err := buildProviderRequest(ExecuteRequest{
			Model: model, Format: "claude",
			Payload: []byte(`{"model":"` + model + `","thinking":{"type":"adaptive"},"output_config":{"effort":"HIGH"},"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`),
		}, false)
		if err != nil || !strings.Contains(string(body), `"effort":"high"`) {
			t.Fatalf("%s native effort body = %s err=%v", model, body, err)
		}
	}
	body, _, err := buildProviderRequest(ExecuteRequest{
		Model: "claude-opus-5-5", Format: "claude",
		Payload: []byte(`{"model":"claude-opus-5-5","reasoning_effort":"max","messages":[{"role":"user","content":"hi"}]}`),
	}, false)
	if err != nil || strings.Contains(string(body), `"effort":"max"`) {
		t.Fatalf("claude body should not gain a builtin effort: %s err=%v", body, err)
	}
}

func TestBuildProviderRequestSuppliesClaudeBillingHeaderWhenMissing(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantLen int
	}{
		{name: "no system", payload: `{"model":"claude-opus-5-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, wantLen: 1},
		{name: "string system", payload: `{"model":"claude-opus-5-5","system":"be brief","messages":[{"role":"user","content":"hi"}]}`, wantLen: 2},
		{name: "array without marker", payload: `{"model":"claude-opus-5-5","system":[{"type":"text","text":"be brief"}],"messages":[{"role":"user","content":"hi"}]}`, wantLen: 2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body, route, err := buildProviderRequest(ExecuteRequest{
				Model: "claude-opus-5-5", Format: "claude", Payload: []byte(testCase.payload),
			}, false)
			if err != nil {
				t.Fatalf("buildProviderRequest() error = %v", err)
			}
			if route.Path != "/v1/messages" {
				t.Fatalf("path = %s", route.Path)
			}
			blocks := systemBlocks(t, body)
			if len(blocks) != testCase.wantLen {
				t.Fatalf("system blocks = %#v, want %d", blocks, testCase.wantLen)
			}
			if !isClaudeBillingHeader(blocks[0]) {
				t.Fatalf("first system block = %q", blocks[0])
			}
		})
	}
}

func TestBuildProviderRequestKeepsClientClaudeBillingHeader(t *testing.T) {
	original := "x-anthropic-billing-header: cc_version=2.1.281.df4; cc_entrypoint=sdk-cli;"
	payload := `{"model":"claude-opus-5-5","system":[{"type":"text","text":` + jsonQuote(original) + `},{"type":"text","text":"be brief"}],"messages":[{"role":"user","content":"hi"}]}`
	body, _, err := buildProviderRequest(ExecuteRequest{
		Model: "claude-opus-5-5", Format: "claude", Payload: []byte(payload),
	}, false)
	if err != nil {
		t.Fatalf("buildProviderRequest() error = %v", err)
	}
	blocks := systemBlocks(t, body)
	if len(blocks) != 2 || blocks[0] != original || blocks[1] != "be brief" {
		t.Fatalf("system blocks = %#v", blocks)
	}
}

func systemBlocks(t *testing.T, body []byte) []string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	raw, ok := payload["system"].([]any)
	if !ok {
		t.Fatalf("system = %#v", payload["system"])
	}
	texts := make([]string, 0, len(raw))
	for _, block := range raw {
		item, _ := block.(map[string]any)
		text, _ := item["text"].(string)
		texts = append(texts, text)
	}
	return texts
}

func jsonQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// 转换器返回的是逻辑 JSON 或 SSE 事件，交给 io.ReadCloser 前必须成为完整线缆 SSE。
func TestTranslatedClaudeStreamHasRealSSEBoundaries(t *testing.T) {
	const prefix = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"完整答案\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n"
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatGemini, sdktranslator.FormatOpenAIResponse} {
		for _, stopped := range []bool{false, true} {
			name := string(format) + "/truncated"
			input := prefix
			if stopped {
				name = string(format) + "/complete"
				input += "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			}
			t.Run(name, func(t *testing.T) {
				reader := translateStream(context.Background(), sdktranslator.FormatClaude, format, "claude-opus-5-5", []byte(`{}`), []byte(`{}`), io.NopCloser(strings.NewReader(input)))
				defer reader.Close()
				wire, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.HasSuffix(wire, []byte("\n\n")) {
					t.Fatalf("translated output lacks SSE delimiter: %q", wire)
				}
				scanner := bufio.NewScanner(bytes.NewReader(wire))
				var text strings.Builder
				done := false
				for scanner.Scan() {
					line := scanner.Bytes()
					if len(line) == 0 || bytes.HasPrefix(line, []byte("event:")) {
						continue
					}
					if !bytes.HasPrefix(line, []byte("data:")) {
						t.Fatalf("bare translated JSON leaked: %q", line)
					}
					payload := bytes.TrimSpace(line[5:])
					if bytes.Equal(payload, []byte("[DONE]")) {
						done = true
						continue
					}
					if !json.Valid(payload) {
						t.Fatalf("invalid converted SSE JSON: %q", payload)
					}
					text.Write(payload)
				}
				if !strings.Contains(text.String(), "完整答案") {
					t.Fatalf("answer lost in conversion: %s", text.String())
				}
				if format == sdktranslator.FormatOpenAI && done != stopped {
					t.Fatalf("DONE=%v, observed message_stop=%v", done, stopped)
				}
			})
		}
	}
}

func TestTranslatedResponsesStreamMapsCompletedToChatDONE(t *testing.T) {
	input := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	reader := translateStream(context.Background(), sdktranslator.FormatCodex, sdktranslator.FormatOpenAI, "gpt-6-astra", []byte(`{}`), []byte(`{}`), io.NopCloser(strings.NewReader(input)))
	defer reader.Close()
	wire, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte("data: [DONE]")) {
		t.Fatalf("Responses completed was not mapped to Chat DONE: %q", wire)
	}
	truncated := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"
	reader = translateStream(context.Background(), sdktranslator.FormatCodex, sdktranslator.FormatOpenAI, "gpt-6-astra", []byte(`{}`), []byte(`{}`), io.NopCloser(strings.NewReader(truncated)))
	defer reader.Close()
	wire, err = io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire, []byte("data: [DONE]")) {
		t.Fatalf("truncated Responses stream forged DONE: %q", wire)
	}
}
