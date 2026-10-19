package requestlog

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"gpt-load/internal/platform/epochms"
	"gpt-load/internal/storage/dbtx"
	"gpt-load/internal/storage/models"
	"gpt-load/internal/telemetry"
)

const (
	// CacheAffinityMaxRangeMS 限制单次分析的时间范围。
	CacheAffinityMaxRangeMS = 90 * epochms.MillisecondsPerDay
	// cacheAffinityHourlyMaxRangeMS 以内按小时分桶，更长按 UTC 日分桶。
	cacheAffinityHourlyMaxRangeMS = 72 * epochms.MillisecondsPerHour
	// CacheAffinityBreakdownLimit 是每个维度返回的行数上限。
	CacheAffinityBreakdownLimit = 50
)

// CacheAffinityQuery 按小时汇总查询；起点向下取整到整点，因此第一个小时整段计入。
type CacheAffinityQuery struct {
	FromMS        int64
	ToMS          int64
	GroupID       *uint
	CredentialID  *uint
	AccessKeyID   *uint
	UpstreamModel string
}

type CacheAffinityAggregate struct {
	RequestCount             int64
	SuccessCount             int64
	PinnedCount              int64
	HitCount                 int64
	UncachedInputTokens      int64
	CacheReadTokens          int64
	CacheWrite5MTokens       int64 `gorm:"column:cache_write_5m_tokens"`
	CacheWrite1HTokens       int64 `gorm:"column:cache_write_1h_tokens"`
	CacheWriteUnknownTokens  int64
	OutputTokens             int64
	EstimatedCostNanoUSD     int64
	CacheReadSavingsNanoUSD  int64
	CacheWritePremiumNanoUSD int64
}

type CacheAffinityOutcomeRow struct {
	Outcome telemetry.AffinityOutcome
	CacheAffinityAggregate
}

type CacheAffinitySeriesPoint struct {
	BucketStartMS int64
	BucketEndMS   int64
	CacheAffinityAggregate
}

type CacheAffinityCredentialRow struct {
	CredentialID uint
	GroupID      uint
	CacheAffinityAggregate
}

type CacheAffinityGroupRow struct {
	GroupID uint
	CacheAffinityAggregate
}

type CacheAffinityAccessKeyRow struct {
	AccessKeyID uint
	CacheAffinityAggregate
}

type CacheAffinityModelRow struct {
	Model string
	CacheAffinityAggregate
}

type CacheAffinityReport struct {
	FromMS        int64
	ToMS          int64
	BucketWidthMS int64
	Summary       CacheAffinityAggregate
	Outcomes      []CacheAffinityOutcomeRow
	Series        []CacheAffinitySeriesPoint
	Credentials   []CacheAffinityCredentialRow
	Groups        []CacheAffinityGroupRow
	AccessKeys    []CacheAffinityAccessKeyRow
	Models        []CacheAffinityModelRow
}

