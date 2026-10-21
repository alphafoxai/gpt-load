package subscription

import (
	"gpt-load/internal/state"
	providerobservation "gpt-load/internal/subscription/providers/observation"
)

func (manager *CredentialManager) publishPassiveQuotaBudget(ref state.CredentialRef, observedAtMS int64, windows []providerobservation.QuotaWindow) {
	if observedAtMS <= 0 {
		return
	}
	copied := cloneQuotaWindows(windows)
	for index := range copied {
		copied[index].ObservedAtMS = cloneInt64(&observedAtMS)
	}
	manager.registry.ApplyQuotaBudgetWindows(ref, copied)
}
