package mirasim

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/channel/modules"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func testProfileServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestFetchAccountProfileReadsAuthMe(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"numeric plan_exp": `{"email":"user@example.com","plan":"max","plan_exp":1820628314}`,
		"string plan_exp":  `{"email":"user@example.com","plan":"pro","plan_exp":"1820628314"}`,
		"no plan_exp":      `{"email":"user@example.com","plan":"free"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var seenAuth string
			admin := testProfileServer(t, func(w http.ResponseWriter, r *http.Request) {
				seenAuth = r.Header.Get("Authorization")
				if r.URL.Path != accountProfilePath || r.Method != http.MethodGet {
					http.NotFound(w, r)
					return
				}
				_, _ = io.WriteString(w, body)
			})
			client := NewClient(Storage{
				Type: credentialType, AccessToken: "access-secret", RefreshToken: "refresh-secret",
				DevicePrivateKey: testDevicePEM(t), AdminURL: admin.URL, RelayURL: defaultRelayURL,
			})
			profile, err := client.FetchAccountProfile(t.Context())
			if err != nil {
				t.Fatalf("FetchAccountProfile() error = %v", err)
			}
			if seenAuth != "Bearer access-secret" {
				t.Fatalf("authorization = %q", seenAuth)
			}
			if profile.Email != "user@example.com" {
				t.Fatalf("email = %q", profile.Email)
			}
			switch name {
			case "no plan_exp":
				if profile.PlanExpiryKnown || profile.PlanExpiresAt != nil {
					t.Fatalf("plan expiry = %#v", profile)
				}
			default:
				if !profile.PlanExpiryKnown || profile.PlanExpiresAt == nil || *profile.PlanExpiresAt != 1820628314 {
					t.Fatalf("plan expiry = %#v", profile)
				}
			}
		})
	}
}

func TestFetchAccountProfileRejectsUnavailableService(t *testing.T) {
	t.Parallel()
	admin := testProfileServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	client := NewClient(Storage{
		Type: credentialType, AccessToken: "access-secret", RefreshToken: "refresh-secret",
		DevicePrivateKey: testDevicePEM(t), AdminURL: admin.URL, RelayURL: defaultRelayURL,
	})
	if _, err := client.FetchAccountProfile(t.Context()); err == nil {
		t.Fatal("an unauthorized profile response was accepted")
	}
}

func TestValidateRemoteGatesOnRelayModels(t *testing.T) {
	t.Parallel()
	for name, status := range map[string]int{"accepted": http.StatusOK, "rejected": http.StatusUnauthorized} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			relay := testProfileServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/device/session":
					http.NotFound(w, r)
				case "/v1/models":
					if status != http.StatusOK {
						w.WriteHeader(status)
						_, _ = io.WriteString(w, `{"error":{"code":"token_missing"}}`)
						return
					}
					_, _ = io.WriteString(w, `{"data":[{"id":"gpt-5.6-luna"}]}`)
				default:
					http.NotFound(w, r)
				}
			})
			client := NewClient(Storage{
				Type: credentialType, AccessToken: "access-secret", RefreshToken: "refresh-secret",
				DevicePrivateKey: testDevicePEM(t), AdminURL: defaultAdminURL, RelayURL: relay.URL,
			})
			err := client.ValidateRemote(t.Context())
			if status == http.StatusOK && err != nil {
				t.Fatalf("ValidateRemote() error = %v", err)
			}
			if status != http.StatusOK {
				if err == nil {
					t.Fatal("a rejected credential passed remote validation")
				}
				if !strings.Contains(err.Error(), "401") {
					t.Fatalf("ValidateRemote() error = %v", err)
				}
			}
		})
	}
}

func TestCompleteAuthorizationRecordsProfileAndGatesOnRelay(t *testing.T) {
	t.Parallel()
	relayStatus := http.StatusOK
	relay := testProfileServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/device/session":
			http.NotFound(w, r)
		case "/v1/models":
			if relayStatus != http.StatusOK {
				w.WriteHeader(relayStatus)
				_, _ = io.WriteString(w, `{"error":{"code":"token_missing"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-5.6-luna"}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	admin := testProfileServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != accountProfilePath {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"email":"authoritative@example.com","plan":"max","plan_exp":1820628314}`)
	})
	state, err := json.Marshal(oauthDriverState{
		DevicePrivateKey: testDevicePEM(t), RelayURL: relay.URL, AdminURL: admin.URL, ClientVersion: defaultClientVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := subscriptionruntime.AuthorizationCompletion{
		ExpectedState: "state-one", ReturnedState: "state-one",
		AccessToken: "access-secret", RefreshToken: "refresh-secret", DriverState: state,
	}
	credential, err := (&driver{}).CompleteAuthorization(t.Context(), completion)
	if err != nil {
		t.Fatalf("CompleteAuthorization() error = %v", err)
	}
	stored, err := ParseCredentialJSON(credential.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Email != "authoritative@example.com" || stored.Plan != "max" {
		t.Fatalf("profile not recorded: email=%q plan=%q", stored.Email, stored.Plan)
	}
	if stored.PlanExpiresAt == nil || *stored.PlanExpiresAt != 1820628314 {
		t.Fatalf("plan expiry = %#v", stored.PlanExpiresAt)
	}
	if stored.ProfileCheckTime().IsZero() {
		t.Fatal("profile check time was not recorded")
	}
	relayStatus = http.StatusUnauthorized
	if _, err := (&driver{}).CompleteAuthorization(t.Context(), completion); err == nil {
		t.Fatal("a credential the relay rejects was persisted")
	}
}

func TestRecordProfileSupersedesTokenClaims(t *testing.T) {
	t.Parallel()
	storage := Storage{Email: "token@example.com", Plan: "free"}
	expiresAt := int64(1820628314)
	checkedAt := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	storage.RecordProfile("profile@example.com", "max", &expiresAt, checkedAt)
	if storage.Email != "profile@example.com" || storage.Plan != "max" {
		t.Fatalf("profile not applied: %#v", storage)
	}
	if storage.PlanExpiresAt == nil || *storage.PlanExpiresAt != expiresAt {
		t.Fatalf("plan expiry = %#v", storage.PlanExpiresAt)
	}
	if got := storage.ProfileCheckTime(); !got.Equal(checkedAt) {
		t.Fatalf("profile check time = %v, want %v", got, checkedAt)
	}
	// An empty answer must not erase what the token already told us.
	storage.RecordProfile("", "", nil, checkedAt.Add(time.Minute))
	if storage.Email != "profile@example.com" || storage.Plan != "max" {
		t.Fatalf("empty profile overwrote stored values: %#v", storage)
	}
	if got := storage.ProfileCheckTime(); !got.Equal(checkedAt.Add(time.Minute)) {
		t.Fatalf("profile check time = %v", got)
	}
}

var _ = modules.MirasimSubscriptionDriver
