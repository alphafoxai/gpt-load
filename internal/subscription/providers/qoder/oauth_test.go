package qoder

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func TestDeviceSignInPollsThenExchanges(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == devicePollPath:
			polls++
			if polls == 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if r.URL.Query().Get("verifier") == "" || r.URL.Query().Get("nonce") == "" {
				t.Errorf("poll query = %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "dt-1", "refresh_token": "dr-1", "user_id": "user-1", "user_name": "Ada"})
		case r.URL.Path == userInfoPath:
			_ = json.NewEncoder(w).Encode(map[string]string{"email": "ada.example", "name": "Ada"})
		case r.URL.Path == jobTokenPath:
			if r.Header.Get("Authorization") != "Bearer dt-1" {
				t.Errorf("job auth = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "jt-1", "refresh_token": "jr-1", "expires_in": 3600000})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx := WithClient(context.Background(), &apiClient{http: server.Client(), sites: []site{testSite(server.URL)}})
	started, err := driver{}.BeginDeviceAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if started.UserCode != "qoder" || !strings.Contains(started.VerificationURL, "challenge_method=S256") {
		t.Fatalf("start = %#v", started)
	}
	if len(started.Providers) != 1 || started.Providers[0].Label != "Qoder" {
		t.Fatalf("providers = %#v", started.Providers)
	}
	pending, err := driver{}.PollDeviceAuthorization(ctx, started.DriverState)
	if err != nil || pending.Status != subscriptionruntime.DeviceAuthorizationPending {
		t.Fatalf("pending = %#v %v", pending.Status, err)
	}
	ready, err := driver{}.PollDeviceAuthorization(ctx, pending.DriverState)
	if err != nil || ready.Status != subscriptionruntime.DeviceAuthorizationAuthorized {
		t.Fatalf("ready = %#v %v", ready.Status, err)
	}
	value, err := ParseCredentialJSON(ready.Credential.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if value.Site != siteQoder || value.UID != "user-1" || value.AccessToken != "jt-1" || value.Email != "ada.example" || value.DeviceChat {
		t.Fatalf("credential = %#v", value)
	}
	if ready.Credential.Identity() != "qoder:user-1" {
		t.Fatalf("identity %s", ready.Credential.Identity())
	}
}

func TestQoderCNFallsBackToDeviceToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case devicePollPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "dt-cn", "refresh_token": "dr-cn", "user_id": "cn-1", "expires_in": 3600})
		case jobTokenPath:
			http.Error(w, "no job token", http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	cn := testSite(server.URL)
	cn.ID, cn.Name, cn.DeviceChat = siteQoderCN, "Qoder CN", true
	ctx := WithClient(context.Background(), &apiClient{http: server.Client(), sites: []site{cn}})
	started, err := driver{}.BeginDeviceAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := driver{}.PollDeviceAuthorization(ctx, started.DriverState)
	if err != nil || ready.Status != subscriptionruntime.DeviceAuthorizationAuthorized {
		t.Fatalf("ready = %#v %v", ready.Status, err)
	}
	value, err := ParseCredentialJSON(ready.Credential.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if !value.DeviceChat || value.AccessToken != "dt-cn" || value.RefreshToken != "dr-cn" {
		t.Fatalf("credential = %#v", value)
	}
}

func TestRefreshReplacesTheSpentJobToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "jr-1") {
			t.Errorf("body %s", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "jt-2", "refresh_token": "jr-2", "expires_in": 1000})
	}))
	defer server.Close()
	current := Credential{
		Site: siteQoder, UID: "user-1", AccessToken: "jt-1", RefreshToken: "jr-1",
		ExpiresAt: time.Now().Add(time.Minute), DeviceToken: "dt-1", MachineID: "machine-1",
	}
	stored, err := runtimeCredential(current)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithClient(context.Background(), &apiClient{http: server.Client(), sites: []site{testSite(server.URL)}})
	refreshed, err := driver{}.Refresh(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	value, err := ParseCredentialJSON(refreshed.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if value.AccessToken != "jt-2" || value.RefreshToken != "jr-2" || value.UID != "user-1" {
		t.Fatalf("refreshed = %#v", value)
	}
	if !(driver{}).MatchesRefreshIdentity(stored, refreshed) {
		t.Fatal("identity changed")
	}
}

func TestRefreshRefusalNeedsSignIn(t *testing.T) {
	decision := driver{}.ClassifyRefreshFailure(&statusError{op: "job token refresh", status: http.StatusUnauthorized})
	if decision.Kind != subscriptionruntime.RefreshFailureReauthorizationRequired || decision.StatusCode != http.StatusUnauthorized {
		t.Fatalf("decision = %#v", decision)
	}
}

func testSite(root string) site {
	return site{ID: siteQoder, Name: "Qoder", Web: root, OpenAPI: root, API: root, ClientID: siteGlobal.ClientID, RedirectURI: siteGlobal.RedirectURI}
}
