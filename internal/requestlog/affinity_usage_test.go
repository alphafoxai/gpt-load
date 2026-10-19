package requestlog

import (
	"context"
	"reflect"
	"testing"
	"time"

	"gpt-load/internal/pricing"
	"gpt-load/internal/storage/models"
	"gpt-load/internal/telemetry"
)

func outcomeRow(index int, completedAt time.Time, credentialID uint, outcome telemetry.AffinityOutcome) models.RequestLog {
	row := aggregationRow(aggregationRequestID(index), completedAt, 8, "sticky-model")
	row.CredentialID = credentialID
	row.AffinityOutcome = string(outcome)
	row.AffinityHit = outcome.Hit()
	if outcome == telemetry.AffinityOutcomeHit {
		row.AffinityKind = telemetry.AffinityPromptPrefix
	}
	if outcome == telemetry.AffinityOutcomeContinuation {
		row.AffinityKind = telemetry.AffinityResponseContinuity
	}
	return row
}

func TestWriteBatchRollsUpAffinityOutcomesForUsageAndAnalysis(t *testing.T) {
	db := openRequestLogQueryDB(t)
	hour := time.Date(2026, time.July, 24, 13, 0, 0, 0, time.UTC)
	at := func(minute int) time.Time { return hour.Add(time.Duration(minute) * time.Minute) }

	cold := outcomeRow(1, at(1), 1, telemetry.AffinityOutcomeNew)
	cold.CacheWrite5MTokens, cold.CacheWritePremiumNanoUSD = 100, 25
	warm := outcomeRow(2, at(2), 1, telemetry.AffinityOutcomeHit)
	warm.CacheReadTokens, warm.CacheReadSavingsNanoUSD = 1_000, 900
	failedHit := outcomeRow(3, at(3), 1, telemetry.AffinityOutcomeHit)
	failedHit.Status = string(telemetry.RequestStatusError)
	moved := outcomeRow(4, at(4), 2, telemetry.AffinityOutcomeMissUnavailable)
	moved.CacheWrite1HTokens, moved.CacheWritePremiumNanoUSD = 1_000, 1_000
	continued := outcomeRow(5, at(5), 2, telemetry.AffinityOutcomeContinuation)
	continued.CacheReadSavingsNanoUSD = 50
	excluded := outcomeRow(6, at(6), 2, telemetry.AffinityOutcomeMissExcluded)
	idle := outcomeRow(7, at(7), 1, telemetry.AffinityOutcomeExpired)
	unpriced := outcomeRow(8, at(8), 3, telemetry.AffinityOutcomeNotApplicable)
	unpriced.CostState = string(pricing.CostStateUnpriced)
	unpriced.PricingCompleteness = string(pricing.CompletenessUnavailable)
	unpriced.EstimatedCostNanoUSD = 0

	rows := []models.RequestLog{cold, warm, failedHit, moved, continued, excluded, idle, unpriced}
	if err := (&gormBatchWriter{db: db}).WriteBatch(context.Background(), rows); err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}

	var usagePinned, usageHits int64
	if err := db.Model(&models.UsageStat{}).Select("COALESCE(SUM(affinity_pinned_count), 0)").Scan(&usagePinned).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.UsageStat{}).Select("COALESCE(SUM(affinity_hit_count), 0)").Scan(&usageHits).Error; err != nil {
		t.Fatal(err)
	}
	if usagePinned != 5 || usageHits != 3 {
		t.Fatalf("usage_stats pinned/hits = %d/%d, want 5/3", usagePinned, usageHits)
	}

	report, err := newRequestLogTestService(db).QueryCacheAffinity(context.Background(), CacheAffinityQuery{
		FromMS: hour.Add(30 * time.Minute).UnixMilli(), ToMS: hour.Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("QueryCacheAffinity() error = %v", err)
	}
	// 起点按小时汇总向下取整，整小时的数据都在。
	if report.FromMS != hour.UnixMilli() || report.BucketWidthMS != time.Hour.Milliseconds() {
		t.Fatalf("range = from %d width %d", report.FromMS, report.BucketWidthMS)
	}
	summary := report.Summary
	if summary.RequestCount != 8 || summary.SuccessCount != 7 || summary.PinnedCount != 5 || summary.HitCount != 3 ||
		summary.CacheReadTokens != 1_000 || summary.CacheWrite5MTokens != 100 || summary.CacheWrite1HTokens != 1_000 ||
		summary.CacheReadSavingsNanoUSD != 950 || summary.CacheWritePremiumNanoUSD != 1_025 {
		t.Fatalf("summary = %+v", summary)
	}
	gotOutcomes := make([]telemetry.AffinityOutcome, 0, len(report.Outcomes))
	for _, row := range report.Outcomes {
		gotOutcomes = append(gotOutcomes, row.Outcome)
	}
	wantOutcomes := []telemetry.AffinityOutcome{
		telemetry.AffinityOutcomeHit, telemetry.AffinityOutcomeContinuation, telemetry.AffinityOutcomeMissUnavailable,
		telemetry.AffinityOutcomeMissExcluded, telemetry.AffinityOutcomeExpired, telemetry.AffinityOutcomeNew,
		telemetry.AffinityOutcomeNotApplicable,
	}
	if !reflect.DeepEqual(gotOutcomes, wantOutcomes) {
		t.Fatalf("outcomes = %v, want display order %v", gotOutcomes, wantOutcomes)
	}
	if hit := report.Outcomes[0]; hit.RequestCount != 2 || hit.SuccessCount != 1 || hit.CacheReadSavingsNanoUSD != 900 {
		t.Fatalf("hit outcome = %+v", hit)
	}
	credentials := make([][2]int64, 0, len(report.Credentials))
	for _, row := range report.Credentials {
		credentials = append(credentials, [2]int64{int64(row.CredentialID), row.RequestCount})
	}
	if !reflect.DeepEqual(credentials, [][2]int64{{1, 4}, {2, 3}, {3, 1}}) {
		t.Fatalf("credential breakdown = %v", credentials)
	}
	if len(report.Series) != 1 || report.Series[0].PinnedCount != 5 || report.Series[0].BucketStartMS != hour.UnixMilli() {
		t.Fatalf("series = %+v", report.Series)
	}

	// 用量页的粘性卡片：小时（usage_stats）与分钟（请求明细）两条路径一致。
	service := newRequestLogTestService(db)
	for _, granularity := range []UsageGranularity{UsageGranularityHour, UsageGranularityMinute} {
		usage, err := service.QueryUsage(context.Background(), UsageQuery{
			FromMS: hour.UnixMilli(), ToMS: hour.Add(time.Hour).UnixMilli(), Granularity: granularity,
		})
		if err != nil {
			t.Fatalf("QueryUsage(%s) error = %v", granularity, err)
		}
		if usage.Summary.AffinityPinnedCount != 5 || usage.Summary.AffinityHitCount != 3 {
			t.Fatalf("%s usage affinity = %d/%d, want 5/3", granularity, usage.Summary.AffinityPinnedCount, usage.Summary.AffinityHitCount)
		}
	}
}

