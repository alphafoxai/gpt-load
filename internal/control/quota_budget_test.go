package control

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func createBudgetAccount(t *testing.T, fixture serviceFixture) (uint, models.Credential) {
	t.Helper()
	stage, err := fixture.service.ImportCredentialStage(t.Context(), channel.Claude, []byte(`{"type":"claude","access_token":"test-access","refresh_token":"test-refresh","account_uuid":"budget-account","expired":"2030-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{Name: stringPointer("Budget account"), ChannelID: channel.Claude, ConnectionType: models.ConnectionTypeSubscription, Models: optionalGroupModels{Set: true, Values: []GroupModel{{ID: "claude-sonnet-4-6"}}}, StagedCredentialIDs: []string{stage.StageID}})
	if err != nil {
		t.Fatal(err)
	}
	var credential models.Credential
	if err := fixture.db.Take(&credential, "group_id = ?", created.GroupID).Error; err != nil {
		t.Fatal(err)
	}
	return created.GroupID, credential
}

func enableBudget(t *testing.T, fixture serviceFixture, enabled bool) {
	t.Helper()
	raw := json.RawMessage(fmt.Sprintf(`{"enabled":%t,"five_hour_percent":50,"seven_day_percent":80,"observation_max_age_seconds":300}`, enabled))
	if _, err := fixture.service.UpdateSettings(t.Context(), SettingsUpdateRequest{Settings: map[string]json.RawMessage{"quota_budget": raw}}); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaBudgetRefreshDisabledSuspendedAndAuthNotReady(t *testing.T) {
	fixture := newServiceFixture(t)
	groupID, credential := createBudgetAccount(t, fixture)
	now := time.Unix(1_800_000_000, 0).UTC()
	fixture.service.now = func() time.Time { return now }
	calls := 0
	fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
		calls++
		return subscriptionruntime.Observation{Payload: json.RawMessage(fmt.Sprintf(`{"quota_windows":[{"id":"five_hour","scope":"account","utilization":0.1,"reset_at_ms":%d},{"id":"seven_day","scope":"account","utilization":0.1,"reset_at_ms":%d}]}`, now.Add(time.Hour).UnixMilli(), now.Add(24*time.Hour).UnixMilli()))}, nil
	}
	fixture.service.refreshQuotaBudgetBatch(t.Context(), 0)
	if calls != 0 {
		t.Fatal("disabled budget contacted provider")
	}
	enableBudget(t, fixture, true)
	if err := fixture.registry.SetCredentialStatus(credential.ID, state.CredentialStatusSuspended); err != nil {
		t.Fatal(err)
	}
	fixture.service.refreshQuotaBudgetBatch(t.Context(), 0)
	if calls != 0 || fixture.service.quotaBudgetRefreshEligible(groupID, credential.ID) {
		t.Fatal("suspended account was refreshed")
	}
	if err := fixture.registry.SetCredentialStatus(credential.ID, state.CredentialStatusDisabled); err != nil {
		t.Fatal(err)
	}
	if err := fixture.registry.SetCredentialStatus(credential.ID, state.CredentialStatusActive); err != nil {
		t.Fatal(err)
	}
	fixture.registry.SetCredentialAuthState(credential.ID, state.CredentialAuthStateReauthorizationRequired)
	fixture.service.refreshQuotaBudgetBatch(t.Context(), 0)
	if calls != 0 {
		t.Fatal("auth-not-ready account was refreshed")
	}
	fixture.registry.SetCredentialAuthState(credential.ID, state.CredentialAuthStateReady)
	fixture.service.refreshQuotaBudgetBatch(t.Context(), 0)
	if calls != 1 {
		t.Fatalf("ready account observations = %d", calls)
	}
	fixture.service.refreshQuotaBudgetBatch(t.Context(), 0)
	if calls != 1 {
		t.Fatal("fresh account redundantly refreshed")
	}
	ref, _ := fixture.registry.CredentialRef(credential.ID)
	if got := fixture.registry.CheckQuotaBudget(ref, fixture.manager.Current().Settings.QuotaBudget, now); !got.Allowed {
		t.Fatalf("fresh observation not published: %+v", got)
	}
}

func TestQuotaBudgetHydratesOnlyWindowTimestampedMatchingIdentity(t *testing.T) {
	for _, kind := range []string{"trusted", "legacy", "other-identity", "stale"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newServiceFixture(t)
			_, credential := createBudgetAccount(t, fixture)
			now := time.Unix(1_800_000_000, 0).UTC()
			at := now.UnixMilli()
			observed := at
			if kind == "stale" {
				observed -= 300_000
			}
			windowTime := fmt.Sprintf(`,"observed_at_ms":%d`, observed)
			if kind == "legacy" {
				windowTime = ""
			}
			identity := credential.IdentityFingerprint
			if kind == "other-identity" {
				identity = "different-account"
			}
			row := models.CredentialObservation{CredentialID: credential.ID, IdentityFingerprint: identity, SchemaVersion: 1, ObservationVersion: 1, State: models.CredentialObservationFresh, ObservedAtMS: &at, UpdatedAtMS: at,
				SnapshotJSON: models.JSON(fmt.Sprintf(`{"quota_windows":[{"id":"five_hour","scope":"account","utilization":0.1,"reset_at_ms":%d%s},{"id":"seven_day","scope":"account","utilization":0.1,"reset_at_ms":%d%s}]}`, now.Add(time.Hour).UnixMilli(), windowTime, now.Add(24*time.Hour).UnixMilli(), windowTime))}
			if err := fixture.db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			enableBudget(t, fixture, true)
			if err := fixture.service.restoreCredentialQuotaObservations(t.Context()); err != nil {
				t.Fatal(err)
			}
			ref, _ := fixture.registry.CredentialRef(credential.ID)
			got := fixture.registry.CheckQuotaBudget(ref, fixture.manager.Current().Settings.QuotaBudget, now)
			if got.Allowed != (kind == "trusted") {
				t.Fatalf("hydration %s = %+v", kind, got)
			}
		})
	}
}

func TestPreserveNewerQuotaWindowSample(t *testing.T) {
	oldAt, newAt := int64(1000), int64(2000)
	oldUsage, newUsage := .1, .9
	previous := []ObservationQuotaWindow{{ID: "five_hour", Scope: "account", ObservedAtMS: &newAt, Utilization: &newUsage}}
	incoming := []ObservationQuotaWindow{{ID: "five_hour", Scope: "account", ObservedAtMS: &oldAt, Utilization: &oldUsage}, {ID: "seven_day", Scope: "account", ObservedAtMS: &newAt, Utilization: &oldUsage}}
	got := preserveNewerQuotaWindows(incoming, previous)
	if *got[0].Utilization != .9 || *got[1].Utilization != .1 || *got[0].ObservedAtMS != newAt {
		t.Fatalf("newer window overwritten: %+v", got)
	}
}
