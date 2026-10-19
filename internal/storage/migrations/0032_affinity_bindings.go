package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const ID0032 = "0032_affinity_bindings"

// affinityBinding0032 冻结迁移时的表结构。binding_key 是 HMAC-SHA256 十六进制摘要（64 字节），
// 不含提示词、访问密钥或账号凭据内容。
type affinityBinding0032 struct {
	BindingKey         string `gorm:"column:binding_key;type:varchar(64);primaryKey;not null"`
	GroupID            uint   `gorm:"column:group_id;not null;check:chk_affinity_bindings_group,group_id > 0"`
	CredentialID       uint   `gorm:"column:credential_id;not null;check:chk_affinity_bindings_credential,credential_id > 0"`
	IdentityGeneration int64  `gorm:"column:identity_generation;not null;check:chk_affinity_bindings_generation,identity_generation <> 0"`
	ExpiresAtMS        int64  `gorm:"column:expires_at_ms;not null;index:idx_affinity_bindings_expires_at_ms"`
	UpdatedAtMS        int64  `gorm:"column:updated_at_ms;not null"`
}

func (affinityBinding0032) TableName() string { return "affinity_bindings" }

// Up0032 创建持久化的会话亲和绑定表，使网关重启后仍能恢复会话到账号的绑定。
func Up0032(db *gorm.DB) error {
	if Validate0032(db) == nil {
		return nil
	}
	if err := ValidateRecoverable0032(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(&affinityBinding0032{}); err != nil {
		return fmt.Errorf("create affinity bindings: %w", err)
	}
	return Validate0032(db)
}

// ValidateRecoverable0032 允许中断后的残留空表继续补齐；含数据的不完整表必须人工处理。
func ValidateRecoverable0032(db *gorm.DB) error {
	if !db.Migrator().HasTable(&affinityBinding0032{}) {
		return nil
	}
	if Validate0032(db) == nil {
		return nil
	}
	var count int64
	if err := db.Model(&affinityBinding0032{}).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("incomplete affinity bindings schema contains data")
	}
	return nil
}

func Validate0032(db *gorm.DB) error {
	model := &affinityBinding0032{}
	if !db.Migrator().HasTable(model) {
		return fmt.Errorf("affinity bindings table is missing")
	}
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(model); err != nil {
		return err
	}
	columns, err := db.Migrator().ColumnTypes(model)
	if err != nil {
		return err
	}
	found := make(map[string]bool)
	for _, column := range columns {
		field := statement.Schema.LookUpField(column.Name())
		if field == nil {
			return fmt.Errorf("affinity bindings contains unexpected column %q", column.Name())
		}
		found[column.Name()] = true
		if (field.DataType == "int" || field.DataType == "uint") &&
			!strings.Contains(strings.ToLower(column.DatabaseTypeName()), "int") {
			return fmt.Errorf("affinity bindings column %s must be integer", column.Name())
		}
		if field.NotNull {
			if nullable, known := column.Nullable(); !known || nullable {
				return fmt.Errorf("affinity bindings column %s must be non-null", column.Name())
			}
		}
		if column.Name() == "binding_key" {
			if primary, known := column.PrimaryKey(); !known || !primary {
				return fmt.Errorf("affinity bindings binding_key must be the primary key")
			}
		}
	}
	for _, field := range statement.Schema.Fields {
		if !found[field.DBName] {
			return fmt.Errorf("affinity bindings column %s is missing", field.DBName)
		}
	}
	for _, index := range statement.Schema.ParseIndexes() {
		if !db.Migrator().HasIndex(model, index.Name) {
			return fmt.Errorf("affinity bindings index %s is missing", index.Name)
		}
	}
	for name := range statement.Schema.ParseCheckConstraints() {
		if !db.Migrator().HasConstraint(model, name) {
			return fmt.Errorf("affinity bindings constraint %s is missing", name)
		}
	}
	return nil
}
