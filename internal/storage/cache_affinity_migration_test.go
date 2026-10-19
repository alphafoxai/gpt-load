package storage

import (
	"fmt"
	"os"
	"testing"

	"gorm.io/gorm"
)

func TestCacheAffinityAnalysisMigrationContract(t *testing.T) {
	t.Parallel()

	testCacheAffinityAnalysisMigration(t, openInternalMigrationTestDatabase)
}
func TestExternalCacheAffinityAnalysisMigrationContract(t *testing.T) {
	dsn := os.Getenv("GPT_LOAD_DATABASE_TEST_DSN")
	if dsn == "" {
		t.Skip("GPT_LOAD_DATABASE_TEST_DSN is not set")
	}
	testCacheAffinityAnalysisMigration(t, func(t *testing.T) *gorm.DB { return openExternalIncrementalMigrationDatabase(t, dsn) })
}
func testCacheAffinityAnalysisMigration(t *testing.T, open func(*testing.T) *gorm.DB) {
	const requestID = "00000000-0000-4000-8000-000000000033"
	for _, scenario := range []string{"fresh", "upgrade", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			db := open(t)
			if db.Dialector.Name() == "sqlite" {
				t.Parallel()
			}
			if len(migrations) < 33 {
				t.Fatal("cache affinity analysis migration is missing")
			}
			if scenario != "fresh" {
				if err := applyMigrationRegistry(db, migrations[:32]); err != nil {
					t.Fatal(err)
				}
				if err := db.Table("request_logs").Create(map[string]any{
					"id": requestID, "completed_at_ms": 1000, "access_key_id": 1, "protocol": "openai-completions",
					"client_model": "model", "upstream_model": "model", "model_consistency": "not_applicable",
					"status": "success", "status_code": 200, "duration_ms": 1, "error_summary": "",
					"affinity_hit": true, "affinity_pinned": true,
					"usage_state": "not_applicable", "cost_state": "not_applicable", "pricing_completeness": "not_applicable",
				}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "interrupted" {
				entry := migrations[32]
				up := entry.Up
				entry.Up = func(tx *gorm.DB) error {
					if err := up(tx); err != nil {
						return err
					}
					return fmt.Errorf("interrupt after cache affinity analysis DDL")
				}
				if err := applyMigrationRegistry(db, append(append([]migration(nil), migrations[:32]...), entry)); err == nil {
					t.Fatal("interruption succeeded")
				}
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
			for _, column := range []string{"affinity_outcome", "cache_read_savings_nano_usd", "cache_write_premium_nano_usd"} {
				for _, table := range []string{"request_logs", "usage_aggregation_journal"} {
					if !db.Migrator().HasColumn(table, column) {
						t.Fatalf("%s.%s is missing", table, column)
					}
				}
			}
			if db.Migrator().HasColumn("request_logs", "affinity_pinned") {
				t.Fatal("obsolete request_logs.affinity_pinned remains")
			}
			if scenario != "fresh" {
				var row struct {
					AffinityHit             bool
					AffinityOutcome         string
					CacheReadSavingsNanoUSD int64
				}
				if err := db.Table("request_logs").Where("id = ?", requestID).Take(&row).Error; err != nil {
					t.Fatal(err)
				}
				// 历史日志不推测结果：保留命中标记，结果为空（未记录）。
				if !row.AffinityHit || row.AffinityOutcome != "" || row.CacheReadSavingsNanoUSD != 0 {
					t.Fatalf("existing request log changed: %+v", row)
				}
			}
			stat := map[string]any{
				"bucket_start_ms": 0, "access_key_id": 1, "group_id": 1, "credential_id": 1, "model": "model",
				"affinity_outcome": "hit", "request_count": 2, "success_count": 2, "cache_write_premium_nano_usd": -5,
			}
			if err := db.Table("cache_affinity_stats").Create(stat).Error; err != nil {
				t.Fatalf("create cache affinity stat: %v", err)
			}
			if err := db.Table("cache_affinity_stats").Create(stat).Error; err == nil {
				t.Fatal("duplicate cache affinity identity accepted")
			}
			for column, value := range map[string]int64{"cache_read_tokens": -1, "success_count": 3} {
				if err := db.Table("cache_affinity_stats").Where("model = ?", "model").Update(column, value).Error; err == nil {
					t.Fatalf("invalid cache_affinity_stats.%s = %d accepted", column, value)
				}
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
		})
	}
}
