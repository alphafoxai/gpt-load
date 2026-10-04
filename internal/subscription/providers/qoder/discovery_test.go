package qoder

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func TestDiscoverModelsUsesAccountListing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/algo/api/v2/model/list" {
			http.NotFound(w, r)
			return
		}
		if !stringsHasPrefix(r.Header.Get("Authorization"), "Bearer COSY.") {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"chat":[
			{"key":"auto","enable":true},
			{"key":"Qwen3.8-Flash","enable":true},
			{"key":"disabled-one","enable":false},
			{"key":"Qwen3.8-Max","enable":true}
		]}`))
	}))
	defer server.Close()
	cred := Credential{
		Site: siteQoder, UID: "user-1", AccessToken: "jt-1", RefreshToken: "jr-1",
		ExpiresAt: time.Now().Add(time.Hour), MachineID: "machine-1",
	}
	runtimeCred, err := runtimeCredential(cred)
	if err != nil {
		t.Fatal(err)
	}
	client := &apiClient{http: server.Client(), sites: []site{{
		ID: siteQoder, API: server.URL,
	}}}
	ctx := WithClient(WithHTTPClient(context.Background(), server.Client()), client)
	names, err := modelDiscovery{}.DiscoverModels(ctx, runtimeCred, subscriptionruntime.Target{})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "Qwen3.8-Flash" || names[1] != "Qwen3.8-Max" {
		t.Fatalf("models = %#v", names)
	}
}

func TestDiscoverModelsMapsUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	cred := Credential{
		Site: siteQoder, UID: "user-1", AccessToken: "jt-1", RefreshToken: "jr-1",
		ExpiresAt: time.Now().Add(time.Hour), MachineID: "machine-1",
	}
	runtimeCred, err := runtimeCredential(cred)
	if err != nil {
		t.Fatal(err)
	}
	client := &apiClient{http: server.Client(), sites: []site{{ID: siteQoder, API: server.URL}}}
	ctx := WithClient(WithHTTPClient(context.Background(), server.Client()), client)
	_, err = modelDiscovery{}.DiscoverModels(ctx, runtimeCred, subscriptionruntime.Target{})
	var upstream *subscriptionruntime.UpstreamHTTPError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %v", err)
	}
}

func stringsHasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}
