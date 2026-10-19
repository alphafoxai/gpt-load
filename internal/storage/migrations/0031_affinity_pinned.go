package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ID0031 = "0031_affinity_pinned"

type requestLog0031 struct {
	AffinityPinned bool `gorm:"not null;default:false"`
}

func (requestLog0031) TableName() string { return "request_logs" }

type affinityCounter0031 struct {
	table      string
	column     string
	constraint string
}

// 只统计迁移之后的请求：历史日志与聚合保持 0，不按 affinity_hit 推测续接次数。
var affinityCounters0031 = []affinityCounter0031{
	{table: "usage_aggregation_journal", column: "affinity_pinned_count", constraint: "chk_usage_journal_affinity_pinned"},
	{table: "usage_aggregation_journal", column: "affinity_hit_count", constraint: "chk_usage_journal_affinity_hit"},
	{table: "usage_stats", column: "affinity_pinned_count", constraint: "chk_usage_stat_affinity_pinned"},
	{table: "usage_stats", column: "affinity_hit_count", constraint: "chk_usage_stat_affinity_hit"},
}

// Up0031 记录请求到达时会话是否已有绑定，并在用量聚合里累计续接次数与其中的亲和命中次数。
func Up0031(db *gorm.DB) error {
	if err := ValidateRecoverable0031(db); err != nil {
		return err
	}
	if !db.Migrator().HasColumn(&requestLog0031{}, "affinity_pinned") {
		if err := db.Migrator().AddColumn(&requestLog0031{}, "AffinityPinned"); err != nil {
			return fmt.Errorf("add request log affinity pinned: %w", err)
		}
	}
	for _, counter := range affinityCounters0031 {
		if db.Migrator().HasColumn(counter.table, counter.column) {
			continue
		}
		statement := fmt.Sprintf(
			"ALTER TABLE ? ADD COLUMN %s BIGINT NOT NULL DEFAULT 0 CONSTRAINT %s CHECK (%s >= 0)",
			counter.column, counter.constraint, counter.column,
		)
		if err := db.Exec(statement, clause.Table{Name: counter.table}).Error; err != nil {
			return fmt.Errorf("add %s.%s: %w", counter.table, counter.column, err)
		}
	}
	return Validate0031(db)
}

func ValidateRecoverable0031(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "sqlite", "mysql", "postgres":
	default:
		return fmt.Errorf("unsupported affinity pinned migration driver %q", db.Dialector.Name())
	}
	for _, table := range []string{"request_logs", "usage_aggregation_journal", "usage_stats"} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("affinity pinned migration requires %s", table)
		}
	}
	if db.Migrator().HasColumn(&requestLog0031{}, "affinity_pinned") {
		if err := validatePinnedColumn0031(db); err != nil {
			return err
		}
	}
	for _, counter := range affinityCounters0031 {
		if db.Migrator().HasColumn(counter.table, counter.column) {
			if err := validateCounter0031(db, counter); err != nil {
				return err
			}
		}
	}
	return nil
}

func Validate0031(db *gorm.DB) error {
	if err := validatePinnedColumn0031(db); err != nil {
		return err
	}
	for _, counter := range affinityCounters0031 {
		if err := validateCounter0031(db, counter); err != nil {
			return err
		}
	}
	return nil
}

func validatePinnedColumn0031(db *gorm.DB) error {
	columns, err := db.Migrator().ColumnTypes(&requestLog0031{})
	if err != nil {
		return err
	}
	for _, column := range columns {
		if column.Name() != "affinity_pinned" {
			continue
		}
		kind := strings.ToLower(column.DatabaseTypeName())
		if kind != "bool" && kind != "boolean" && kind != "numeric" && !strings.Contains(kind, "int") {
			return fmt.Errorf("request log affinity pinned must be boolean")
		}
		if nullable, known := column.Nullable(); !known || nullable {
			return fmt.Errorf("request log affinity pinned must not be nullable")
		}
		value, known := column.DefaultValue()
		switch strings.ToLower(strings.Trim(strings.SplitN(value, "::", 2)[0], "'\"() ")) {
		case "0", "false":
		default:
			known = false
		}
		if !known {
			return fmt.Errorf("request log affinity pinned must default to false")
		}
		return nil
	}
	return fmt.Errorf("request log affinity pinned is missing")
}

func validateCounter0031(db *gorm.DB, counter affinityCounter0031) error {
	if !db.Migrator().HasConstraint(counter.table, counter.constraint) {
		return fmt.Errorf("%s constraint is missing", counter.constraint)
	}
	columns, err := db.Migrator().ColumnTypes(counter.table)
	if err != nil {
		return err
	}
	for _, column := range columns {
		if column.Name() != counter.column {
			continue
		}
		if !strings.Contains(strings.ToLower(column.DatabaseTypeName()), "int") {
			return fmt.Errorf("%s.%s must be integer", counter.table, counter.column)
		}
		if nullable, known := column.Nullable(); !known || nullable {
			return fmt.Errorf("%s.%s must not be nullable", counter.table, counter.column)
		}
		return nil
	}
	return fmt.Errorf("%s.%s is missing", counter.table, counter.column)
}
