package migrations

import (
	"fmt"
	"regexp"
	"strings"

	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const ID0035 = "0035_credential_suspension"

const credentialStatusConstraint0035 = "chk_credential_status"

type credentialSuspension0035 struct {
	Status string `gorm:"type:varchar(32);not null;default:'active';check:chk_credential_status,status IN ('active','disabled','suspended')"`
}

func (credentialSuspension0035) TableName() string { return "credentials" }

func Up0035(db *gorm.DB) error {
	if err := ValidateRecoverable0035(db); err != nil {
		return err
	}
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		if err := rebuildCredentialStatusSQLite0035(db); err != nil {
			return err
		}
		return Validate0035(db)
	}
	if db.Migrator().HasConstraint(&credentialSuspension0035{}, credentialStatusConstraint0035) {
		definition, err := credentialStatusDefinition0035(db)
		if err != nil {
			return err
		}
		if credentialStatusValues0035(definition, true) {
			return Validate0035(db)
		}
		if strings.EqualFold(db.Dialector.Name(), "mysql") {
			if dialect, ok := db.Dialector.(*gormmysql.Dialector); ok && dialect.Config != nil &&
				mysqlRequiresCheckDropSyntax0003(dialect.ServerVersion) {
				if err := db.Exec("ALTER TABLE `credentials` DROP CHECK `chk_credential_status`").Error; err != nil {
					return fmt.Errorf("drop credential status constraint: %w", err)
				}
			} else if err := db.Migrator().DropConstraint(&credentialSuspension0035{}, credentialStatusConstraint0035); err != nil {
				return fmt.Errorf("drop credential status constraint: %w", err)
			}
		} else if err := db.Migrator().DropConstraint(&credentialSuspension0035{}, credentialStatusConstraint0035); err != nil {
			return fmt.Errorf("drop credential status constraint: %w", err)
		}
	}
	if err := db.Migrator().CreateConstraint(&credentialSuspension0035{}, credentialStatusConstraint0035); err != nil {
		return fmt.Errorf("add credential suspension status: %w", err)
	}
	return Validate0035(db)
}

func ValidateRecoverable0035(db *gorm.DB) error {
	model := &credentialSuspension0035{}
	if !db.Migrator().HasTable(model) || !db.Migrator().HasColumn(model, "status") {
		return fmt.Errorf("credential suspension migration requires credentials.status")
	}
	var invalid int64
	if err := db.Table("credentials").Where("status IS NULL OR status NOT IN ?", []string{"active", "disabled", "suspended"}).Count(&invalid).Error; err != nil {
		return fmt.Errorf("validate credential statuses: %w", err)
	}
	if invalid != 0 {
		return fmt.Errorf("credentials contain unsupported configured statuses")
	}
	if db.Migrator().HasConstraint(model, credentialStatusConstraint0035) {
		definition, err := credentialStatusDefinition0035(db)
		if err != nil {
			return err
		}
		if !credentialStatusValues0035(definition, false) && !credentialStatusValues0035(definition, true) {
			return fmt.Errorf("credential status constraint has an unexpected definition")
		}
	}
	return nil
}

func Validate0035(db *gorm.DB) error {
	if err := ValidateRecoverable0035(db); err != nil {
		return err
	}
	if !db.Migrator().HasConstraint(&credentialSuspension0035{}, credentialStatusConstraint0035) {
		return fmt.Errorf("credential status constraint is missing")
	}
	definition, err := credentialStatusDefinition0035(db)
	if err != nil {
		return err
	}
	if !credentialStatusValues0035(definition, true) {
		return fmt.Errorf("credential status constraint does not support suspension")
	}
	return nil
}

var credentialStatusCheck0035 = regexp.MustCompile(`(?is)CONSTRAINT\s+["` + "`" + `\[]?chk_credential_status["` + "`" + `\]]?\s+CHECK\s*\((.*?)\)`)
var credentialStatusCast0035 = regexp.MustCompile(`(?i)::(?:character varying|text)(?:\[\])?`)
var credentialStatusCharset0035 = regexp.MustCompile(`(?i)_[a-z0-9]+`)
var credentialStatusWhitespace0035 = regexp.MustCompile(`\s+`)

