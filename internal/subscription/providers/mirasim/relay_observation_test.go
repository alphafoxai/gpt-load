package mirasim

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Exercise real HTTP dispatch, not only the wire-selection helper: a relay
// rejection must retain the selected path even when there is no success body.
func TestRelayRejectionRetainsSelectedPath(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5-5", "claude-fable-5-1", "claude-opus-5-5", "gpt-6-astra"} {
		for _, stream := range []bool{false, true} {
			name := model + "/unary"
			if stream {
				name = model + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				seenPath := ""
				relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v1/device/session" {
						http.NotFound(w, r)
						return
					}
					seenPath = r.URL.Path
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "23")
					w.Header().Set("X-Request-Id", "relay-rejected")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":{"message":"The platform is busy; retry shortly."}}`))
				}))
				defer relay.Close()
				credential := Storage{Type: "mirasim", AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", DevicePrivateKey: testDevicePEM(t), RelayURL: relay.URL, AdminURL: relay.URL}
				request := ExecuteRequest{Model: model, Format: "claude", Payload: []byte(`{"model":"` + model + `","max_tokens":32,"messages":[{"role":"user","content":"pong"}]}`)}
				var response ExecuteResponse
				var err error
				if stream {
					var body interface{ Close() error }
					response, body, err = NewExecutor().ExecuteStream(context.Background(), credential, request)
					if body != nil {
						_ = body.Close()
						t.Fatal("rejection must not return a success stream")
					}
				} else {
					response, err = NewExecutor().Execute(context.Background(), credential, request)
				}
				var status *StatusError
				if !errors.As(err, &status) || status.StatusCode() != 503 {
					t.Fatalf("expected relay HTTP503, got %v", err)
				}
				expected := "/v1/messages"
				if model == "gpt-6-astra" {
					expected = "/v1/responses"
				}
				if seenPath != expected {
					t.Fatalf("actual HTTP path=%q want %q", seenPath, expected)
				}
				if response.UpstreamRequestPath != expected {
					t.Fatalf("lost actual HTTP path on failure: captured=%q observed=%q", seenPath, response.UpstreamRequestPath)
				}
				if response.StatusCode != 503 || response.Headers.Get("X-Request-Id") != "relay-rejected" {
					t.Fatalf("lost rejection metadata: %#v", response)
				}
				if retry := status.RetryAfter(); retry == nil || *retry != 23*time.Second {
					t.Fatalf("retry after=%v", retry)
				}
			})
		}
	}
}
