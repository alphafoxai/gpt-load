package subscription

import (
	"encoding/json"
	"testing"
	"time"

	"gpt-load/internal/state"
	providerobservation "gpt-load/internal/subscription/providers/observation"
)

func TestPassiveQuotaWeeklySampleDoesNotRefreshFiveHour(t *testing.T) {
	storedAt := int64(3000)
	raw := []byte(`{"quota_windows":[{"id":"five_hour","scope":"account","utilization":0.2,"state":"available","observed_at_ms":1000,"reset_at_ms":1000000},{"id":"seven_day","scope":"account","utilization":0.2,"state":"available","observed_at_ms":3000,"reset_at_ms":1000000}]}`)
	used := .3
	merged, at, err := mergePassiveQuotaSamples(raw, &storedAt, PassiveQuotaObservation{ObservedAtMS: 4000, Windows: []providerobservation.QuotaWindow{{ID: "seven_day", Utilization: &used}}})
	if err != nil || at != 4000 {
		t.Fatalf("merge = %d, %v", at, err)
	}
	var snapshot providerobservation.Snapshot
	if err := json.Unmarshal(merged.Encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	if *snapshot.QuotaWindows[0].ObservedAtMS != 1000 || *snapshot.QuotaWindows[1].ObservedAtMS != 4000 {
		t.Fatalf("unrelated freshness advanced: %+v", snapshot.QuotaWindows)
	}
	// An independent five-hour sample older than the weekly sample is still newer for its own window.
	merged, at, err = mergePassiveQuotaSamples(merged.Encoded, &at, PassiveQuotaObservation{ObservedAtMS: 2000, Windows: []providerobservation.QuotaWindow{{ID: "five_hour", Utilization: &used}}})
	if err != nil || at != 4000 {
		t.Fatalf("independent merge = %d, %v", at, err)
	}
	if err := json.Unmarshal(merged.Encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	if *snapshot.QuotaWindows[0].ObservedAtMS != 2000 || *snapshot.QuotaWindows[1].ObservedAtMS != 4000 {
		t.Fatal("window-local older ordering rejected valid sample")
	}
	olderUsage := .1
	merged, _, err = mergePassiveQuotaSamples(merged.Encoded, &at, PassiveQuotaObservation{ObservedAtMS: 1500, Windows: []providerobservation.QuotaWindow{{ID: "five_hour", Utilization: &olderUsage}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(merged.Encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	if *snapshot.QuotaWindows[0].Utilization != .3 {
		t.Fatal("older five-hour sample overwrote newer one")
	}
}

func TestPassiveQuotaPublishesBudgetBeforePersistence(t *testing.T) {
	manager, _, registry, _, credential := newCredentialManagerFixture(t, credentialJSON("access", "refresh", time.Now().Add(time.Hour)))
	ref, _ := registry.CredentialRef(credential.ID)
	now := time.Unix(1_800_000_000, 0)
	reset := now.Add(time.Hour).UnixMilli()
	used := .5
	manager.RecordPassiveQuotaObservation(credential.ID, ref.IdentityGeneration, now.UnixMilli(), []providerobservation.QuotaWindow{
		{ID: "five_hour", Utilization: &used, ResetAtMS: &reset},
		{ID: "seven_day", Utilization: &used, ResetAtMS: &reset},
	})
	cfg := state.DefaultQuotaBudgetConfig()
	cfg.Enabled = true
	if got := registry.CheckQuotaBudget(ref, cfg, now); got.Reason != "quota_budget_exceeded" {
		t.Fatalf("passive observation not immediate: %+v", got)
	}
	used = .1
	manager.RecordPassiveQuotaObservation(credential.ID, ref.IdentityGeneration+1, now.Add(time.Second).UnixMilli(), []providerobservation.QuotaWindow{{ID: "five_hour", Utilization: &used, ResetAtMS: &reset}})
	if got := registry.CheckQuotaBudget(ref, cfg, now); got.Reason != "quota_budget_exceeded" {
		t.Fatal("other-identity sample changed budget")
	}
}
