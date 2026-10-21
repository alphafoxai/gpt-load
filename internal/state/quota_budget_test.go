package state

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"gpt-load/internal/platform/config"
	providerobservation "gpt-load/internal/subscription/providers/observation"
)

func budgetFixture(t *testing.T) (*CredentialRegistry, CredentialRef, time.Time, QuotaBudgetConfig) {
	t.Helper()
	r := NewCredentialRegistry()
	if err := r.ReplaceCredentials([]CredentialEntry{{ID: 1, GroupID: 10, Version: 1, IdentityGeneration: 1, Fingerprint: "budget-test", EncryptedValue: "cipher", Status: CredentialStatusActive}}); err != nil {
		t.Fatal(err)
	}
	ref, _ := r.CredentialRef(1)
	cfg := DefaultQuotaBudgetConfig()
	cfg.Enabled = true
	return r, ref, time.Unix(1_800_000_000, 0).UTC(), cfg
}

func budgetWindow(id string, used float64, at, reset time.Time) providerobservation.QuotaWindow {
	a, b := at.UnixMilli(), reset.UnixMilli()
	return providerobservation.QuotaWindow{ID: id, Scope: "account", Utilization: &used, ObservedAtMS: &a, ResetAtMS: &b, State: "available"}
}

func TestQuotaBudgetThresholdEquality(t *testing.T) {
	for _, percent := range []int{30, 50, 80, 100} {
		for _, id := range []string{"five_hour", "seven_day"} {
			for _, equality := range []bool{false, true} {
				r, ref, now, cfg := budgetFixture(t)
				cfg.FiveHourPercent, cfg.SevenDayPercent = percent, percent
				used := float64(percent) / 100
				if !equality {
					used = math.Nextafter(used, 0)
				}
				windows := []providerobservation.QuotaWindow{budgetWindow("five_hour", 0, now, now.Add(time.Hour)), budgetWindow("seven_day", 0, now, now.Add(24*time.Hour))}
				if id == "five_hour" {
					windows[0].Utilization = &used
				} else {
					windows[1].Utilization = &used
				}
				r.ApplyQuotaBudgetWindows(ref, windows)
				decision := r.CheckQuotaBudget(ref, cfg, now)
				if decision.Allowed == equality || equality && (decision.Reason != "quota_budget_exceeded" || decision.RetryAfter <= 0) {
					t.Fatalf("%s %d equality=%t: %+v", id, percent, equality, decision)
				}
			}
		}
	}
}

func TestQuotaBudgetUnavailableBoundaries(t *testing.T) {
	for _, kind := range []string{"missing", "stale", "reset", "future", "before-window", "invalid-number", "missing-time"} {
		t.Run(kind, func(t *testing.T) {
			r, ref, now, cfg := budgetFixture(t)
			five := budgetWindow("five_hour", .2, now, now.Add(time.Hour))
			switch kind {
			case "missing":
			case "stale":
				five = budgetWindow("five_hour", .2, now.Add(-300*time.Second), now.Add(time.Hour))
			case "reset":
				five = budgetWindow("five_hour", .2, now, now)
			case "future":
				five = budgetWindow("five_hour", .2, now.Add(time.Millisecond), now.Add(time.Hour))
			case "before-window":
				five = budgetWindow("five_hour", .2, now, now.Add(6*time.Hour))
			case "invalid-number":
				invalid := math.NaN()
				five.Utilization = &invalid
			case "missing-time":
				five.ObservedAtMS = nil
			}
			windows := []providerobservation.QuotaWindow{budgetWindow("seven_day", .2, now, now.Add(24*time.Hour))}
			if kind != "missing" {
				windows = append(windows, five)
			}
			r.ApplyQuotaBudgetWindows(ref, windows)
			if got := r.CheckQuotaBudget(ref, cfg, now); got.Allowed || got.Reason != "quota_observation_unavailable" {
				t.Fatalf("decision = %+v", got)
			}
			cfg.Enabled = false
			if !r.CheckQuotaBudget(ref, cfg, now).Allowed {
				t.Fatal("disabled budget rejected request")
			}
		})
	}
}

func TestQuotaBudgetWindowFreshnessAndIdentity(t *testing.T) {
	r, ref, now, cfg := budgetFixture(t)
	r.ApplyQuotaBudgetWindows(ref, []providerobservation.QuotaWindow{budgetWindow("five_hour", .6, now, now.Add(time.Hour)), budgetWindow("seven_day", .1, now, now.Add(24*time.Hour))})
	r.ApplyQuotaBudgetWindows(ref, []providerobservation.QuotaWindow{budgetWindow("five_hour", .1, now.Add(-time.Second), now.Add(time.Hour))})
	if got := r.CheckQuotaBudget(ref, cfg, now); got.Reason != "quota_budget_exceeded" {
		t.Fatalf("older sample changed budget: %+v", got)
	}
	later := now.Add(300 * time.Second)
	r.ApplyQuotaBudgetWindows(ref, []providerobservation.QuotaWindow{budgetWindow("seven_day", .1, later, later.Add(24*time.Hour))})
	if got := r.CheckQuotaBudget(ref, cfg, later); got.Reason != "quota_observation_unavailable" {
		t.Fatalf("weekly sample refreshed five-hour evidence: %+v", got)
	}
	entries, err := r.SnapshotGroupCredentialEntriesExact(10, []uint{1})
	if err != nil {
		t.Fatal(err)
	}
	entries[0].IdentityGeneration++
	entries[0].Fingerprint = "other-account"
	if _, err := r.ReconcileGroup(10, entries); err != nil {
		t.Fatal(err)
	}
	if r.ApplyQuotaBudgetWindows(ref, []providerobservation.QuotaWindow{budgetWindow("five_hour", .1, later, later.Add(time.Hour))}) {
		t.Fatal("old account identity accepted")
	}
	current, _ := r.CredentialRef(1)
	if got := r.CheckQuotaBudget(current, cfg, later); got.Reason != "quota_observation_unavailable" {
		t.Fatalf("old identity survived cutover: %+v", got)
	}
}

