package storage

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"gpt-load/internal/affinity"
	"gpt-load/internal/storage/models"
)

func TestAffinityBindingStoreRoundTrip(t *testing.T) {
	t.Parallel()

	db := openInternalMigrationTestDatabase(t)
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	testAffinityBindingStoreRoundTrip(t, db)
}

func TestExternalAffinityBindingStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("GPT_LOAD_DATABASE_TEST_DSN")
	if dsn == "" {
		t.Skip("GPT_LOAD_DATABASE_TEST_DSN is not set")
	}
	db := openExternalIncrementalMigrationDatabase(t, dsn)
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	testAffinityBindingStoreRoundTrip(t, db)
}

func testAffinityBindingStoreRoundTrip(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	store := NewAffinityBindingStore(db)
	base := time.UnixMilli(1_800_000_000_000)
	first := affinity.Key(strings.Repeat("a", 64))
	second := affinity.Key(strings.Repeat("b", 64))
	expired := affinity.Key(strings.Repeat("c", 64))
	// Generations are FNV-64 values; the high bit must survive the signed column.
	highGeneration := uint64(0xFFFF_FFFF_FFFF_FFF1)

	if err := store.Save(ctx, []affinity.Binding{
		{Key: first, Target: affinity.Target{GroupID: 1, CredentialID: 11, IdentityGeneration: 101},
			ExpiresAt: base.Add(time.Hour), UpdatedAt: base},
		{Key: second, Target: affinity.Target{GroupID: 2, CredentialID: 21, IdentityGeneration: highGeneration},
			ExpiresAt: base.Add(2 * time.Hour), UpdatedAt: base},
		{Key: expired, Target: affinity.Target{GroupID: 1, CredentialID: 12, IdentityGeneration: 5},
			ExpiresAt: base.Add(-time.Minute), UpdatedAt: base},
		// Repeated key in one batch: the later write wins.
		{Key: first, Target: affinity.Target{GroupID: 1, CredentialID: 12, IdentityGeneration: 101},
			ExpiresAt: base.Add(90 * time.Minute), UpdatedAt: base.Add(time.Second)},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	live, err := store.LoadLive(ctx, base, 10)
	if err != nil {
		t.Fatalf("LoadLive() error = %v", err)
	}
	if len(live) != 2 || live[0].Key != second || live[1].Key != first {
		t.Fatalf("LoadLive() = %#v, want latest-expiring live rows first", live)
	}
	if live[0].Target != (affinity.Target{GroupID: 2, CredentialID: 21, IdentityGeneration: highGeneration}) ||
		!live[0].ExpiresAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("second binding round-trip = %#v", live[0])
	}
	if live[1].Target.CredentialID != 12 || !live[1].ExpiresAt.Equal(base.Add(90*time.Minute)) ||
		!live[1].UpdatedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("repeated-key binding = %#v, want the later write", live[1])
	}

	limited, err := store.LoadLive(ctx, base, 1)
	if err != nil || len(limited) != 1 || limited[0].Key != second {
		t.Fatalf("LoadLive(limit 1) = %#v, %v; want only the latest expiry", limited, err)
	}

	if err := store.Save(ctx, []affinity.Binding{{
		Key: first, Target: affinity.Target{GroupID: 1, CredentialID: 13, IdentityGeneration: 101},
		ExpiresAt: base.Add(3 * time.Hour), UpdatedAt: base.Add(2 * time.Second),
	}}); err != nil {
		t.Fatalf("Save(update) error = %v", err)
	}
	updated, err := store.LoadLive(ctx, base, 1)
	if err != nil || len(updated) != 1 || updated[0].Key != first || updated[0].Target.CredentialID != 13 {
		t.Fatalf("update not visible: %#v, %v", updated, err)
	}

	if err := store.DeleteExpired(ctx, base.Add(2*time.Hour)); err != nil {
		t.Fatalf("DeleteExpired() error = %v", err)
	}
	var remaining int64
	if err := db.Model(&models.AffinityBinding{}).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("rows after DeleteExpired = %d, want only the live binding", remaining)
	}

	if err := store.Clear(ctx); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if err := db.Model(&models.AffinityBinding{}).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("rows after Clear = %d, want 0", remaining)
	}
}
