package qoder

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQoderBodyCarriesSystemToolsAndModel(t *testing.T) {
	raw := []byte(`{"model":"Qwen3.8-Flash","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"echo","description":"say","parameters":{"type":"object"}}}],"tool_choice":"required","reasoning_effort":"low"}`)
	var chat openAIChat
	if err := json.Unmarshal(raw, &chat); err != nil {
		t.Fatal(err)
	}
	model := fallbackModel(chat.Model)
	model.Thinks = true
	model.Efforts = []string{"low", "high"}
	body, err := qoderBody(chat, raw, model, time.UnixMilli(10))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("You are a Qoder agent")) || !bytes.Contains(body, []byte("be brief")) {
		t.Fatalf("system missing: %s", body)
	}
	if !bytes.Contains(body, []byte(`"key":"Qwen3.8-Flash"`)) {
		t.Fatalf("model config missing: %s", body)
	}
	if !bytes.Contains(body, []byte(`"enable_thinking":true`)) || !bytes.Contains(body, []byte(`"reasoning_effort":"low"`)) {
		t.Fatalf("effort missing: %s", body)
	}
	if !bytes.Contains(body, []byte(`"name":"echo"`)) || !bytes.Contains(body, []byte("must call")) {
		t.Fatalf("tools missing: %s", body)
	}
	plain := decodeRequestBody(encodeRequestBody(body))
	if !bytes.Equal(plain, body) {
		t.Fatal("body codec did not round-trip")
	}
}

func TestToolCallSplitter(t *testing.T) {
	var split splitter
	var got []piece
	for _, part := range []string{"hello <tool_ca", "ll>{\"name\":\"echo\",\"arguments\":{\"x\":1}}</tool_call> tail"} {
		got = append(got, split.feed(part)...)
	}
	got = append(got, split.flush()...)
	if len(got) != 3 || got[0].text != "hello " || got[1].call == nil || got[1].call.Name != "echo" || got[1].call.Args != `{"x":1}` || got[2].text != " tail" {
		t.Fatalf("pieces = %#v", got)
	}
}

func TestReadEventsLiftsXMLToolCall(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"body":"{\"choices\":[{\"delta\":{\"content\":\"<tool_call><function=echo><parameter=x>1</parameter></function></tool_call>\"}}]}"}`,
		`data: {"body":"{\"choices\":[{\"finish_reason\":\"stop\"}]}"}`,
		`data: {"body":"[DONE]"}`,
		"",
	}, "\n")
	events := readEvents(strings.NewReader(sse))
	raw, status, err := openAICompletion("m", "id", 1, events)
	if err != nil || status != 200 {
		t.Fatal(err, status)
	}
	if !bytes.Contains(raw, []byte(`"name":"echo"`)) || !bytes.Contains(raw, []byte(`"tool_calls"`)) {
		t.Fatalf("completion %s", raw)
	}
}

func TestContextWindowRisesOnlyWhenNeeded(t *testing.T) {
	model := modelSpec{Windows: []int{200000, 400000, 1000000}, DefaultWindow: 200000, Context: 1000000}
	if got := windowFor([]byte("small"), model); got != 200000 {
		t.Fatalf("small window %d", got)
	}
	if got := windowFor(bytes.Repeat([]byte("x"), 200000*3+10), model); got != 400000 {
		t.Fatalf("raised window %d", got)
	}
}
