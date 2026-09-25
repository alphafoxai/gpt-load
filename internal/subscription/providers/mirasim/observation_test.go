package mirasim

import (
	"encoding/json"
	"testing"

	providerobservation "gpt-load/internal/subscription/providers/observation"
)

func boolPointer(value bool) *bool { return &value }

func TestPlanSummaryPrefersTheReportedNameOverThePaidFlag(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		plan      string
		paid      *bool
		wantName  string
		wantLevel providerobservation.PlanLevel
	}{
		// The relay reports paid=false for this account while the account
		// service reports the max plan; the name wins.
		"named plan ignores paid flag": {plan: "max", paid: boolPointer(false), wantName: "max", wantLevel: providerobservation.PlanLevelStandard},
		"named plan with paid":         {plan: "max", paid: boolPointer(true), wantName: "max", wantLevel: providerobservation.PlanLevelStandard},
		"free plan by name":            {plan: "free", paid: boolPointer(true), wantName: "free", wantLevel: providerobservation.PlanLevelFree},
		"free plan without a flag":     {plan: "Free Tier", wantName: "Free Tier", wantLevel: providerobservation.PlanLevelFree},
		"no name and unpaid":           {paid: boolPointer(false), wantName: "free", wantLevel: providerobservation.PlanLevelFree},
		"no name and paid":             {paid: boolPointer(true), wantName: "paid", wantLevel: providerobservation.PlanLevelStandard},
		"no name and no flag":          {wantName: "paid", wantLevel: providerobservation.PlanLevelStandard},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			summary := planSummary(testCase.plan, testCase.paid)
			if summary.Name != testCase.wantName || summary.Level != testCase.wantLevel {
				t.Fatalf("planSummary(%q, %v) = %#v, want name=%q level=%q", testCase.plan, testCase.paid, summary, testCase.wantName, testCase.wantLevel)
			}
		})
	}
}

func TestNormalizeObservationReportsLiveLimitsShape(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"subject":"usr_x","suspended":false,"unmetered":false,"degraded":false,"paid":false,"windows":[
		{"name":"5h","used":18.96992,"budget":143528,"reset_at":1790341485},
		{"name":"7d","used":19.46942,"budget":512600,"reset_at":1790927857},
		{"name":"7d_claude","used":0,"budget":512600,"reset_at":1790927857,"model_scoped":true},
		{"name":"7d_fable","used":0,"budget":102520,"reset_at":1790927857,"model_scoped":true}]}`)
	limits, err := ParseLimits(raw)
	if err != nil {
		t.Fatalf("ParseLimits() error = %v", err)
	}
	payload, err := NormalizeObservation("[EMAIL]", "max", limits)
	if err != nil {
		t.Fatalf("NormalizeObservation() error = %v", err)
	}
	var snapshot providerobservation.Snapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.Plan.Name != "max" || snapshot.Plan.Level != providerobservation.PlanLevelStandard {
		t.Fatalf("plan = %#v", snapshot.Plan)
	}
	if snapshot.Account == nil || snapshot.Account.Email != "[EMAIL]" {
		t.Fatalf("account = %#v", snapshot.Account)
	}
	if len(snapshot.QuotaWindows) != 4 {
		t.Fatalf("windows = %d, want 4", len(snapshot.QuotaWindows))
	}
	scopes := map[string]int{}
	for _, window := range snapshot.QuotaWindows {
		scopes[window.Scope]++
	}
	if scopes[quotaScopeAccount] != 2 || scopes[quotaScopeSurface] != 2 {
		t.Fatalf("scopes = %#v", scopes)
	}
	if !snapshot.QuotaWindows[0].IsPrimary {
		t.Fatal("first account-scoped window is not primary")
	}
}