// QueryCacheAffinity 在一个只读快照里读取 cache_affinity_stats。
func (service *Service) QueryCacheAffinity(ctx context.Context, input CacheAffinityQuery) (CacheAffinityReport, error) {
	if service == nil || service.db == nil {
		return CacheAffinityReport{}, fmt.Errorf("query cache affinity: database is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if input.FromMS < 0 || input.ToMS <= input.FromMS || input.ToMS-input.FromMS > CacheAffinityMaxRangeMS {
		return CacheAffinityReport{}, fmt.Errorf("query cache affinity: invalid range")
	}
	fromMS, err := epochms.AlignDown(input.FromMS, epochms.MillisecondsPerHour)
	if err != nil {
		return CacheAffinityReport{}, fmt.Errorf("query cache affinity: %w", err)
	}
	report := CacheAffinityReport{FromMS: fromMS, ToMS: input.ToMS, BucketWidthMS: epochms.MillisecondsPerHour}
	if input.ToMS-input.FromMS > cacheAffinityHourlyMaxRangeMS {
		report.BucketWidthMS = epochms.MillisecondsPerDay
	}
	input.FromMS = fromMS
	err = dbtx.Run(ctx, service.db, dbtx.Options{
		Mode:           dbtx.ReadSnapshot,
		CleanupTimeout: usageRollbackTimeout,
		Operation:      "cache affinity read transaction",
	}, func(connection *gorm.DB) error {
		scope := func() *gorm.DB { return cacheAffinityScope(connection, input) }
		if err := scope().Select(cacheAffinityAggregateSelect).Scan(&report.Summary).Error; err != nil {
			return fmt.Errorf("query cache affinity summary: %w", err)
		}
		var outcomes []struct {
			AffinityOutcome string
			CacheAffinityAggregate
		}
		if err := scope().Select("affinity_outcome, " + cacheAffinityAggregateSelect).
			Group("affinity_outcome").Scan(&outcomes).Error; err != nil {
			return fmt.Errorf("query cache affinity outcomes: %w", err)
		}
		byOutcome := make(map[telemetry.AffinityOutcome]CacheAffinityAggregate, len(outcomes))
		for _, row := range outcomes {
			byOutcome[telemetry.AffinityOutcome(row.AffinityOutcome)] = row.CacheAffinityAggregate
		}
		for _, outcome := range telemetry.AffinityOutcomes {
			if row, ok := byOutcome[outcome]; ok && row.RequestCount > 0 {
				report.Outcomes = append(report.Outcomes, CacheAffinityOutcomeRow{Outcome: outcome, CacheAffinityAggregate: row})
				delete(byOutcome, outcome)
			}
		}
		if len(byOutcome) > 0 {
			return fmt.Errorf("query cache affinity outcomes: unknown persisted outcome")
		}

		var series []struct {
			SeriesBucketMS int64
			CacheAffinityAggregate
		}
		if err := scope().Select("bucket_start_ms - bucket_start_ms % ? AS series_bucket_ms, "+cacheAffinityAggregateSelect, report.BucketWidthMS).
			Group("series_bucket_ms").Order("series_bucket_ms ASC").Scan(&series).Error; err != nil {
			return fmt.Errorf("query cache affinity series: %w", err)
		}
		for _, point := range series {
			start := max(point.SeriesBucketMS, report.FromMS)
			end := min(point.SeriesBucketMS+report.BucketWidthMS, report.ToMS)
			report.Series = append(report.Series, CacheAffinitySeriesPoint{BucketStartMS: start, BucketEndMS: end, CacheAffinityAggregate: point.CacheAffinityAggregate})
		}

		order := "request_count DESC, "
		if err := scope().Select("credential_id, group_id, " + cacheAffinityAggregateSelect).
			Group("credential_id, group_id").Order(order + "credential_id ASC, group_id ASC").
			Limit(CacheAffinityBreakdownLimit).Scan(&report.Credentials).Error; err != nil {
			return fmt.Errorf("query cache affinity credentials: %w", err)
		}
		if err := scope().Select("group_id, " + cacheAffinityAggregateSelect).
			Group("group_id").Order(order + "group_id ASC").
			Limit(CacheAffinityBreakdownLimit).Scan(&report.Groups).Error; err != nil {
			return fmt.Errorf("query cache affinity groups: %w", err)
		}
		if err := scope().Select("access_key_id, " + cacheAffinityAggregateSelect).
			Group("access_key_id").Order(order + "access_key_id ASC").
			Limit(CacheAffinityBreakdownLimit).Scan(&report.AccessKeys).Error; err != nil {
			return fmt.Errorf("query cache affinity access keys: %w", err)
		}
		if err := scope().Select("model, " + cacheAffinityAggregateSelect).
			Group("model").Order(order + "model ASC").
			Limit(CacheAffinityBreakdownLimit).Scan(&report.Models).Error; err != nil {
			return fmt.Errorf("query cache affinity models: %w", err)
		}
		return validateCacheAffinityReport(report)
	})
	if err != nil {
		return CacheAffinityReport{}, err
	}
	return report, nil
}

func cacheAffinityScope(db *gorm.DB, input CacheAffinityQuery) *gorm.DB {
	scope := db.Session(&gorm.Session{NewDB: true}).Model(&models.CacheAffinityStat{}).
		Where("bucket_start_ms >= ? AND bucket_start_ms < ?", input.FromMS, input.ToMS)
	if input.GroupID != nil {
		scope = scope.Where("group_id = ?", *input.GroupID)
	}
	if input.CredentialID != nil {
		scope = scope.Where("credential_id = ?", *input.CredentialID)
	}
	if input.AccessKeyID != nil {
		scope = scope.Where("access_key_id = ?", *input.AccessKeyID)
	}
	if input.UpstreamModel != "" {
		scope = scope.Where("model = ?", input.UpstreamModel)
	}
	return scope
}

func validateCacheAffinityReport(report CacheAffinityReport) error {
	rows := []CacheAffinityAggregate{report.Summary}
	var outcomeTotal CacheAffinityAggregate
	for _, row := range report.Outcomes {
		rows = append(rows, row.CacheAffinityAggregate)
		outcomeTotal.RequestCount += row.RequestCount
		outcomeTotal.PinnedCount += row.PinnedCount
		outcomeTotal.HitCount += row.HitCount
	}
	for _, point := range report.Series {
		rows = append(rows, point.CacheAffinityAggregate)
	}
	for _, row := range rows {
		if row.RequestCount < row.SuccessCount || row.SuccessCount < 0 ||
			row.HitCount < 0 || row.HitCount > row.PinnedCount || row.PinnedCount > row.RequestCount ||
			row.UncachedInputTokens < 0 || row.CacheReadTokens < 0 || row.CacheWrite5MTokens < 0 ||
			row.CacheWrite1HTokens < 0 || row.CacheWriteUnknownTokens < 0 || row.OutputTokens < 0 ||
			row.EstimatedCostNanoUSD < 0 {
			return fmt.Errorf("query cache affinity: corrupt aggregate")
		}
	}
	if outcomeTotal.RequestCount != report.Summary.RequestCount ||
		outcomeTotal.PinnedCount != report.Summary.PinnedCount ||
		outcomeTotal.HitCount != report.Summary.HitCount {
		return fmt.Errorf("query cache affinity: outcomes do not add up to the summary")
	}
	return nil
}

func quotedOutcomes(outcomes []telemetry.AffinityOutcome) string {
	quoted := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		quoted = append(quoted, "'"+string(outcome)+"'")
	}
	return strings.Join(quoted, ", ")
}

