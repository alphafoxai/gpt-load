package subscription

import (
	"context"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/storage/models"
	"gpt-load/internal/subscription/providers/codex"
)

func TestSuspendedCredentialCannotRefreshThroughAnyManagerEntrypoint(t *testing.T) {
	for _, mode := range []string{"dispatch", "control", "manual_recovery"} {
		t.Run(mode, func(t *testing.T) {
			manager, db, _, keyService, row := newCredentialManagerFixture(t, credentialJSON("old-access", "old-refresh", time.Now().Add(-time.Minute)))
			if err := db.Model(&models.Credential{}).Where("id = ?", row.ID).Update("status", models.CredentialStatusSuspended).Error; err != nil {
				t.Fatal(err)
			}
			calls := 0
			manager.refresh = adaptCodexRefresh(func(_ context.Context, current codex.Credential) (codex.Credential, error) {
				calls++
				return current, nil
			})
			snapshot := credentialSnapshot(t, row, keyService)
			var evidence *execution.ErrorEvidence
			switch mode {
			case "dispatch":
				_, evidence = manager.Prepare(t.Context(), channel.Codex, snapshot, false)
			case "control":
				_, evidence = manager.PrepareForControl(t.Context(), channel.Codex, snapshot, true)
			case "manual_recovery":
				_, evidence = manager.RefreshForManualRecovery(t.Context(), channel.Codex, snapshot)
			}
			if evidence == nil || evidence.Code != "credential_suspended" || calls != 0 {
				t.Fatalf("evidence = %#v, provider calls = %d", evidence, calls)
			}
			var stored models.Credential
			if err := db.First(&stored, row.ID).Error; err != nil || stored.Status != models.CredentialStatusSuspended || stored.SecretVersion != row.SecretVersion || stored.AuthState != row.AuthState {
				t.Fatalf("suspended credential changed = %#v, %v", stored, err)
			}
		})
	}
}
