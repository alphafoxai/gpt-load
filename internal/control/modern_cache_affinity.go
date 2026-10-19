package control

import (
	"context"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
	"gpt-load/internal/requestlog"
)

type cacheAffinityReader interface {
	QueryCacheAffinity(context.Context, requestlog.CacheAffinityQuery) (requestlog.CacheAffinityReport, error)
}

type cacheAffinityAggregateResponse struct {
	RequestCount             int64  `json:"request_count"`
	SuccessCount             int64  `json:"success_count"`
	PinnedCount              int64  `json:"pinned_count"`
	HitCount                 int64  `json:"hit_count"`
	MissCount                int64  `json:"miss_count"`
	UncachedInputTokens      int64  `json:"uncached_input_tokens"`
	CacheReadTokens          int64  `json:"cache_read_tokens"`
	CacheWrite5MTokens       int64  `json:"cache_write_5m_tokens"`
	CacheWrite1HTokens       int64  `json:"cache_write_1h_tokens"`
	CacheWriteUnknownTokens  int64  `json:"cache_write_unknown_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	EstimatedCostNanoUSD     string `json:"estimated_cost_nano_usd"`
	CacheReadSavingsNanoUSD  string `json:"cache_read_savings_nano_usd"`
	CacheWritePremiumNanoUSD string `json:"cache_write_premium_nano_usd"`
}

type cacheAffinityOutcomeResponse struct {
	Outcome string `json:"outcome"`
	cacheAffinityAggregateResponse
}

type cacheAffinitySeriesResponse struct {
	BucketStartMS int64 `json:"bucket_start_ms"`
	BucketEndMS   int64 `json:"bucket_end_ms"`
	cacheAffinityAggregateResponse
}

type cacheAffinityCredentialResponse struct {
	CredentialID uint `json:"credential_id"`
	GroupID      uint `json:"group_id"`
	cacheAffinityAggregateResponse
}

type cacheAffinityGroupResponse struct {
	GroupID uint `json:"group_id"`
	cacheAffinityAggregateResponse
}

type cacheAffinityAccessKeyResponse struct {
	AccessKeyID uint `json:"access_key_id"`
	cacheAffinityAggregateResponse
}

type cacheAffinityModelResponse struct {
	Model string `json:"model"`
	cacheAffinityAggregateResponse
}

type cacheAffinityBreakdownsResponse struct {
	Credential []cacheAffinityCredentialResponse `json:"credential"`
	Group      []cacheAffinityGroupResponse      `json:"group"`
	AccessKey  []cacheAffinityAccessKeyResponse  `json:"access_key"`
	Model      []cacheAffinityModelResponse      `json:"model"`
}

type cacheAffinityResponse struct {
	FromMS        int64                           `json:"from_ms"`
	ToMS          int64                           `json:"to_ms"`
	ObservedAtMS  int64                           `json:"observed_at_ms"`
	BucketWidthMS int64                           `json:"bucket_width_ms"`
	Summary       cacheAffinityAggregateResponse  `json:"summary"`
	Outcomes      []cacheAffinityOutcomeResponse  `json:"outcomes"`
	Series        []cacheAffinitySeriesResponse   `json:"series"`
	Breakdowns    cacheAffinityBreakdownsResponse `json:"breakdowns"`
}

func (server *Server) handleModernCacheAffinity(c *gin.Context) {
	query, apiErr := parseCacheAffinityQuery(c.Request.URL.RawQuery)
	if apiErr != nil {
		writeServiceError(c, "modern_cache_affinity", apiErr)
		return
	}
	observedAt, err := safeEpochMilliseconds(server.service.now())
	if err != nil {
		writeServiceError(c, "modern_cache_affinity", err)
		return
	}
	reader, ok := server.service.usageStats.(cacheAffinityReader)
	if !ok {
		writeServiceError(c, "modern_cache_affinity", app_errors.ErrInternalServer)
		return
	}
	report, err := reader.QueryCacheAffinity(c.Request.Context(), query)
	if err != nil {
		writeServiceError(c, "modern_cache_affinity", app_errors.ParseDBError(err))
		return
	}
	result := cacheAffinityResponse{
		FromMS: report.FromMS, ToMS: report.ToMS, ObservedAtMS: observedAt, BucketWidthMS: report.BucketWidthMS,
		Summary:  mapCacheAffinityAggregate(report.Summary),
		Outcomes: make([]cacheAffinityOutcomeResponse, 0, len(report.Outcomes)),
		Series:   make([]cacheAffinitySeriesResponse, 0, len(report.Series)),
		Breakdowns: cacheAffinityBreakdownsResponse{
			Credential: make([]cacheAffinityCredentialResponse, 0, len(report.Credentials)),
			Group:      make([]cacheAffinityGroupResponse, 0, len(report.Groups)),
			AccessKey:  make([]cacheAffinityAccessKeyResponse, 0, len(report.AccessKeys)),
			Model:      make([]cacheAffinityModelResponse, 0, len(report.Models)),
		},
	}
	for _, row := range report.Outcomes {
		result.Outcomes = append(result.Outcomes, cacheAffinityOutcomeResponse{Outcome: string(row.Outcome), cacheAffinityAggregateResponse: mapCacheAffinityAggregate(row.CacheAffinityAggregate)})
	}
	for _, point := range report.Series {
		result.Series = append(result.Series, cacheAffinitySeriesResponse{BucketStartMS: point.BucketStartMS, BucketEndMS: point.BucketEndMS, cacheAffinityAggregateResponse: mapCacheAffinityAggregate(point.CacheAffinityAggregate)})
	}
	for _, row := range report.Credentials {
		result.Breakdowns.Credential = append(result.Breakdowns.Credential, cacheAffinityCredentialResponse{CredentialID: row.CredentialID, GroupID: row.GroupID, cacheAffinityAggregateResponse: mapCacheAffinityAggregate(row.CacheAffinityAggregate)})
	}
	for _, row := range report.Groups {
		result.Breakdowns.Group = append(result.Breakdowns.Group, cacheAffinityGroupResponse{GroupID: row.GroupID, cacheAffinityAggregateResponse: mapCacheAffinityAggregate(row.CacheAffinityAggregate)})
	}
	for _, row := range report.AccessKeys {
		result.Breakdowns.AccessKey = append(result.Breakdowns.AccessKey, cacheAffinityAccessKeyResponse{AccessKeyID: row.AccessKeyID, cacheAffinityAggregateResponse: mapCacheAffinityAggregate(row.CacheAffinityAggregate)})
	}
	for _, row := range report.Models {
		result.Breakdowns.Model = append(result.Breakdowns.Model, cacheAffinityModelResponse{Model: row.Model, cacheAffinityAggregateResponse: mapCacheAffinityAggregate(row.CacheAffinityAggregate)})
	}
	c.Header("Cache-Control", "no-store")
	response.SuccessI18n(c, "common.success", result)
}

func parseCacheAffinityQuery(rawQuery string) (requestlog.CacheAffinityQuery, *app_errors.APIError) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return requestlog.CacheAffinityQuery{}, app_errors.ErrBadRequest
	}
	allowed := map[string]struct{}{
		"from_ms": {}, "to_ms": {}, "group_id": {}, "credential_id": {}, "access_key_id": {}, "model": {},
	}
	for key, value := range values {
		if _, ok := allowed[key]; !ok || len(value) != 1 {
			return requestlog.CacheAffinityQuery{}, app_errors.ErrBadRequest
		}
	}
	fromValue, hasFrom := singleQueryValue(values, "from_ms")
	toValue, hasTo := singleQueryValue(values, "to_ms")
	if !hasFrom || !hasTo {
		return requestlog.CacheAffinityQuery{}, app_errors.ErrValidation
	}
	fromMS, err := parseCanonicalSafeMilliseconds(fromValue)
	if err != nil {
		return requestlog.CacheAffinityQuery{}, app_errors.ErrBadRequest
	}
	toMS, err := parseCanonicalSafeMilliseconds(toValue)
	if err != nil {
		return requestlog.CacheAffinityQuery{}, app_errors.ErrBadRequest
	}
	if toMS <= fromMS || toMS-fromMS > requestlog.CacheAffinityMaxRangeMS {
		return requestlog.CacheAffinityQuery{}, app_errors.ErrValidation
	}
	query := requestlog.CacheAffinityQuery{FromMS: fromMS, ToMS: toMS}
	for name, target := range map[string]**uint{
		"group_id": &query.GroupID, "credential_id": &query.CredentialID, "access_key_id": &query.AccessKeyID,
	} {
		value, ok := singleQueryValue(values, name)
		if !ok {
			continue
		}
		id, apiErr := parseUsageGroupID(value)
		if apiErr != nil {
			return requestlog.CacheAffinityQuery{}, apiErr
		}
		*target = &id
	}
	if value, ok := singleQueryValue(values, "model"); ok {
		if !validUsageModel(value) {
			return requestlog.CacheAffinityQuery{}, app_errors.ErrValidation
		}
		query.UpstreamModel = value
	}
	return query, nil
}

func mapCacheAffinityAggregate(source requestlog.CacheAffinityAggregate) cacheAffinityAggregateResponse {
	return cacheAffinityAggregateResponse{
		RequestCount: source.RequestCount, SuccessCount: source.SuccessCount,
		PinnedCount: source.PinnedCount, HitCount: source.HitCount, MissCount: source.PinnedCount - source.HitCount,
		UncachedInputTokens: source.UncachedInputTokens, CacheReadTokens: source.CacheReadTokens,
		CacheWrite5MTokens: source.CacheWrite5MTokens, CacheWrite1HTokens: source.CacheWrite1HTokens,
		CacheWriteUnknownTokens: source.CacheWriteUnknownTokens, OutputTokens: source.OutputTokens,
		EstimatedCostNanoUSD:     strconv.FormatInt(source.EstimatedCostNanoUSD, 10),
		CacheReadSavingsNanoUSD:  strconv.FormatInt(source.CacheReadSavingsNanoUSD, 10),
		CacheWritePremiumNanoUSD: strconv.FormatInt(source.CacheWritePremiumNanoUSD, 10),
	}
}