func TestQueryCacheAffinityUsesDayBucketsBeyondThreeDays(t *testing.T) {
	db := openRequestLogQueryDB(t)
	day := time.Date(2026, time.July, 20, 0, 0, 0, 0, time.UTC)
	rows := []models.RequestLog{
		outcomeRow(11, day.Add(2*time.Hour), 1, telemetry.AffinityOutcomeNew),
		outcomeRow(12, day.Add(20*time.Hour), 1, telemetry.AffinityOutcomeHit),
		outcomeRow(13, day.Add(50*time.Hour), 1, telemetry.AffinityOutcomeMissUnavailable),
	}
	if err := (&gormBatchWriter{db: db}).WriteBatch(context.Background(), rows); err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}
	report, err := newRequestLogTestService(db).QueryCacheAffinity(context.Background(), CacheAffinityQuery{
		FromMS: day.UnixMilli(), ToMS: day.Add(4 * 24 * time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("QueryCacheAffinity() error = %v", err)
	}
	if report.BucketWidthMS != (24*time.Hour).Milliseconds() || len(report.Series) != 2 {
		t.Fatalf("series = width %d, %+v", report.BucketWidthMS, report.Series)
	}
	first, third := report.Series[0], report.Series[1]
	if first.RequestCount != 2 || first.HitCount != 1 || third.BucketStartMS != day.Add(48*time.Hour).UnixMilli() || third.PinnedCount != 1 {
		t.Fatalf("day buckets = %+v", report.Series)
	}
}

func TestWriteBatchRejectsAffinityHitThatDisagreesWithOutcome(t *testing.T) {
	db := openRequestLogQueryDB(t)
	row := outcomeRow(21, time.Date(2026, time.July, 24, 13, 5, 0, 0, time.UTC), 1, telemetry.AffinityOutcomeMissUnavailable)
	row.AffinityHit = true
	if err := (&gormBatchWriter{db: db}).WriteBatch(context.Background(), []models.RequestLog{row}); err == nil {
		t.Fatal("WriteBatch() accepted an affinity hit on a missed outcome")
	}
	for _, model := range []any{&models.UsageStat{}, &models.CacheAffinityStat{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%T rows after rejected batch = %d, want 0", model, count)
		}
	}
}

// 记录结果之前的日志有 affinity_hit 但结果为空；分钟视图不能把它们算成续接或命中。
func TestQueryUsageMinuteIgnoresHitsLoggedBeforeOutcomesWereRecorded(t *testing.T) {
	db := openRequestLogQueryDB(t)
	start := time.Date(2026, time.July, 24, 13, 0, 0, 0, time.UTC)
	legacy := outcomeRow(31, start.Add(time.Minute), 1, telemetry.AffinityOutcomeUnrecorded)
	legacy.AffinityHit = true
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy RequestLog: %v", err)
	}
	report, err := newRequestLogTestService(db).QueryUsage(context.Background(), UsageQuery{
		FromMS: start.UnixMilli(), ToMS: start.Add(time.Hour).UnixMilli(), Granularity: UsageGranularityMinute,
	})
	if err != nil {
		t.Fatalf("QueryUsage() error = %v", err)
	}
	if report.Summary.RequestCount != 1 || report.Summary.AffinityPinnedCount != 0 || report.Summary.AffinityHitCount != 0 {
		t.Fatalf("summary = requests %d, pinned %d, hits %d; want 1, 0, 0",
			report.Summary.RequestCount, report.Summary.AffinityPinnedCount, report.Summary.AffinityHitCount)
	}
}
