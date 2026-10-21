package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/platform/config"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/state"
	stateloader "gpt-load/internal/state/loader"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func setSuspensionTestStatus(t *testing.T, fixture serviceFixture, groupID, credentialID uint, status state.CredentialStatus) CredentialItemResponse {
	t.Helper()
	item, err := fixture.service.UpdateGroupCredential(t.Context(), groupID, credentialID, CredentialUpdateRequest{
		Status: optionalField[state.CredentialStatus]{Set: true, Value: status},
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestCredentialSuspensionRequiresExplicitClearAndSeparateEnable(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	groupID := createGroupWithCredentials(t, fixture, "suspended-secret\nordinary-secret")
	rows := batchRestoreRows(t, fixture, groupID)
	credentialID := rows[0].ID
	item := setSuspensionTestStatus(t, fixture, groupID, credentialID, state.CredentialStatusSuspended)
	if item.ConfiguredStatus != "suspended" || item.EffectiveStatus != "suspended" || item.Recovery.Mode != "suspended" || item.Recovery.Automatic {
		t.Fatalf("suspended item = %#v", item)
	}
	group, err := fixture.service.GetGroupSummary(t.Context(), groupID)
	if err != nil || group.CredentialCount != 2 {
		t.Fatalf("group containing a suspended credential is unreadable: %#v, %v", group, err)
	}
	records, err := fixture.service.ListGroupCollection(t.Context(), GroupCollectionQuery{Page: 1, PageSize: 20})
	if err != nil || len(records.Items) != 1 || records.Items[0].CredentialCounts.Disabled != 1 {
		t.Fatalf("suspended credential group summary is incorrect: %#v, %v", records, err)
	}
	home, err := fixture.service.ReadHomeBase(t.Context(), fixture.service.now().UnixMilli())
	if err != nil || home.Inventory.AvailableCredentialCount != 1 {
		t.Fatalf("suspended credential broke dashboard availability: %#v, %v", home, err)
	}
	if result, err := fixture.service.ImportGroupCredentials(t.Context(), groupID, CredentialImportRequest{Credentials: "suspended-secret"}); err != nil || result.CredentialsAdded != 0 || result.CredentialsDuplicated != 1 {
		t.Fatalf("duplicate import = %#v, %v", result, err)
	}
	if _, err := fixture.service.UpdateGroupCredential(t.Context(), groupID, credentialID, CredentialUpdateRequest{
		Status: optionalField[state.CredentialStatus]{Set: true, Value: state.CredentialStatusActive},
	}); !errors.Is(err, app_errors.ErrCredentialSuspended) {
		t.Fatalf("direct enable error = %v", err)
	}
	for _, action := range []CredentialBatchAction{CredentialBatchEnable, CredentialBatchDisable, CredentialBatchRestore} {
		result, err := fixture.service.BatchGroupCredentials(t.Context(), groupID, CredentialBatchRequest{Action: action, Scope: CredentialBatchScopeAll})
		if err != nil {
			t.Fatal(err)
		}
		for _, affected := range result.AffectedCredentialIDs {
			if affected == credentialID {
				t.Fatalf("%s affected suspended credential", action)
			}
		}
		var stored models.Credential
		if err := fixture.db.First(&stored, credentialID).Error; err != nil || stored.Status != models.CredentialStatusSuspended {
			t.Fatalf("%s stored status = %s, %v", action, stored.Status, err)
		}
	}
	if _, err := fixture.service.RestoreGroupCredential(t.Context(), groupID, credentialID); !errors.Is(err, app_errors.ErrCredentialSuspended) {
		t.Fatalf("individual restore error = %v", err)
	}
	item = setSuspensionTestStatus(t, fixture, groupID, credentialID, state.CredentialStatusDisabled)
	if item.ConfiguredStatus != "disabled" || item.EffectiveStatus != "disabled" {
		t.Fatalf("clear suspension item = %#v", item)
	}
	if _, ok := fixture.registry.ActiveEncryptedCredentialData(credentialID, groupID); ok {
		t.Fatal("clearing suspension made credential active")
	}
	item = setSuspensionTestStatus(t, fixture, groupID, credentialID, state.CredentialStatusActive)
	if item.ConfiguredStatus != "active" {
		t.Fatalf("separate enable item = %#v", item)
	}
}

func TestCredentialSuspensionSurvivesDatabaseReopenAndHydration(t *testing.T) {
	t.Parallel()
	fixture, dsn := newFileServiceFixture(t)
	groupID := createGroupWithCredentials(t, fixture, "durable-suspended-secret")
	credentialID := batchRestoreRows(t, fixture, groupID)[0].ID
	setSuspensionTestStatus(t, fixture, groupID, credentialID, state.CredentialStatusSuspended)
	reopened := newServiceFixtureWithDSN(t, dsn)
	input, err := stateloader.BuildCompileInputWithProxy(t.Context(), reopened.db, reopened.encryption, nil, reopened.channelRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.manager.Publish(input); err != nil {
		t.Fatal(err)
	}
	entries, err := stateloader.BuildCredentialEntriesWithProxy(t.Context(), reopened.db, reopened.encryption)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.registry.ReplaceCredentials(entries); err != nil {
		t.Fatal(err)
	}
	item, err := reopened.service.loadCredentialItem(t.Context(), groupID, credentialID)
	if err != nil || item.ConfiguredStatus != "suspended" || item.EffectiveStatus != "suspended" || item.Recovery.Mode != "suspended" {
		t.Fatalf("rehydrated item = %#v, %v", item, err)
	}
	if _, ok := reopened.registry.ActiveEncryptedCredentialData(credentialID, groupID); ok {
		t.Fatal("rehydrated suspended credential is dispatchable")
	}
}

func TestSuspendedCredentialProbeRejectsWithoutProviderCall(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	groupID := createGroupWithCredentials(t, fixture, "suspended-probe-secret")
	credentialID := batchRestoreRows(t, fixture, groupID)[0].ID
	setSuspensionTestStatus(t, fixture, groupID, credentialID, state.CredentialStatusSuspended)
	executor := &credentialProbeTestExecutor{result: successfulCredentialProbeResult()}
	fixture.service.executor = executor
	if _, err := fixture.service.TestGroupCredential(t.Context(), groupID, credentialID); !errors.Is(err, app_errors.ErrCredentialSuspended) {
		t.Fatalf("probe error = %v", err)
	}
	if len(executor.recordedCalls()) != 0 {
		t.Fatal("suspended probe contacted provider")
	}
	if _, err := fixture.service.RevealGroupCredential(t.Context(), groupID, credentialID); err != nil {
		t.Fatalf("local reveal error = %v", err)
	}
}

func TestSuspendedSubscriptionActionsRejectWithoutProviderCall(t *testing.T) {
	t.Parallel()
	fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
	setSuspensionTestStatus(t, fixture, groupID, credentialID, state.CredentialStatusSuspended)
	providerCalls := 0
	fixture.service.prepareSubscriptionCredential = func(context.Context, channel.ID, execution.CredentialSnapshot, bool) (subscriptionruntime.Credential, *execution.ErrorEvidence) {
		providerCalls++
		return subscriptionruntime.Credential{}, nil
	}
	fixture.service.recoverSubscriptionCredential = func(context.Context, channel.ID, execution.CredentialSnapshot) (subscriptionruntime.Credential, *execution.ErrorEvidence) {
		providerCalls++
		return subscriptionruntime.Credential{}, nil
	}
	fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
		providerCalls++
		return subscriptionruntime.Observation{}, nil
	}
	fixture.service.consumeSubscriptionResetCredit = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target, subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
		providerCalls++
		return subscriptionruntime.ResetCreditResult{}, nil
	}
	_, refreshErr := fixture.service.RefreshGroupCredential(t.Context(), groupID, credentialID)
	_, observationErr := fixture.service.RefreshCredentialObservation(t.Context(), groupID, credentialID)
	_, resetErr := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, "9ab3e8a8-e361-49c7-b398-e956a96b9b68")
	for _, err := range []error{refreshErr, observationErr, resetErr} {
		if !errors.Is(err, app_errors.ErrCredentialSuspended) {
			t.Fatalf("provider action error = %v", err)
		}
	}
	if providerCalls != 0 {
		t.Fatalf("suspended actions made %d provider calls", providerCalls)
	}
	if _, err := fixture.service.GetCredentialObservation(t.Context(), groupID, credentialID); err != nil {
		t.Fatalf("local observation read error = %v", err)
	}
	if _, err := fixture.service.DownloadGroupCredential(t.Context(), groupID, credentialID); err != nil {
		t.Fatalf("local download error = %v", err)
	}
}

func TestSuspensionErrorUsesStableLocalizedActionResponse(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	fixture := newServiceFixture(t)
	groupID := createGroupWithCredentials(t, fixture, "localized-suspended-secret")
	credentialID := batchRestoreRows(t, fixture, groupID)[0].ID
	setSuspensionTestStatus(t, fixture, groupID, credentialID, state.CredentialStatusSuspended)
	engine := gin.New()
	NewServer(&config.Config{AuthKey: "suspension-test-auth"}, fixture.service).RegisterRoutes(engine)
	response := serveCredentialRequest(t, engine, http.MethodPut,
		fmt.Sprintf("/api/groups/%d/credentials/%d", groupID, credentialID),
		`{"status":"active"}`, "suspension-test-auth", "")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"CREDENTIAL_SUSPENDED"`) {
		t.Fatalf("suspension response = %d %s", response.Code, response.Body.String())
	}
}
