package storage

import (
	"fmt"
	"os"
	"testing"

	"gorm.io/gorm"

	migrationfiles "gpt-load/internal/storage/migrations"
)

func TestAffinityBindingsMigrationContract(t *testing.T) {
	t.Parallel()

	testAffinityBindingsMigration(t, openInternalMigrationTestDatabase)
}

func TestExternalAffinityBindingsMigrationContract(t *testing.T) {
	dsn := os.Getenv("GPT_LOAD_DATABASE_TEST_DSN")
	if dsn == "" {
		t.Skip("GPT_LOAD_DATABASE_TEST_DSN is not set")
	}
	testAffinityBindingsMigration(t, func(t *testing.T) *gorm.DB { return openExternalIncrementalMigrationDatabase(t, dsn) })
}

func testAffinityBindingsMigration(t *testing.T, open func(*testing.T) *gorm.DB) {
	for _, scenario := range []string{"fresh", "upgrade", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			db := open(t)
			if db.Dialector.Name() == "sqlite" {
				t.Parallel()
			}
			index := affinityBindingsMigrationIndex(t)
			if scenario != "fresh" {
				if err := applyMigrationRegistry(db, migrations[:index]); err != nil {
					t.Fatal(err)
				}
				if err := db.Table("usage_stats").Create(map[string]any{
					"bucket_start_ms": 0, "access_key_id": 1, "channel_id": "", "group_id": 1, "credential_id": 0,
					"model": "existing", "request_count": 2, "success_count": 2, "failure_count": 0,
				}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "interrupted" {
				entry := migrations[index]
				up := entry.Up
				entry.Up = func(tx *gorm.DB) error {
					if err := up(tx); err != nil {
						return err
					}
					return fmt.Errorf("interrupt after affinity bindings DDL")
				}
				if err := applyMigrationRegistry(db, append(append([]migration(nil), migrations[:index]...), entry)); err == nil {
					t.Fatal("interruption succeeded")
				}
				// 事务型数据库中，中断的迁移必须整体回滚，不留下半成品表。
				if db.Dialector.Name() != "mysql" && db.Migrator().HasTable("affinity_bindings") {
					t.Fatal("interrupted migration left affinity_bindings behind")
				}
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
			if err := migrationfiles.Validate0032(db); err != nil {
				t.Fatal(err)
			}
			if !db.Migrator().HasIndex("affinity_bindings", "idx_affinity_bindings_expires_at_ms") {
				t.Fatal("affinity_bindings expiry index is missing")
			}
			if scenario != "fresh" {
				var row struct {
					RequestCount int64
				}
				if err := db.Table("usage_stats").Where("model = ?", "existing").Take(&row).Error; err != nil {
					t.Fatal(err)
				}
				if row.RequestCount != 2 {
					t.Fatalf("existing usage stat changed: %+v", row)
				}
			}

			valid := map[string]any{
				"binding_key": "key-one", "group_id": 1, "credential_id": 2,
				"identity_generation": -7, "expires_at_ms": 1000, "updated_at_ms": 500,
			}
			if err := db.Table("affinity_bindings").Create(valid).Error; err != nil {
				t.Fatalf("valid binding rejected: %v", err)
			}
			if err := db.Table("affinity_bindings").Create(valid).Error; err == nil {
				t.Fatal("duplicate binding key accepted")
			}
			for _, column := range []string{"group_id", "credential_id", "identity_generation"} {
				invalid := map[string]any{
					"binding_key": "key-" + column, "group_id": 1, "credential_id": 2,
					"identity_generation": 3, "expires_at_ms": 1000, "updated_at_ms": 500,
				}
				invalid[column] = 0
				if err := db.Table("affinity_bindings").Create(invalid).Error; err == nil {
					t.Fatalf("affinity_bindings.%s = 0 accepted", column)
				}
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
			var applied int64
			if err := db.Table(migrationLedgerTable).Where("id = ?", migrationfiles.ID0032).Count(&applied).Error; err != nil {
				t.Fatal(err)
			}
			if applied != 1 {
				t.Fatalf("%s recorded %d times, want 1", migrationfiles.ID0032, applied)
			}
		})
	}
}

func affinityBindingsMigrationIndex(t *testing.T) int {
	t.Helper()
	for index, entry := range migrations {
		if entry.ID == migrationfiles.ID0032 {
			return index
		}
	}
	t.Fatalf("migration %s is not registered", migrationfiles.ID0032)
	return -1
}
