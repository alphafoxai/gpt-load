package control

import (
	"context"
	"testing"

	"gpt-load/internal/channel"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func TestQuotaBudgetRefreshExcludesDisabledGroup(t *testing.T) {
	fixture := newServiceFixture(t)
	groupID, credential := createBudgetAccount(t, fixture)
	enableBudget(t, fixture, true)
	input := mustBuildCompileInput(t, fixture.db)
	for index := range input.Groups {
		if input.Groups[index].ID == groupID {
			input.Groups[index].Enabled = false
		}
	}
	if _, err := fixture.manager.Publish(input); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
		calls++
		return subscriptionruntime.Observation{}, nil
	}
	fixture.service.refreshQuotaBudgetBatch(t.Context(), 0)
	if calls != 0 || fixture.service.quotaBudgetRefreshEligible(groupID, credential.ID) {
		t.Fatal("disabled group contacted provider")
	}
}
