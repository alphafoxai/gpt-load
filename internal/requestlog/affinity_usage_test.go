package requestlog

import (
	"context"
	"testing"
	"time"

	"gpt-load/internal/storage/models"
	"gpt-load/internal/telemetry"
)

func affinityRow(index int, completedAt time.Time, pinned, hit bool) models.RequestLog {
	row := aggregationRow(aggregationRequestID(index), completedAt, 8, "sticky-model")
	row.AffinityPinned = pinned
	row.AffinityHit = hit
	if hit {
		row.AffinityKind = string(telemetry.AffinityPromptPrefix)
	}
	return row
}

func TestWriteBatchCountsAffinityFollowUpsAndHitsIntoUsage(t *testing.T) {
	db := openRequestLogQueryDB(t)
	hour := time.Date(2026, time.July, 24, 13, 0, 0, 0, time.UTC)
	failedHit := affinityRow(4, hour.Add(4*time.Minute), true, true)
	failedHit.Status = string(telemetry.RequestStatusError)
	rows := []models.RequestLog{
		affinityRow(1, hour.Add(time.Minute), false, false),
		affinityRow(2, hour.Add(2*time.Minute), true, true),
		affinityRow(3, hour.Add(3*time.Minute), true, false),
		failedHit,
	}
	if err := (&gormBatchWriter{db: db}).WriteBatch(context.Background(), rows); err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}

	var stat models.UsageStat
	if err := db.Take(&stat).Error; err != nil {
		t.Fatalf("query UsageStat: %v", err)
	}
	if stat.RequestCount != 4 || stat.AffinityPinnedCount != 3 || stat.AffinityHitCount != 2 {
		t.Fatalf("usage stat affinity = requests %d, pinned %d, hits %d; want 4, 3, 2",
			stat.RequestCount, stat.AffinityPinnedCount, stat.AffinityHitCount)
	}

	// 小时粒度读 usage_stats，分钟粒度读请求明细；两条路径必须给出同一组数字。
	service := newRequestLogTestService(db)
	for _, granularity := range []UsageGranularity{UsageGranularityHour, UsageGranularityMinute} {
		report, err := service.QueryUsage(context.Background(), UsageQuery{
			FromMS:      hour.UnixMilli(),
			ToMS:        hour.Add(time.Hour).UnixMilli(),
			Granularity: granularity,
		})
		if err != nil {
			t.Fatalf("QueryUsage(%s) error = %v", granularity, err)
		}
		if report.Summary.AffinityPinnedCount != 3 || report.Summary.AffinityHitCount != 2 {
			t.Fatalf("%s summary affinity = %d/%d, want 3/2",
				granularity, report.Summary.AffinityPinnedCount, report.Summary.AffinityHitCount)
		}
		var pinned, hits int64
		for _, point := range report.Series {
			pinned += point.AffinityPinnedCount
			hits += point.AffinityHitCount
		}
		if pinned != 3 || hits != 2 {
			t.Fatalf("%s series affinity = %d/%d, want 3/2", granularity, pinned, hits)
		}
	}
}

func TestWriteBatchRejectsAffinityHitWithoutPinnedConversation(t *testing.T) {
	db := openRequestLogQueryDB(t)
	row := affinityRow(5, time.Date(2026, time.July, 24, 13, 5, 0, 0, time.UTC), false, true)
	if err := (&gormBatchWriter{db: db}).WriteBatch(context.Background(), []models.RequestLog{row}); err == nil {
		t.Fatal("WriteBatch() accepted an affinity hit without a pinned conversation")
	}
	var count int64
	if err := db.Model(&models.UsageStat{}).Count(&count).Error; err != nil {
		t.Fatalf("count UsageStat: %v", err)
	}
	if count != 0 {
		t.Fatalf("usage stats after rejected batch = %d, want 0", count)
	}
}

// 迁移前的日志有 affinity_hit 但没有 affinity_pinned；分钟视图不能把它们算成命中，
// 否则 hit > pinned 会让整段用量查询校验失败。
func TestQueryUsageMinuteIgnoresHitsLoggedBeforePinnedWasRecorded(t *testing.T) {
	db := openRequestLogQueryDB(t)
	start := time.Date(2026, time.July, 24, 13, 0, 0, 0, time.UTC)
	legacy := affinityRow(6, start.Add(time.Minute), false, true)
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy RequestLog: %v", err)
	}
	report, err := newRequestLogTestService(db).QueryUsage(context.Background(), UsageQuery{
		FromMS:      start.UnixMilli(),
		ToMS:        start.Add(time.Hour).UnixMilli(),
		Granularity: UsageGranularityMinute,
	})
	if err != nil {
		t.Fatalf("QueryUsage() error = %v", err)
	}
	if report.Summary.RequestCount != 1 ||
		report.Summary.AffinityPinnedCount != 0 || report.Summary.AffinityHitCount != 0 {
		t.Fatalf("summary = requests %d, pinned %d, hits %d; want 1, 0, 0",
			report.Summary.RequestCount, report.Summary.AffinityPinnedCount, report.Summary.AffinityHitCount)
	}
}
