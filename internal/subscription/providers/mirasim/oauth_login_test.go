package mirasim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBeginBrowserLoginListsEveryDiscoveredProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/oauth/providers" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"providers":["google","github","google"]}`))
	}))
	defer server.Close()

	login, err := BeginBrowserLogin(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(login.Providers) != 2 {
		t.Fatalf("providers = %#v", login.Providers)
	}
	if login.Providers[0].ID != "google" || login.Providers[0].Label != "Google" || !strings.Contains(login.Providers[0].URL, "/auth/oauth/google/login") {
		t.Fatalf("google provider = %#v", login.Providers[0])
	}
	if login.Providers[1].ID != "github" || !strings.Contains(login.Providers[1].URL, "/auth/oauth/github/login") {
		t.Fatalf("github provider = %#v", login.Providers[1])
	}
	if login.URL != login.Providers[1].URL {
		t.Fatalf("default URL = %s, want github %s", login.URL, login.Providers[1].URL)
	}
	if !strings.Contains(login.Providers[0].URL, "state=") || !strings.Contains(login.Providers[1].URL, login.State) {
		t.Fatal("provider links do not carry the login state")
	}
}

func TestEmailCodeSignInExchangesAMailedCode(t *testing.T) {
	var requested, verified map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/auth/code":
			requested = body
			w.WriteHeader(http.StatusNoContent)
		case "/auth/verify":
			verified = body
			_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh"}`))
		default:
			t.Errorf("path = %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if err := requestEmailCode(context.Background(), server.URL, "person@example.com"); err != nil {
		t.Fatal(err)
	}
	access, refresh, err := verifyEmailCode(context.Background(), server.URL, "person@example.com", "123456")
	if err != nil {
		t.Fatal(err)
	}
	if requested["email"] != "person@example.com" || verified["code"] != "123456" || access != "access" || refresh != "refresh" {
		t.Fatalf("requested=%v verified=%v tokens=%s %s", requested, verified, access, refresh)
	}
}

func TestEmailCodeSignInDoesNotReturnUpstreamDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"secret-code-123456"}`))
	}))
	defer server.Close()
	err := requestEmailCode(context.Background(), server.URL, "person@example.com")
	if err == nil || strings.Contains(err.Error(), "secret-code") {
		t.Fatalf("error = %v", err)
	}
}
