package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func writeSSE(w http.ResponseWriter, body string) {
	raw, _ := json.Marshal(map[string]string{"body": body})
	fmt.Fprintf(w, "data: %s\n\n", raw)
}

func TestExecuteSignsAndTranslatesSSE(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/model/list") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		seen = string(decodeRequestBody(string(body)))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer COSY.") {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Cosy-User") != "user-1" {
			t.Errorf("user %q", r.Header.Get("Cosy-User"))
		}
		if r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("X-Model-Key") != "Qwen3.8-Flash" || r.Header.Get("Cache-Control") != "no-cache" {
			t.Errorf("chat headers accept=%q key=%q cache=%q", r.Header.Get("Accept"), r.Header.Get("X-Model-Key"), r.Header.Get("Cache-Control"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, `{"choices":[{"delta":{"content":"hello"}}]}`)
		writeSSE(w, `{"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
		writeSSE(w, "[DONE]")
	}))
	defer server.Close()
	cred := Credential{
		Site: siteQoder, UID: "user-1", AccessToken: "jt-1", RefreshToken: "jr-1",
		ExpiresAt: time.Now().Add(time.Hour), MachineID: "machine-1",
	}
	original := siteGlobal
	siteGlobal.API = server.URL
	t.Cleanup(func() { siteGlobal = original })
	body, _, _, err := Execute(WithHTTPClient(context.Background(), server.Client()), cred, "openai", []byte(`{"model":"Qwen3.8-Flash","messages":[{"role":"user","content":"hi"}]}`), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, `"text":"hi"`) || !strings.Contains(seen, "You are a Qoder agent") {
		t.Fatalf("upstream body %s", seen)
	}
	if !strings.Contains(string(body), `"content":"hello"`) || !strings.Contains(string(body), `"prompt_tokens":3`) {
		t.Fatalf("completion %s", body)
	}
}
