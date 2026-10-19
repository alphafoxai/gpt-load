package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ID0033 = "0033_cache_affinity_analysis"

type cacheAffinityColumn0033 struct {
	table      string
	column     string
	definition string
}

// 新列在删除 affinity_pinned 之前全部加好：affinity_pinned 不在，说明前面的步骤都已完成。
var cacheAffinityColumns0033 = []cacheAffinityColumn0033{
	{table: "request_logs", column: "affinity_outcome", definition: "VARCHAR(32) NOT NULL DEFAULT ''"},
	{table: "request_logs", column: "cache_read_savings_nano_usd", definition: "BIGINT NOT NULL DEFAULT 0"},
	{table: "request_logs", column: "cache_write_premium_nano_usd", definition: "BIGINT NOT NULL DEFAULT 0"},
	{table: "usage_aggregation_journal", column: "affinity_outcome", definition: "VARCHAR(32) NOT NULL DEFAULT ''"},
	{table: "usage_aggregation_journal", column: "cache_read_savings_nano_usd", definition: "BIGINT NOT NULL DEFAULT 0"},
	{table: "usage_aggregation_journal", column: "cache_write_premium_nano_usd", definition: "BIGINT NOT NULL DEFAULT 0"},
}

// cacheAffinityStat0033 冻结 0033 创建的分析汇总表结构。
type cacheAffinityStat0033 struct {
	ID                       uint   `gorm:"primaryKey;autoIncrement"`
	BucketStartMS            int64  `gorm:"column:bucket_start_ms;not null;check:chk_cache_affinity_stat_bucket,bucket_start_ms >= 0;uniqueIndex:idx_cache_affinity_stats_identity,priority:1;index:idx_cache_affinity_stats_bucket"`
	AccessKeyID              uint   `gorm:"not null;uniqueIndex:idx_cache_affinity_stats_identity,priority:2"`
	GroupID                  uint   `gorm:"not null;uniqueIndex:idx_cache_affinity_stats_identity,priority:3"`
	CredentialID             uint   `gorm:"not null;default:0;uniqueIndex:idx_cache_affinity_stats_identity,priority:4"`
	Model                    string `gorm:"type:varchar(255);not null;uniqueIndex:idx_cache_affinity_stats_identity,priority:5"`
	AffinityOutcome          string `gorm:"type:varchar(32);not null;uniqueIndex:idx_cache_affinity_stats_identity,priority:6"`
	RequestCount             int64  `gorm:"not null;default:0;check:chk_cache_affinity_stat_requests,request_count >= success_count"`
	SuccessCount             int64  `gorm:"not null;default:0;check:chk_cache_affinity_stat_successes,success_count >= 0"`
	UncachedInputTokens      int64  `gorm:"column:uncached_input_tokens;not null;default:0;check:chk_cache_affinity_stat_uncached_input,uncached_input_tokens >= 0"`
	CacheReadTokens          int64  `gorm:"not null;default:0;check:chk_cache_affinity_stat_cache_read,cache_read_tokens >= 0"`
	CacheWrite5MTokens       int64  `gorm:"column:cache_write_5m_tokens;not null;default:0;check:chk_cache_affinity_stat_cache_write_5m,cache_write_5m_tokens >= 0"`
	CacheWrite1HTokens       int64  `gorm:"column:cache_write_1h_tokens;not null;default:0;check:chk_cache_affinity_stat_cache_write_1h,cache_write_1h_tokens >= 0"`
	CacheWriteUnknownTokens  int64  `gorm:"column:cache_write_unknown_tokens;not null;default:0;check:chk_cache_affinity_stat_cache_write_unknown,cache_write_unknown_tokens >= 0"`
	OutputTokens             int64  `gorm:"not null;default:0;check:chk_cache_affinity_stat_output,output_tokens >= 0"`
	EstimatedCostNanoUSD     int64  `gorm:"column:estimated_cost_nano_usd;not null;default:0;check:chk_cache_affinity_stat_cost,estimated_cost_nano_usd >= 0"`
	CacheReadSavingsNanoUSD  int64  `gorm:"column:cache_read_savings_nano_usd;not null;default:0"`
	CacheWritePremiumNanoUSD int64  `gorm:"column:cache_write_premium_nano_usd;not null;default:0"`
}

