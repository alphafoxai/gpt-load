package mirasim

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestBuildProviderRequestSelectsModelWire(t *testing.T) {
	claudeBody, claudeRoute, err := buildProviderRequest(ExecuteRequest{
		Model: "claude-sonnet-5", Format: "claude",
		Payload: []byte(`{"model":"claude-sonnet-5","messages":[]}`),
	}, false)
	// A non-streaming caller still has to open Claude as SSE. Mirasim's gateway
	// returns 504 when /v1/messages waits out the whole generation.
	if err != nil || claudeRoute.Path != "/v1/messages" || !strings.Contains(string(claudeBody), `"stream":true`) {
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

// A 4xx rejection still proves which wire path Mirasim was asked to use, so the
// executor must report it. Callers derive the upstream protocol from this path,
// and Mirasim serves both /v1/messages and /v1/responses, so there is no safe
// static fallback for a Claude model.
func TestFailedExecuteStillReportsUpstreamRequestPath(t *testing.T) {
	key := testDevicePEM(t)
	var seenPath string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mirasimTestTicket(w, r) {
			return
		}
		seenPath = r.URL.Path
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"bad tool schema"}}`)
	}))
	defer relay.Close()

	credential, err := ParseCredentialJSON([]byte(`{"type":"mirasim","access_token":"access-secret","refresh_token":"refresh-secret","device_private_key":` + jsonString(key) + `,"relay_url":"` + relay.URL + `","admin_url":"` + relay.URL + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := NewExecutor().Execute(context.Background(), credential, ExecuteRequest{
		Model: "claude-opus-5-5", Format: "openai-response",
		Payload: []byte(`{"model":"mira/claude-opus-5-5","input":"hi"}`),
	})
	if err == nil {
		t.Fatal("expected the 400 to surface as an error")
	}
	if seenPath != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", seenPath)
	}
	if response.UpstreamRequestPath != "/v1/messages" {
		t.Fatalf("UpstreamRequestPath = %q, want /v1/messages on a failed attempt",
			response.UpstreamRequestPath)
	}
}

