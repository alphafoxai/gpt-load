package requestlog

import (
	"fmt"
	"sort"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"gpt-load/internal/storage/models"
	"gpt-load/internal/telemetry"
	"gpt-load/internal/usage"
)

// cacheAffinityKey 是 cache_affinity_stats 的唯一键：小时、访问密钥、分组、账号、上游模型与亲和结果。
type cacheAffinityKey struct {
	BucketStartMS int64
	AccessKeyID   uint
	GroupID       uint
	CredentialID  uint
	Model         string
	Outcome       string
}

type cacheAffinityDelta struct {
	RequestCount             int64
	SuccessCount             int64
	UncachedInputTokens      int64
	CacheReadTokens          int64
	CacheWrite5MTokens       int64
	CacheWrite1HTokens       int64
	CacheWriteUnknownTokens  int64
	OutputTokens             int64
	EstimatedCostNanoUSD     int64
	CacheReadSavingsNanoUSD  int64
	CacheWritePremiumNanoUSD int64
}

// applyCacheAffinityJournals 把同一批用量日志累加到分析汇总表，与 usage_stats 在同一事务内。
func applyCacheAffinityJournals(tx *gorm.DB, journals []models.UsageAggregationJournal) error {
	deltas, err := buildCacheAffinityDeltas(journals)
	if err != nil {
		return err
	}
	if len(deltas) == 0 {
		return nil
	}
	keys := make([]cacheAffinityKey, 0, len(deltas))
	for key := range deltas {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		a, b := keys[left], keys[right]
		if a.BucketStartMS != b.BucketStartMS {
			return a.BucketStartMS < b.BucketStartMS
		}
		if a.AccessKeyID != b.AccessKeyID {
			return a.AccessKeyID < b.AccessKeyID
		}
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.CredentialID != b.CredentialID {
			return a.CredentialID < b.CredentialID
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.Outcome < b.Outcome
	})
	existing, err := queryExistingCacheAffinityStats(tx, keys)
	if err != nil {
		return err
	}
	for _, key := range keys {
		stat := existing[key]
		stat.BucketStartMS = key.BucketStartMS
		stat.AccessKeyID = key.AccessKeyID
		stat.GroupID = key.GroupID
		stat.CredentialID = key.CredentialID
		stat.Model = key.Model
		stat.AffinityOutcome = key.Outcome
		total, err := checkedCacheAffinityTotal(stat, deltas[key])
		if err != nil {
			return err
		}
		if err := tx.Clauses(cacheAffinityUpsertClause()).Create(&total).Error; err != nil {
			return fmt.Errorf("upsert cache affinity stat: %w", err)
		}
	}
	return nil
}

func buildCacheAffinityDeltas(journals []models.UsageAggregationJournal) (map[cacheAffinityKey]cacheAffinityDelta, error) {
	deltas := make(map[cacheAffinityKey]cacheAffinityDelta)
	for _, journal := range journals {
		if !telemetry.AffinityOutcome(journal.AffinityOutcome).Valid() {
			return nil, fmt.Errorf("aggregate cache affinity journal %q: invalid outcome %q", journal.RequestID, journal.AffinityOutcome)
		}
		key := cacheAffinityKey{
			BucketStartMS: journal.BucketStartMS,
			AccessKeyID:   journal.AccessKeyID,
			GroupID:       journal.GroupID,
			CredentialID:  journal.CredentialID,
			Model:         journal.Model,
			Outcome:       journal.AffinityOutcome,
		}
		delta := deltas[key]
		for _, field := range []struct {
			name   string
			target *int64
			value  int64
		}{
			{"request_count", &delta.RequestCount, journal.RequestCount},
			{"success_count", &delta.SuccessCount, journal.SuccessCount},
			{"uncached_input_tokens", &delta.UncachedInputTokens, journal.UncachedInputTokens},
			{"cache_read_tokens", &delta.CacheReadTokens, journal.CacheReadTokens},
			{"cache_write_5m_tokens", &delta.CacheWrite5MTokens, journal.CacheWrite5MTokens},
			{"cache_write_1h_tokens", &delta.CacheWrite1HTokens, journal.CacheWrite1HTokens},
			{"cache_write_unknown_tokens", &delta.CacheWriteUnknownTokens, journal.CacheWriteUnknownTokens},
			{"output_tokens", &delta.OutputTokens, journal.OutputTokens},
			{"estimated_cost_nano_usd", &delta.EstimatedCostNanoUSD, journal.EstimatedCostNanoUSD},
		} {
			if err := checkedInt64Add(field.target, field.value, field.name); err != nil {
				return nil, err
			}
		}
		var ok bool
		if delta.CacheReadSavingsNanoUSD, ok = checkedSignedAdd(delta.CacheReadSavingsNanoUSD, journal.CacheReadSavingsNanoUSD); !ok {
			return nil, fmt.Errorf("aggregate cache affinity cache_read_savings_nano_usd: checked addition failed")
		}
		if delta.CacheWritePremiumNanoUSD, ok = checkedSignedAdd(delta.CacheWritePremiumNanoUSD, journal.CacheWritePremiumNanoUSD); !ok {
			return nil, fmt.Errorf("aggregate cache affinity cache_write_premium_nano_usd: checked addition failed")
		}
		deltas[key] = delta
	}
	return deltas, nil
}

