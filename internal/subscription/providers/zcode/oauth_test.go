package zcode

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceSignInMintsBigModelCodingPlanKey(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/oauth/cli/init" && r.Method == http.MethodPost:
			if r.Header.Get("X-Device-Mid") == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("init headers missing device or poll auth")
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"flow_id":"flow-1","authorize_url":"https://zcode.z.ai/oauth/start","poll_interval_sec":1}}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/oauth/cli/poll/"):
			_, _ = io.WriteString(w, `{"code":200,"data":{"status":"ready","bigmodel":{"access_token":"biz-token"},"user":{"email":"owner@example.com"}}}`)
		case r.URL.Path == "/api/biz/customer/getCustomerInfo":
			if r.Header.Get("Authorization") != "biz-token" {
				t.Errorf("business auth = %q", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"organizations":[{"organizationId":"org","organizationName":"默认机构","projects":[{"projectId":"proj","projectName":"默认项目","projectType":"1"}]}]}}`)
		case r.URL.Path == "/api/biz/v1/organization/org/projects/proj/api_keys" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"code":0,"data":[]}`)
		case r.URL.Path == "/api/biz/v1/organization/org/projects/proj/api_keys" && r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `{"code":0,"data":{"apiKey":"keyid"}}`)
		case strings.HasSuffix(r.URL.Path, "/copy/keyid"):
			_, _ = io.WriteString(w, `{"code":0,"data":{"secretKey":"secret"}}`)
		case r.URL.Path == "/api/biz/subscription/list":
			if r.Header.Get("Authorization") != "keyid.secret" {
				t.Errorf("plan auth = %q", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"code":0,"data":[{"status":"VALID","productName":"GLM Coding Plan"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(Endpoints{
		ZCode: server.URL, ZAIAPI: server.URL, BigModelAPI: server.URL,
		ZAIAnthropic: "https://api.z.ai/api/anthropic", BigModelAnthropic: "https://open.bigmodel.cn/api/anthropic",
	})
	ctx := WithClient(context.Background(), client)
	started, err := driver{}.BeginDeviceAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if started.UserCode != "zcode" || !strings.Contains(started.VerificationURL, "redirect=") || len(started.Providers) != 2 {
		t.Fatalf("authorization URL %q providers %d", started.VerificationURL, len(started.Providers))
	}
	polled, err := driver{}.PollDeviceAuthorization(ctx, started.DriverState)
	if err != nil {
		t.Fatal(err)
	}
	if polled.Status != "authorized" || polled.Credential.Identity() != "bigmodel:owner@example.com" {
		t.Fatalf("poll status %q identity %q", polled.Status, polled.Credential.Identity())
	}
}

func TestExecuteTranslatesOpenAIResponsesToAnthropic(t *testing.T) {
	var gotBody string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"GLM-5.3","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()
	previous := newUpstreamClient
	newUpstreamClient = func(string) (*http.Client, error) { return server.Client(), nil }
	t.Cleanup(func() { newUpstreamClient = previous })
	body, _, _, err := Execute(context.Background(), Credential{
		Site: "bigmodel", BaseURL: server.URL, APIKey: "keyid.secret",
	}, "openai-response", []byte(`{"model":"GLM-5.3","input":"ping"}`), nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, `"input"`) || !strings.Contains(gotBody, `"messages"`) {
		t.Fatalf("upstream body = %s", gotBody)
	}
	if !strings.Contains(string(body), `"output"`) && !strings.Contains(string(body), "ok") {
		t.Fatalf("client body = %s", body)
	}
}

func TestExecutePostsAnthropicMessages(t *testing.T) {
	var gotPath, gotKey, gotAuth, gotVersion string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("anthropic-version")
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"GLM-5.3","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()
	previous := newUpstreamClient
	newUpstreamClient = func(string) (*http.Client, error) { return server.Client(), nil }
	t.Cleanup(func() { newUpstreamClient = previous })
	body, _, _, err := Execute(context.Background(), Credential{
		Site: "bigmodel", BaseURL: server.URL, APIKey: "keyid.secret",
	}, "claude", []byte(`{"model":"GLM-5.3","max_tokens":8,"messages":[{"role":"user","content":"ping"}]}`), nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/messages" || gotKey != "keyid.secret" || gotAuth != "Bearer keyid.secret" || gotVersion != "2023-06-01" {
		t.Fatalf("path=%s key=%s auth=%s version=%s", gotPath, gotKey, gotAuth, gotVersion)
	}
	if !strings.Contains(string(body), `"text":"ok"`) {
		t.Fatalf("body = %s", body)
	}
}