func (cacheAffinityStat0033) TableName() string { return "cache_affinity_stats" }

// Up0033 记录每个请求的亲和结果与缓存节省、写入溢价，建分析小时汇总表，并删除被结果取代的 affinity_pinned。
// 历史日志的结果保持空值（未记录），汇总表不回填。
func Up0033(db *gorm.DB) error {
	if err := ValidateRecoverable0033(db); err != nil {
		return err
	}
	for _, column := range cacheAffinityColumns0033 {
		if db.Migrator().HasColumn(column.table, column.column) {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE ? ADD COLUMN %s %s", column.column, column.definition)
		if err := db.Exec(statement, clause.Table{Name: column.table}).Error; err != nil {
			return fmt.Errorf("add %s.%s: %w", column.table, column.column, err)
		}
	}
	if !db.Migrator().HasTable(&cacheAffinityStat0033{}) {
		if err := db.Migrator().CreateTable(&cacheAffinityStat0033{}); err != nil {
			return fmt.Errorf("create cache affinity stats: %w", err)
		}
	}
	if db.Migrator().HasColumn("request_logs", "affinity_pinned") {
		if err := db.Exec("ALTER TABLE request_logs DROP COLUMN affinity_pinned").Error; err != nil {
			return fmt.Errorf("remove request log affinity pinned: %w", err)
		}
	}
	return Validate0033(db)
}

func ValidateRecoverable0033(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "sqlite", "mysql", "postgres":
	default:
		return fmt.Errorf("unsupported cache affinity analysis migration driver %q", db.Dialector.Name())
	}
	for _, table := range []string{"request_logs", "usage_aggregation_journal", "usage_stats"} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("cache affinity analysis migration requires %s", table)
		}
	}
	for _, column := range cacheAffinityColumns0033 {
		if db.Migrator().HasColumn(column.table, column.column) {
			if err := validateColumn0033(db, column); err != nil {
				return err
			}
		}
	}
	if !db.Migrator().HasColumn("request_logs", "affinity_pinned") {
		// 删列是最后一步；之前的步骤必须已全部完成。
		return Validate0033(db)
	}
	return nil
}

func Validate0033(db *gorm.DB) error {
	for _, column := range cacheAffinityColumns0033 {
		if err := validateColumn0033(db, column); err != nil {
			return err
		}
	}
	if !db.Migrator().HasTable(&cacheAffinityStat0033{}) {
		return fmt.Errorf("cache_affinity_stats is missing")
	}
	if !db.Migrator().HasIndex(&cacheAffinityStat0033{}, "idx_cache_affinity_stats_identity") {
		return fmt.Errorf("cache_affinity_stats identity index is missing")
	}
	if db.Migrator().HasColumn("request_logs", "affinity_pinned") {
		return fmt.Errorf("obsolete request log affinity_pinned remains")
	}
	return nil
}

func validateColumn0033(db *gorm.DB, expected cacheAffinityColumn0033) error {
	columns, err := db.Migrator().ColumnTypes(expected.table)
	if err != nil {
		return err
	}
	for _, column := range columns {
		if column.Name() != expected.column {
			continue
		}
		kind := strings.ToLower(column.DatabaseTypeName())
		textual := strings.HasPrefix(expected.definition, "VARCHAR")
		if textual && !strings.Contains(kind, "char") && kind != "text" {
			return fmt.Errorf("%s.%s must be text", expected.table, expected.column)
		}
		if !textual && !strings.Contains(kind, "int") {
			return fmt.Errorf("%s.%s must be integer", expected.table, expected.column)
		}
		if nullable, known := column.Nullable(); !known || nullable {
			return fmt.Errorf("%s.%s must not be nullable", expected.table, expected.column)
		}
		return nil
	}
	return fmt.Errorf("%s.%s is missing", expected.table, expected.column)
}