func credentialStatusValues0035(definition string, suspended bool) bool {
	if match := credentialStatusCheck0035.FindStringSubmatch(definition); len(match) == 2 {
		definition = match[1]
	}
	definition = strings.ToLower(definition)
	definition = strings.ReplaceAll(definition, `\'`, `'`)
	definition = credentialStatusCast0035.ReplaceAllString(definition, "")
	definition = credentialStatusCharset0035.ReplaceAllString(definition, "")
	definition = credentialStatusWhitespace0035.ReplaceAllString(definition, "")
	definition = strings.NewReplacer("(", "", ")", "", "[", "", "]", "", "`", "", `"`, "").Replace(definition)
	values := "'active','disabled'"
	if suspended {
		values += ",'suspended'"
	}
	for _, prefix := range []string{"", "check"} {
		if definition == prefix+"statusin"+values || definition == prefix+"status=anyarray"+values {
			return true
		}
	}
	return false
}

func credentialStatusDefinition0035(db *gorm.DB) (string, error) {
	var definition string
	var err error
	switch strings.ToLower(db.Dialector.Name()) {
	case "sqlite":
		err = db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", "credentials").Scan(&definition).Error
	case "mysql":
		err = db.Raw("SELECT CHECK_CLAUSE FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = ?", credentialStatusConstraint0035).Scan(&definition).Error
	case "postgres", "postgresql":
		err = db.Raw("SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = ? AND conrelid = 'credentials'::regclass", credentialStatusConstraint0035).Scan(&definition).Error
	default:
		return "", fmt.Errorf("inspect credential status constraint: unsupported driver %q", db.Dialector.Name())
	}
	if err != nil {
		return "", fmt.Errorf("inspect credential status constraint: %w", err)
	}
	if strings.TrimSpace(definition) == "" {
		return "", fmt.Errorf("credential status constraint definition is missing")
	}
	return definition, nil
}

func rebuildCredentialStatusSQLite0035(db *gorm.DB) error {
	definition, err := credentialStatusDefinition0035(db)
	if err != nil {
		return err
	}
	if credentialStatusValues0035(definition, true) {
		return nil
	}
	constraint := regexp.MustCompile(`(?is)CONSTRAINT\s+["` + "`" + `\[]?chk_credential_status["` + "`" + `\]]?\s+CHECK\s*\(\s*status\s+IN\s*\([^)]*\)\s*\)`)
	updated := constraint.ReplaceAllString(definition, `CONSTRAINT chk_credential_status CHECK (status IN ('active','disabled','suspended'))`)
	if updated == definition {
		return fmt.Errorf("cannot replace SQLite credential status constraint")
	}
	table := regexp.MustCompile(`(?is)^(CREATE\s+TABLE\s+)(?:"credentials"|` + "`credentials`" + `|\[credentials\]|credentials)`)
	updated = table.ReplaceAllString(updated, `${1}"credentials__0035"`)
	var objects []struct{ SQL string }
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE tbl_name = ? AND type IN ('index','trigger') AND sql IS NOT NULL", "credentials").Scan(&objects).Error; err != nil {
		return fmt.Errorf("inspect credential table objects: %w", err)
	}
	columns, err := db.Migrator().ColumnTypes("credentials")
	if err != nil {
		return err
	}
	names := make([]string, len(columns))
	for index, column := range columns {
		names[index] = quoteMigrationIdentifier0006(db, column.Name())
	}
	columnList := strings.Join(names, ",")
	for _, statement := range []string{
		updated,
		"INSERT INTO credentials__0035 (" + columnList + ") SELECT " + columnList + " FROM credentials",
		"DROP TABLE credentials",
		"ALTER TABLE credentials__0035 RENAME TO credentials",
	} {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("rebuild credential status: %w", err)
		}
	}
	for _, object := range objects {
		if err := db.Exec(object.SQL).Error; err != nil {
			return fmt.Errorf("restore credential table object: %w", err)
		}
	}
	return nil
}

func credentialStatusReplacementInProgress0035(db *gorm.DB) (bool, error) {
	if !strings.EqualFold(db.Dialector.Name(), "mysql") {
		return false, nil
	}
	var count int64
	if err := db.Table("schema_migrations").Where("id = ?", ID0035+"#building").Count(&count).Error; err != nil {
		return false, fmt.Errorf("inspect credential suspension migration marker: %w", err)
	}
	return count == 1, nil
}
