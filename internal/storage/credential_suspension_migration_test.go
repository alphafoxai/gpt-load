package storage

import (
	"os"
	"testing"

	"gorm.io/gorm"

	migrationfiles "gpt-load/internal/storage/migrations"
)

func TestCredentialSuspensionMigration(t *testing.T) {
	t.Parallel()
	testCredentialSuspensionMigration(t, openInternalMigrationTestDatabase)
}

func TestExternalCredentialSuspensionMigration(t *testing.T) {
	dsn := os.Getenv("GPT_LOAD_DATABASE_TEST_DSN")
	if dsn == "" {
		t.Skip("GPT_LOAD_DATABASE_TEST_DSN is not set")
	}
	testCredentialSuspensionMigration(t, func(t *testing.T) *gorm.DB { return openExternalIncrementalMigrationDatabase(t, dsn) })
}

func testCredentialSuspensionMigration(t *testing.T, open func(*testing.T) *gorm.DB) {
	for _, scenario := range []string{"fresh", "upgrade", "interrupted", "stale_constraint"} {
		t.Run(scenario, func(t *testing.T) {
			db := open(t)
			if db.Dialector.Name() == "sqlite" {
				t.Parallel()
			}
			if scenario == "fresh" {
				if err := AutoMigrate(db); err != nil {
					t.Fatal(err)
				}
			} else if err := applyMigrationRegistry(db, migrations[:34]); err != nil {
				t.Fatal(err)
			}
			if err := db.Table("groups").Create(map[string]any{"id": 1, "name": "suspension migration", "channel_id": "openai", "params": "{}", "models": "[]", "created_at_ms": 1, "updated_at_ms": 1}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Table("credentials").Create(map[string]any{"id": 1, "group_id": 1, "data": "cipher", "fingerprint": "suspension-fingerprint", "identity_fingerprint": "suspension-identity", "created_at_ms": 1, "updated_at_ms": 1}).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "interrupted" && db.Dialector.Name() == "mysql" {
				if err := db.Create(&schemaMigration{ID: migrationResumeMarker(migrations[34].ID)}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec("ALTER TABLE credentials DROP CHECK chk_credential_status").Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "stale_constraint" {
				if err := db.Create(&schemaMigration{ID: migrations[34].ID}).Error; err != nil {
					t.Fatal(err)
				}
				if err := AutoMigrate(db); err == nil {
					t.Fatal("restart validation accepted stale credential status constraint")
				}
				return
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
			if err := db.Table("credentials").Where("id = 1").Update("status", "suspended").Error; err != nil {
				t.Fatalf("persist suspension: %v", err)
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatalf("restart validation: %v", err)
			}
			var stored struct{ Status, Data string }
			if err := db.Table("credentials").Where("id = 1").Take(&stored).Error; err != nil || stored.Status != "suspended" || stored.Data != "cipher" {
				t.Fatalf("persisted suspension = %#v, %v", stored, err)
			}
			for _, invalid := range []string{"blacklisted", "auth_required", ""} {
				if err := db.Table("credentials").Where("id = 1").Update("status", invalid).Error; err == nil {
					t.Fatalf("constraint accepted status %q", invalid)
				}
			}
			for _, name := range []string{"idx_credentials_group_identity", "idx_credentials_group_fingerprint"} {
				if !db.Migrator().HasIndex("credentials", name) {
					t.Fatalf("migration dropped credential index %s", name)
				}
			}
		})
	}
}

func TestCredentialSuspensionValidationRejectsLegacyConstraint(t *testing.T) {
	t.Parallel()
	db := openInternalMigrationTestDatabase(t)
	if err := applyMigrationRegistry(db, migrations[:34]); err != nil {
		t.Fatal(err)
	}
	if err := migrationfiles.Validate0035(db); err == nil {
		t.Fatal("suspension validator accepted legacy constraint")
	}
}
