package storage

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"gpt-load/internal/affinity"
	"gpt-load/internal/storage/models"
)

// AffinityBindingStore persists soft-affinity bindings for the affinity cache.
type AffinityBindingStore struct {
	db *gorm.DB
}

var _ affinity.Store = (*AffinityBindingStore)(nil)

func NewAffinityBindingStore(db *gorm.DB) *AffinityBindingStore {
	return &AffinityBindingStore{db: db}
}

func (store *AffinityBindingStore) LoadLive(ctx context.Context, now time.Time, limit int) ([]affinity.Binding, error) {
	var rows []models.AffinityBinding
	err := store.db.WithContext(ctx).
		Where("expires_at_ms > ?", now.UnixMilli()).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "expires_at_ms"}, Desc: true}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "binding_key"}}).
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	bindings := make([]affinity.Binding, 0, len(rows))
	for _, row := range rows {
		bindings = append(bindings, affinity.Binding{
			Key: affinity.Key(row.BindingKey),
			Target: affinity.Target{
				GroupID: row.GroupID, CredentialID: row.CredentialID,
				IdentityGeneration: uint64(row.IdentityGeneration),
			},
			ExpiresAt: time.UnixMilli(row.ExpiresAtMS),
			UpdatedAt: time.UnixMilli(row.UpdatedAtMS),
		})
	}
	return bindings, nil
}

func (store *AffinityBindingStore) Save(ctx context.Context, bindings []affinity.Binding) error {
	if len(bindings) == 0 {
		return nil
	}
	// 一条 upsert 语句内同一键只能出现一次（PostgreSQL 会拒绝），
	// 队列按写入顺序排列，因此保留每个键最后一次的值。
	position := make(map[string]int, len(bindings))
	rows := make([]models.AffinityBinding, 0, len(bindings))
	for _, binding := range bindings {
		row := models.AffinityBinding{
			BindingKey:         string(binding.Key),
			GroupID:            binding.Target.GroupID,
			CredentialID:       binding.Target.CredentialID,
			IdentityGeneration: int64(binding.Target.IdentityGeneration),
			ExpiresAtMS:        binding.ExpiresAt.UnixMilli(),
			UpdatedAtMS:        binding.UpdatedAt.UnixMilli(),
		}
		if index, seen := position[row.BindingKey]; seen {
			rows[index] = row
			continue
		}
		position[row.BindingKey] = len(rows)
		rows = append(rows, row)
	}
	return store.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "binding_key"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"group_id", "credential_id", "identity_generation", "expires_at_ms", "updated_at_ms",
			}),
		}).
		CreateInBatches(&rows, 100).Error
}

func (store *AffinityBindingStore) DeleteExpired(ctx context.Context, now time.Time) error {
	return store.db.WithContext(ctx).
		Where("expires_at_ms <= ?", now.UnixMilli()).
		Delete(&models.AffinityBinding{}).Error
}

func (store *AffinityBindingStore) Clear(ctx context.Context) error {
	return store.db.WithContext(ctx).
		Session(&gorm.Session{AllowGlobalUpdate: true}).
		Delete(&models.AffinityBinding{}).Error
}