func queryExistingCacheAffinityStats(tx *gorm.DB, keys []cacheAffinityKey) (map[cacheAffinityKey]models.CacheAffinityStat, error) {
	const match = "bucket_start_ms = ? AND access_key_id = ? AND group_id = ? AND credential_id = ? AND model = ? AND affinity_outcome = ?"
	query := tx.Model(&models.CacheAffinityStat{})
	for index, key := range keys {
		arguments := []any{key.BucketStartMS, key.AccessKeyID, key.GroupID, key.CredentialID, key.Model, key.Outcome}
		if index == 0 {
			query = query.Where(match, arguments...)
		} else {
			query = query.Or(match, arguments...)
		}
	}
	var rows []models.CacheAffinityStat
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query existing cache affinity stats: %w", err)
	}
	existing := make(map[cacheAffinityKey]models.CacheAffinityStat, len(rows))
	for _, row := range rows {
		existing[cacheAffinityKey{
			BucketStartMS: row.BucketStartMS, AccessKeyID: row.AccessKeyID, GroupID: row.GroupID,
			CredentialID: row.CredentialID, Model: row.Model, Outcome: row.AffinityOutcome,
		}] = row
	}
	return existing, nil
}

func checkedCacheAffinityTotal(existing models.CacheAffinityStat, delta cacheAffinityDelta) (models.CacheAffinityStat, error) {
	total := existing
	for _, field := range []struct {
		name   string
		target *int64
		value  int64
	}{
		{"request_count", &total.RequestCount, delta.RequestCount},
		{"success_count", &total.SuccessCount, delta.SuccessCount},
		{"uncached_input_tokens", &total.UncachedInputTokens, delta.UncachedInputTokens},
		{"cache_read_tokens", &total.CacheReadTokens, delta.CacheReadTokens},
		{"cache_write_5m_tokens", &total.CacheWrite5MTokens, delta.CacheWrite5MTokens},
		{"cache_write_1h_tokens", &total.CacheWrite1HTokens, delta.CacheWrite1HTokens},
		{"cache_write_unknown_tokens", &total.CacheWriteUnknownTokens, delta.CacheWriteUnknownTokens},
		{"output_tokens", &total.OutputTokens, delta.OutputTokens},
		{"estimated_cost_nano_usd", &total.EstimatedCostNanoUSD, delta.EstimatedCostNanoUSD},
	} {
		value, ok := usage.CheckedAdd(*field.target, field.value)
		if !ok {
			return models.CacheAffinityStat{}, fmt.Errorf("calculate cache affinity stat %s: checked addition failed", field.name)
		}
		*field.target = value
	}
	var ok bool
	if total.CacheReadSavingsNanoUSD, ok = checkedSignedAdd(existing.CacheReadSavingsNanoUSD, delta.CacheReadSavingsNanoUSD); !ok {
		return models.CacheAffinityStat{}, fmt.Errorf("calculate cache affinity stat cache_read_savings_nano_usd: checked addition failed")
	}
	if total.CacheWritePremiumNanoUSD, ok = checkedSignedAdd(existing.CacheWritePremiumNanoUSD, delta.CacheWritePremiumNanoUSD); !ok {
		return models.CacheAffinityStat{}, fmt.Errorf("calculate cache affinity stat cache_write_premium_nano_usd: checked addition failed")
	}
	return total, nil
}

func cacheAffinityUpsertClause() clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{
			{Name: "bucket_start_ms"}, {Name: "access_key_id"}, {Name: "group_id"},
			{Name: "credential_id"}, {Name: "model"}, {Name: "affinity_outcome"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"request_count", "success_count", "uncached_input_tokens", "cache_read_tokens",
			"cache_write_5m_tokens", "cache_write_1h_tokens", "cache_write_unknown_tokens",
			"output_tokens", "estimated_cost_nano_usd", "cache_read_savings_nano_usd", "cache_write_premium_nano_usd",
		}),
	}
}

// checkedSignedAdd 用于可为负的金额：缓存节省与写入溢价。
func checkedSignedAdd(left, right int64) (int64, bool) {
	sum := left + right
	if (right > 0 && sum < left) || (right < 0 && sum > left) {
		return 0, false
	}
	return sum, true
}
