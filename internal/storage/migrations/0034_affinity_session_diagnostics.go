package migrations

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ID0034 = "0034_affinity_session_diagnostics"

const requestLogAffinityKeyIndex0034 = "idx_request_logs_affinity_key_completed_id"

// 0034 的列沿用 0033 的列校验：文本列要求字符类型，整数列要求整数类型，全部非空。
var affinitySessionColumns0034 = []cacheAffinityColumn0033{
	{table: "request_logs", column: "affinity_key", definition: "VARCHAR(16) NOT NULL DEFAULT ''"},
	{table: "request_logs", column: "affinity_bound_credential_id", definition: "BIGINT NOT NULL DEFAULT 0"},
	{table: "request_logs", column: "affinity_miss_reason", definition: "VARCHAR(32) NOT NULL DEFAULT ''"},
}

// requestLog0034 冻结按会话筛选请求日志的索引，与列表游标顺序一致。
type requestLog0034 struct {
	AffinityKey   string `gorm:"column:affinity_key;index:idx_request_logs_affinity_key_completed_id,priority:1"`
	CompletedAtMS int64  `gorm:"column:completed_at_ms;index:idx_request_logs_affinity_key_completed_id,priority:2,sort:desc"`
	ID            string `gorm:"column:id;index:idx_request_logs_affinity_key_completed_id,priority:3,sort:desc"`
}

func (requestLog0034) TableName() string { return "request_logs" }

// Up0034 给请求日志加会话标识、绑定账号和未命中原因，并为按会话筛选建索引。历史日志保持空值，不回填。
func Up0034(db *gorm.DB) error {
	if err := ValidateRecoverable0034(db); err != nil {
		return err
	}
	for _, column := range affinitySessionColumns0034 {
		if db.Migrator().HasColumn(column.table, column.column) {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE ? ADD COLUMN %s %s", column.column, column.definition)
		if err := db.Exec(statement, clause.Table{Name: column.table}).Error; err != nil {
			return fmt.Errorf("add %s.%s: %w", column.table, column.column, err)
		}
	}
	if !db.Migrator().HasIndex(&requestLog0034{}, requestLogAffinityKeyIndex0034) {
		if err := db.Migrator().CreateIndex(&requestLog0034{}, requestLogAffinityKeyIndex0034); err != nil {
			return fmt.Errorf("create request log affinity key index: %w", err)
		}
	}
	return Validate0034(db)
}

func ValidateRecoverable0034(db *gorm.DB) error {
	if !db.Migrator().HasTable("request_logs") {
		return fmt.Errorf("affinity session diagnostics migration requires request_logs")
	}
	for _, column := range affinitySessionColumns0034 {
		if db.Migrator().HasColumn(column.table, column.column) {
			if err := validateColumn0033(db, column); err != nil {
				return err
			}
		}
	}
	return nil
}

func Validate0034(db *gorm.DB) error {
	for _, column := range affinitySessionColumns0034 {
		if err := validateColumn0033(db, column); err != nil {
			return err
		}
	}
	if !db.Migrator().HasIndex(&requestLog0034{}, requestLogAffinityKeyIndex0034) {
		return fmt.Errorf("request log affinity key index is missing")
	}
	return nil
}
