package scheduler

import (
	"errors"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
	providerobservation "gpt-load/internal/subscription/providers/observation"
)

func TestQuotaBudgetFiltersBeforePreferredCredential(t *testing.T) {
	registry := state.NewCredentialRegistry()
	entries := []state.CredentialEntry{
		{ID: 1, GroupID: 1, Version: 1, IdentityGeneration: 1, Fingerprint: "one", EncryptedValue: "cipher-one", Status: state.CredentialStatusActive},
		{ID: 2, GroupID: 1, Version: 1, IdentityGeneration: 2, Fingerprint: "two", EncryptedValue: "cipher-two", Status: state.CredentialStatusActive},
	}
	if err := registry.ReplaceCredentials(entries); err != nil {
		t.Fatal(err)
	}
	snapshot, err := state.Compile(state.CompileInput{ChannelRegistry: channel.NewRegistry(), Groups: []state.GroupConfig{{ID: 1, Name: "subscription", Enabled: true, ChannelID: channel.Claude, ConnectionType: "subscription", Models: []state.ModelConfig{{ID: "claude-sonnet-4-6"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Settings.QuotaBudget = state.DefaultQuotaBudgetConfig()
	snapshot.Settings.QuotaBudget.Enabled = true
	now := time.Unix(1_800_000_000, 0)
	at, reset := now.UnixMilli(), now.Add(time.Hour).UnixMilli()
	for _, id := range []uint{1, 2} {
		used := .1
		if id == 1 {
			used = .5
		}
		ref, _ := registry.CredentialRef(id)
		registry.ApplyQuotaBudgetWindows(ref, []providerobservation.QuotaWindow{
			{ID: "five_hour", Scope: "account", Utilization: &used, ObservedAtMS: &at, ResetAtMS: &reset},
			{ID: "seven_day", Scope: "account", Utilization: &used, ObservedAtMS: &at, ResetAtMS: &reset},
		})
	}
	iterator := newWithClock(snapshot, registry, Query{ClientProtocol: protocol.Anthropic, Operation: execution.OperationChatCompletion, ExternalModel: modelPointer("claude-sonnet-4-6"), PreferredCredentialID: 1}, func() time.Time { return now })
	selected, err := iterator.Next()
	if err != nil || selected.CredentialID != 2 {
		t.Fatalf("sticky bypassed budget: %+v, %v", selected, err)
	}
	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("exhaustion = %v", err)
	}
	if decision := iterator.QuotaBudgetDecision(); decision.Reason != "quota_budget_exceeded" {
		t.Fatalf("lost exhaustion reason: %+v", decision)
	}
	iterator = newWithClock(snapshot, registry, Query{ClientProtocol: protocol.Anthropic, Operation: execution.OperationChatCompletion, ExternalModel: modelPointer("claude-sonnet-4-6"), PreferredCredentialID: 1}, func() time.Time { return now.Add(300 * time.Second) })
	if _, err := iterator.Next(); !errors.Is(err, ErrExhausted) {
		t.Fatalf("stale budget selection = %v", err)
	}
	if decision := iterator.QuotaBudgetDecision(); decision.Reason != "quota_observation_unavailable" {
		t.Fatalf("stale exhaustion = %+v", decision)
	}
}
