package mirasim

import (
	"encoding/json"
	"testing"

	providerobservation "gpt-load/internal/subscription/providers/observation"
)

func boolPointer(value bool) *bool { return &value }

func TestNormalizeObservationUsesSharedQuotaStates(t *testing.T) {
	paid := false
	payload, err := NormalizeObservation("person@example.com", "max", Limits{
		Paid: &paid,
		Windows: []LimitWindow{
			{Name: "5h", Used: 50, Budget: 100},
			{Name: "7d", Used: 100, Budget: 100, ModelScoped: true},
		},
	})
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
	if len(snapshot.QuotaWindows) != 2 {
		t.Fatalf("windows = %#v", snapshot.QuotaWindows)
	}
	if snapshot.QuotaWindows[0].State != "available" || snapshot.QuotaWindows[0].Utilization == nil || *snapshot.QuotaWindows[0].Utilization != 0.5 {
		t.Fatalf("open window = %#v", snapshot.QuotaWindows[0])
	}
	if snapshot.QuotaWindows[1].State != "exhausted" || snapshot.QuotaWindows[1].Utilization == nil || *snapshot.QuotaWindows[1].Utilization != 1 {
		t.Fatalf("spent window = %#v", snapshot.QuotaWindows[1])
	}
}

func TestPlanSummaryPrefersTheReportedName(t *testing.T) {
	summary := planSummary("max", boolPointer(false))
	if summary.Name != "max" || summary.Level != providerobservation.PlanLevelStandard {
		t.Fatalf("planSummary() = %#v", summary)
	}
	free := planSummary("", boolPointer(false))
	if free.Name != "free" || free.Level != providerobservation.PlanLevelFree {
		t.Fatalf("unnamed unpaid plan = %#v", free)
	}
}
