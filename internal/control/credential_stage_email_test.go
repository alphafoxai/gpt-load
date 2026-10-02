package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"gpt-load/internal/channel"
	app_errors "gpt-load/internal/platform/errors"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func mirasimStyleAuthorization(now time.Time) subscriptionruntime.Authorization {
	return subscriptionruntime.Authorization{
		URL: "https://auth.example.test/auth/oauth/github/login", State: "state",
		DriverState: []byte(`{"admin_url":"https://auth.example.test"}`),
		ExpiresAt:   now.Add(30 * time.Minute), EmailLogin: true,
		Providers: []subscriptionruntime.AuthorizationProvider{
			{ID: "github", Label: "GitHub", URL: "https://auth.example.test/github"},
			{ID: "google", Label: "Google", URL: "https://auth.example.test/google"},
			{ID: "bad", Label: "Bad", URL: "http://auth.example.test/bad"},
		},
	}
}

func TestBeginAuthorizationPublishesEveryLoginProvider(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	now := time.UnixMilli(1_800_000_000_000).UTC()
	fixture.service.now = func() time.Time { return now }
	fixture.service.beginSubscriptionAuthorization = func(channel.ID) (subscriptionruntime.Authorization, error) {
		return mirasimStyleAuthorization(now), nil
	}
	result, err := fixture.service.BeginCredentialAuthorization(t.Context(), channel.Codex)
	if err != nil {
		t.Fatal(err)
	}
	if !result.EmailLogin || len(result.LoginProviders) != 2 || result.LoginProviders[1].ID != "google" {
		t.Fatalf("providers = %#v email=%v", result.LoginProviders, result.EmailLogin)
	}
	loaded, err := fixture.service.GetCredentialStage(t.Context(), result.StageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.LoginProviders) != 2 || !loaded.EmailLogin || loaded.AuthorizationURL == "" {
		t.Fatalf("reloaded = %#v", loaded)
	}
}

func TestEmailCodeSendKeepsTheLimitWhenTheServiceRejects(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	now := time.UnixMilli(1_800_000_000_000).UTC()
	fixture.service.now = func() time.Time { return now }
	fixture.service.beginSubscriptionAuthorization = func(channel.ID) (subscriptionruntime.Authorization, error) {
		return mirasimStyleAuthorization(now), nil
	}
	stage, err := fixture.service.BeginCredentialAuthorization(t.Context(), channel.Codex)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.sendSubscriptionEmailCode = func(context.Context, channel.ID, []byte, string) error {
		return subscriptionruntime.ErrEmailLoginRejected
	}
	const address = "person@example.com"
	if err := fixture.service.SendCredentialEmailCode(t.Context(), stage.StageID, address); !errors.Is(err, app_errors.ErrEmailLoginRejected) {
		t.Fatalf("first send = %v", err)
	}
	now = now.Add(emailCodeSendInterval)
	if err := fixture.service.SendCredentialEmailCode(t.Context(), stage.StageID, address); !errors.Is(err, app_errors.ErrEmailLoginRejected) {
		t.Fatalf("second send = %v", err)
	}
	now = now.Add(emailCodeSendInterval)
	if err := fixture.service.SendCredentialEmailCode(t.Context(), stage.StageID, address); !errors.Is(err, app_errors.ErrEmailLoginRejected) {
		t.Fatalf("third send = %v", err)
	}
	now = now.Add(emailCodeSendInterval)
	if err := fixture.service.SendCredentialEmailCode(t.Context(), stage.StageID, address); !errors.Is(err, app_errors.ErrEmailLoginLimited) {
		t.Fatalf("fourth send = %v", err)
	}
}
