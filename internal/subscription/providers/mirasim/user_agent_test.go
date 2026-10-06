package mirasim

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// relay.mirasim.ai refuses any inference request whose User-Agent it does not
// recognise with 403 "this client is not supported; use the mirasim client or
// Claude Code". Go would otherwise send Go-http-client/1.1, which is exactly
// what the relay rejects.
func TestRelayIdentifiesItselfAsMirasimClient(t *testing.T) {
	for _, tc := range []struct {
		name          string
		clientVersion string
		want          string
	}{
		{name: "enrolled version", clientVersion: "0.0.403", want: "mirasim/0.0.403"},
		{name: "empty version falls back", clientVersion: "", want: "mirasim/" + defaultClientVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := testDevicePEM(t)
			var userAgent string
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				userAgent = r.Header.Get("User-Agent")
				switch r.URL.Path {
				case "/v1/device/session":
					_, _ = io.WriteString(w, `{"ticket":"t","expiresIn":600}`)
				case "/v1/messages":
					_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer relay.Close()

			credential, err := ParseCredentialJSON([]byte(`{"type":"mirasim","access_token":"access-secret","refresh_token":"refresh-secret","device_private_key":` + jsonString(key) + `,"relay_url":"` + relay.URL + `","admin_url":"` + relay.URL + `","client_version":"` + tc.clientVersion + `"}`))
			if err != nil {
				t.Fatal(err)
			}
			result, err := NewClient(credential).Do(context.Background(), http.MethodPost, "/v1/messages", nil, nil,
				[]byte(`{"model":"claude-opus-5-5"}`))
			if err != nil {
				t.Fatalf("do = %v", err)
			}
			if result.StatusCode != http.StatusOK {
				t.Fatalf("status = %d body = %s", result.StatusCode, result.Body)
			}
			if userAgent != tc.want {
				t.Fatalf("user-agent = %q, want %q", userAgent, tc.want)
			}
			if strings.Contains(userAgent, "Go-http-client") {
				t.Fatalf("user-agent leaked Go default: %q", userAgent)
			}
		})
	}
}

// A downstream agent string must not be forwarded verbatim: the relay gates on
// the client it recognises, not on whoever called the proxy.
func TestRelayOverwritesDownstreamUserAgent(t *testing.T) {
	key := testDevicePEM(t)
	var userAgent string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/v1/device/session":
			_, _ = io.WriteString(w, `{"ticket":"t","expiresIn":600}`)
		default:
			_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		}
	}))
	defer relay.Close()

	credential, err := ParseCredentialJSON([]byte(`{"type":"mirasim","access_token":"access-secret","refresh_token":"refresh-secret","device_private_key":` + jsonString(key) + `,"relay_url":"` + relay.URL + `","admin_url":"` + relay.URL + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	downstream := http.Header{"User-Agent": []string{"curl/8.7.1"}}
	if _, err := NewClient(credential).Do(context.Background(), http.MethodPost, "/v1/messages", nil, downstream,
		[]byte(`{"model":"claude-opus-5-5"}`)); err != nil {
		t.Fatalf("do = %v", err)
	}
	if userAgent != "mirasim/"+defaultClientVersion {
		t.Fatalf("downstream user-agent was forwarded: %q", userAgent)
	}
}