var (
	pinnedOutcomesSQL = quotedOutcomes([]telemetry.AffinityOutcome{
		telemetry.AffinityOutcomeHit, telemetry.AffinityOutcomeContinuation,
		telemetry.AffinityOutcomeMissUnavailable, telemetry.AffinityOutcomeMissExcluded, telemetry.AffinityOutcomeMissReauthorized,
	})
	hitOutcomesSQL = quotedOutcomes([]telemetry.AffinityOutcome{
		telemetry.AffinityOutcomeHit, telemetry.AffinityOutcomeContinuation,
	})
	// 别名遵循 GORM 对嵌入字段的蛇形命名；5M/1H 字段带显式列名。
	cacheAffinityAggregateSelect = "" +
		"COALESCE(SUM(request_count), 0) AS request_count, " +
		"COALESCE(SUM(success_count), 0) AS success_count, " +
		"COALESCE(SUM(CASE WHEN affinity_outcome IN (" + pinnedOutcomesSQL + ") THEN request_count ELSE 0 END), 0) AS pinned_count, " +
		"COALESCE(SUM(CASE WHEN affinity_outcome IN (" + hitOutcomesSQL + ") THEN request_count ELSE 0 END), 0) AS hit_count, " +
		"COALESCE(SUM(uncached_input_tokens), 0) AS uncached_input_tokens, " +
		"COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens, " +
		"COALESCE(SUM(cache_write_5m_tokens), 0) AS cache_write_5m_tokens, " +
		"COALESCE(SUM(cache_write_1h_tokens), 0) AS cache_write_1h_tokens, " +
		"COALESCE(SUM(cache_write_unknown_tokens), 0) AS cache_write_unknown_tokens, " +
		"COALESCE(SUM(output_tokens), 0) AS output_tokens, " +
		"COALESCE(SUM(estimated_cost_nano_usd), 0) AS estimated_cost_nano_usd, " +
		"COALESCE(SUM(cache_read_savings_nano_usd), 0) AS cache_read_savings_nano_usd, " +
		"COALESCE(SUM(cache_write_premium_nano_usd), 0) AS cache_write_premium_nano_usd"
)