func TestCountTokensRequestStaysNonStreaming(t *testing.T) {
	body, err := normalizeBody([]byte(`{"model":"claude-opus-5-5","messages":[]}`), "claude-opus-5-5", false, sdktranslator.FormatClaude)
	if err != nil || !strings.Contains(string(body), `"stream":false`) {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestExecuteClaudeNonStreamAggregatesUpstreamSSE(t *testing.T) {
	key := testDevicePEM(t)
	var accept string
	var requestBody []byte
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mirasimTestTicket(w, r) {
			return
		}
		accept = r.Header.Get("Accept")
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ""+
			"event: message_start\n"+
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\n"+
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\n"+
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"READY\"}}\n\n"+
			"event: message_delta\n"+
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
			"event: message_stop\n"+
			"data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer relay.Close()

	response, err := NewExecutor().Execute(context.Background(), mirasimTestCredential(t, key, relay.URL), ExecuteRequest{
		Model: "claude-opus-5-5", Format: "openai-response",
		Payload: []byte(`{"model":"mira/claude-opus-5-5","input":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(requestBody), `"stream":true`) {
		t.Fatalf("upstream body = %s", requestBody)
	}
	if !strings.Contains(accept, "text/event-stream") {
		t.Fatalf("accept = %q", accept)
	}
	if response.UpstreamRequestPath != "/v1/messages" {
		t.Fatalf("path = %q", response.UpstreamRequestPath)
	}
	if response.Headers.Get("Content-Type") != "application/json" || response.Headers.Get("Content-Length") != "" {
		t.Fatalf("headers = %v", response.Headers)
	}
	if !strings.Contains(string(response.Payload), "READY") || strings.Contains(string(response.Payload), "data:") {
		t.Fatalf("payload = %s", response.Payload)
	}
}

func TestExecuteClaudeNativeNonStreamRebuildsMessage(t *testing.T) {
	key := testDevicePEM(t)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mirasimTestTicket(w, r) {
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, ""+
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_2\",\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":4}}}\n"+
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n"+
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"look\"}}\n"+
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig\"}}\n"+
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n"+
			"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n"+
			"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"READY\"}}\n"+
			"data: {\"type\":\"content_block_stop\",\"index\":1}\n"+
			"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"read\",\"input\":{}}}\n"+
			"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"a\\\"}\"}}\n"+
			"data: {\"type\":\"content_block_stop\",\"index\":2}\n"+
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7}}\n"+
			"data: {\"type\":\"message_stop\"}\n")
	}))
	defer relay.Close()

	response, err := NewExecutor().Execute(context.Background(), mirasimTestCredential(t, key, relay.URL), ExecuteRequest{
		Model: "claude-opus-5-5", Format: "claude",
		Payload: []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var message struct {
		ID         string `json:"id"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type      string         `json:"type"`
			Text      string         `json:"text"`
			Thinking  string         `json:"thinking"`
			Signature string         `json:"signature"`
			Name      string         `json:"name"`
			Input     map[string]any `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(response.Payload, &message) != nil {
		t.Fatalf("payload = %s", response.Payload)
	}
	if message.ID != "msg_2" || message.StopReason != "tool_use" || message.Usage.InputTokens != 4 || message.Usage.OutputTokens != 7 {
		t.Fatalf("message meta = %+v", message)
	}
	if len(message.Content) != 3 || message.Content[0].Thinking != "look" || message.Content[0].Signature != "sig" {
		t.Fatalf("thinking = %+v", message.Content)
	}
	if message.Content[1].Text != "READY" || message.Content[2].Name != "read" || message.Content[2].Input["path"] != "a" {
		t.Fatalf("content = %+v", message.Content)
	}
}

func TestExecuteClaudeNonStreamFailsOnSSEError(t *testing.T) {
	key := testDevicePEM(t)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mirasimTestTicket(w, r) {
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n")
	}))
	defer relay.Close()

	_, err := NewExecutor().Execute(context.Background(), mirasimTestCredential(t, key, relay.URL), ExecuteRequest{
		Model: "claude-opus-5-5", Format: "openai-response",
		Payload: []byte(`{"model":"mira/claude-opus-5-5","input":"hi"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("err = %v", err)
	}
}

func mirasimTestTicket(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/v1/device/session" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"ticket":"device-ticket","expiresIn":3600}`)
	return true
}

func mirasimTestCredential(t *testing.T, key, relayURL string) Storage {
	t.Helper()
	credential, err := ParseCredentialJSON([]byte(`{"type":"mirasim","access_token":"access-secret","refresh_token":"refresh-secret","device_private_key":` + jsonString(key) + `,"relay_url":"` + relayURL + `","admin_url":"` + relayURL + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func TestFailedExecuteStreamStillReportsUpstreamRequestPath(t *testing.T) {
	key := testDevicePEM(t)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request"}}`)
	}))
	defer relay.Close()

	credential, err := ParseCredentialJSON([]byte(`{"type":"mirasim","access_token":"access-secret","refresh_token":"refresh-secret","device_private_key":` + jsonString(key) + `,"relay_url":"` + relay.URL + `","admin_url":"` + relay.URL + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, _, err := NewExecutor().ExecuteStream(context.Background(), credential, ExecuteRequest{
		Model: "claude-opus-5-5", Format: "openai-response",
		Payload: []byte(`{"model":"mira/claude-opus-5-5","input":"hi"}`),
	})
	if err == nil {
		t.Fatal("expected the 400 to surface as an error")
	}
	if response.UpstreamRequestPath != "/v1/messages" {
		t.Fatalf("UpstreamRequestPath = %q, want /v1/messages on a failed stream attempt",
			response.UpstreamRequestPath)
	}
}
