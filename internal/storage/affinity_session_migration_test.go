package storage

import (
	"fmt"
	"os"
	"testing"

	"gorm.io/gorm"
)

func TestAffinitySessionDiagnosticsMigrationContract(t *testing.T) {
	t.Parallel()

	testAffinitySessionDiagnosticsMigration(t, openInternalMigrationTestDatabase)
}

func TestExternalAffinitySessionDiagnosticsMigrationContract(t *testing.T) {
	dsn := os.Getenv("GPT_LOAD_DATABASE_TEST_DSN")
	if dsn == "" {
		t.Skip("GPT_LOAD_DATABASE_TEST_DSN is not set")
	}
	testAffinitySessionDiagnosticsMigration(t, func(t *testing.T) *gorm.DB { return openExternalIncrementalMigrationDatabase(t, dsn) })
}

func testAffinitySessionDiagnosticsMigration(t *testing.T, open func(*testing.T) *gorm.DB) {
	const requestID = "00000000-0000-4000-8000-000000000034"
	for _, scenario := range []string{"fresh", "upgrade", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			db := open(t)
			if db.Dialector.Name() == "sqlite" {
				t.Parallel()
			}
			if len(migrations) < 34 {
				t.Fatal("affinity session diagnostics migration is missing")
			}
			if scenario != "fresh" {
				if err := applyMigrationRegistry(db, migrations[:33]); err != nil {
					t.Fatal(err)
				}
				if err := db.Table("request_logs").Create(map[string]any{
					"id": requestID, "completed_at_ms": 1000, "access_key_id": 1, "protocol": "anthropic",
					"client_model": "model", "upstream_model": "model", "model_consistency": "not_applicable",
					"status": "success", "status_code": 200, "duration_ms": 1, "error_summary": "",
					"affinity_hit": true, "affinity_outcome": "hit",
					"usage_state": "not_applicable", "cost_state": "not_applicable", "pricing_completeness": "not_applicable",
				}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "interrupted" {
				entry := migrations[33]
				up := entry.Up
				entry.Up = func(tx *gorm.DB) error {
					if err := up(tx); err != nil {
						return err
					}
					return fmt.Errorf("interrupt after affinity session diagnostics DDL")
				}
				if err := applyMigrationRegistry(db, append(append([]migration(nil), migrations[:33]...), entry)); err == nil {
					t.Fatal("interruption succeeded")
				}
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
			if !db.Migrator().HasIndex("request_logs", "idx_request_logs_affinity_key_completed_id") {
				t.Fatal("request log affinity key index is missing")
			}
			if scenario != "fresh" {
				var row struct {
					AffinityOutcome           string
					AffinityKey               string
					AffinityBoundCredentialID uint
					AffinityMissReason        string
				}
				if err := db.Table("request_logs").Where("id = ?", requestID).Take(&row).Error; err != nil {
					t.Fatal(err)
				}
				// 历史日志不回填：结果保留，会话标识、绑定账号与原因为空。
				if row.AffinityOutcome != "hit" || row.AffinityKey != "" || row.AffinityBoundCredentialID != 0 || row.AffinityMissReason != "" {
					t.Fatalf("existing request log changed: %+v", row)
				}
			}
			// 再次启动时已应用的 0034 必须通过校验。
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
		})
	}
}
