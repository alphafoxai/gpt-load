package storage

import (
	"fmt"
	"os"
	"testing"

	"gorm.io/gorm"
)

func TestAffinityPinnedMigrationContract(t *testing.T) {
	t.Parallel()

	testAffinityPinnedMigration(t, openInternalMigrationTestDatabase)
}
func TestExternalAffinityPinnedMigrationContract(t *testing.T) {
	dsn := os.Getenv("GPT_LOAD_DATABASE_TEST_DSN")
	if dsn == "" {
		t.Skip("GPT_LOAD_DATABASE_TEST_DSN is not set")
	}
	testAffinityPinnedMigration(t, func(t *testing.T) *gorm.DB { return openExternalIncrementalMigrationDatabase(t, dsn) })
}
func testAffinityPinnedMigration(t *testing.T, open func(*testing.T) *gorm.DB) {
	for _, scenario := range []string{"fresh", "upgrade", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			db := open(t)
			if db.Dialector.Name() == "sqlite" {
				t.Parallel()
			}
			if len(migrations) < 31 {
				t.Fatal("affinity pinned migration is missing")
			}
			if scenario != "fresh" {
				if err := applyMigrationRegistry(db, migrations[:30]); err != nil {
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
				entry := migrations[30]
				up := entry.Up
				entry.Up = func(tx *gorm.DB) error {
					if err := up(tx); err != nil {
						return err
					}
					return fmt.Errorf("interrupt after affinity pinned DDL")
				}
				if err := applyMigrationRegistry(db, append(append([]migration(nil), migrations[:30]...), entry)); err == nil {
					t.Fatal("interruption succeeded")
				}
			}
			// 只应用到 0031 来检查它自己的契约；0033 之后 affinity_pinned 会被删除。
			if err := applyMigrationRegistry(db, migrations[:31]); err != nil {
				t.Fatal(err)
			}
			if !db.Migrator().HasColumn("request_logs", "affinity_pinned") {
				t.Fatal("request_logs.affinity_pinned is missing")
			}
			if scenario != "fresh" {
				var row struct {
					RequestCount        int64
					AffinityPinnedCount int64
					AffinityHitCount    int64
				}
				if err := db.Table("usage_stats").Where("model = ?", "existing").Take(&row).Error; err != nil {
					t.Fatal(err)
				}
				// 历史聚合不回填：迁移前没有记录续接，按 0 处理而不是由命中推测。
				if row.RequestCount != 2 || row.AffinityPinnedCount != 0 || row.AffinityHitCount != 0 {
					t.Fatalf("existing usage stat changed: %+v", row)
				}
				if err := db.Table("usage_stats").Where("model = ?", "existing").
					Updates(map[string]any{"affinity_pinned_count": 2, "affinity_hit_count": 1}).Error; err != nil {
					t.Fatal(err)
				}
				for _, column := range []string{"affinity_pinned_count", "affinity_hit_count"} {
					if err := db.Table("usage_stats").Where("model = ?", "existing").Update(column, -1).Error; err == nil {
						t.Fatalf("negative usage_stats.%s accepted", column)
					}
				}
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
			if db.Migrator().HasColumn("request_logs", "affinity_pinned") {
				t.Fatal("request_logs.affinity_pinned survived 0033")
			}
			// 再次启动：0031 的已应用校验必须接受 0033 删掉的列，并保留计数列。
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
		})
	}
}