func TestQuotaBudgetPreservesEvidenceAcrossSecretRotationAndConfigPublication(t *testing.T) {
	r, ref, now, cfg := budgetFixture(t)
	windows := []providerobservation.QuotaWindow{budgetWindow("five_hour", .1, now, now.Add(time.Hour)), budgetWindow("seven_day", .1, now, now.Add(24*time.Hour))}
	r.ApplyQuotaBudgetWindows(ref, windows)
	if !r.ReplaceCredentialSecretIfMatch(1, 1, 2, "rotated-token", "next-cipher") {
		t.Fatal("rotate secret")
	}
	entries, err := r.SnapshotGroupCredentialEntriesExact(10, []uint{1})
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Name = "renamed"
	if err := r.ReplaceCredentials(entries); err != nil {
		t.Fatal(err)
	}
	current, _ := r.CredentialRef(1)
	if got := r.CheckQuotaBudget(current, cfg, now); !got.Allowed {
		t.Fatalf("same-account sample lost: %+v", got)
	}
	r.InvalidateQuotaBudget(1, now.Add(time.Second))
	if got := r.CheckQuotaBudget(current, cfg, now.Add(time.Second)); got.Allowed {
		t.Fatal("reset invalidation retained evidence")
	}
	r.ApplyQuotaBudgetWindows(current, windows)
	if got := r.CheckQuotaBudget(current, cfg, now.Add(time.Second)); got.Allowed {
		t.Fatal("pre-reset evidence restored budget")
	}
}

func TestQuotaBudgetSuspensionIsolation(t *testing.T) {
	r, ref, now, cfg := budgetFixture(t)
	if err := r.SetCredentialStatus(1, CredentialStatusSuspended); err != nil {
		t.Fatal(err)
	}
	if r.RestoreRuntimeState(1) || r.Recover(1) {
		t.Fatal("generic restore accepted suspension")
	}
	if err := r.SetCredentialStatus(1, CredentialStatusActive); err == nil {
		t.Fatal("direct enable cleared suspension")
	}
	for _, status := range []CredentialStatus{CredentialStatusActive, CredentialStatusDisabled} {
		if err := r.UpdateGroupCredentialStatuses(10, []uint{1}, status); err != nil {
			t.Fatal(err)
		}
		if r.Snapshot()[0].Status != CredentialStatusSuspended {
			t.Fatal("generic batch cleared suspension")
		}
	}
	if r.QuotaBudgetRefreshDue(ref, cfg, now) || r.ApplyQuotaBudgetWindows(ref, []providerobservation.QuotaWindow{budgetWindow("five_hour", .1, now, now.Add(time.Hour))}) {
		t.Fatal("suspended credential accepted quota work")
	}
	if r.Snapshot()[0].RuntimeState(now) != CredentialRuntimeSuspended || len(r.CollectCredentialCandidates([]uint{10}, nil, now)) != 0 {
		t.Fatal("suspension is schedulable")
	}
}

func TestCapacitySettingsStrictValidation(t *testing.T) {
	valid := map[string]any{"enabled": true, "five_hour_percent": json.Number("50"), "seven_day_percent": json.Number("80"), "observation_max_age_seconds": json.Number("300")}
	for _, key := range []string{SettingCredentialConcurrencyLimit, SettingCredentialRPMLimit} {
		for _, raw := range []any{json.Number("-1"), json.Number("0.5"), json.Number("9007199254740992"), nil, "2", true} {
			if err := ValidateRuntimeSetting(key, raw); err == nil {
				t.Fatalf("%s accepted %#v", key, raw)
			}
		}
		if err := ValidateRuntimeSetting(key, json.Number("9007199254740991")); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		key   string
		value any
	}{{"enabled", nil}, {"enabled", "true"}, {"five_hour_percent", 0}, {"five_hour_percent", 101}, {"seven_day_percent", 80.5}, {"observation_max_age_seconds", 0}, {"observation_max_age_seconds", 86401}, {"extra", true}} {
		candidate := make(map[string]any, len(valid)+1)
		for key, value := range valid {
			candidate[key] = value
		}
		candidate[test.key] = test.value
		if err := ValidateRuntimeSetting(SettingQuotaBudget, candidate); err == nil {
			t.Fatalf("accepted invalid budget %s=%v", test.key, test.value)
		}
	}
	settings, err := ResolveRuntimeSettings(config.Settings{SettingCredentialConcurrencyLimit: json.Number("2"), SettingCredentialRPMLimit: json.Number("20"), SettingQuotaBudget: valid})
	if err != nil || settings.CredentialConcurrencyLimit != 2 || settings.CredentialRPMLimit != 20 || !settings.QuotaBudget.Enabled {
		t.Fatalf("resolve = %+v, %v", settings, err)
	}
	if _, err := ResolveGroupRuntimeSettings(settings, config.Settings{SettingQuotaBudget: valid}); err == nil {
		t.Fatal("group accepted global budget override")
	}
}
