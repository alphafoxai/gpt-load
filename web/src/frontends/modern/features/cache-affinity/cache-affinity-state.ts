import type { LocationQuery, LocationQueryRaw } from 'vue-router'
import type { AffinityFilters, AffinityOutcomeFilter } from '@modern/api/cache-affinity'
import { readTimeRange, timeRangeQuery, type TimeRangeState } from '@modern/app/time-range'
import { validUsageFilter } from '@modern/features/usage/usage-state'

// URL 中的筛选键与用量页、请求日志页保持一致；上游模型在 API 中名为 model。
export const cacheAffinityFilterKeys = [
  'group_id',
  'credential_id',
  'access_key_id',
  'upstream_model',
] as const
export type CacheAffinityFilterKey = (typeof cacheAffinityFilterKeys)[number]
export type CacheAffinityUrlFilters = Partial<Record<CacheAffinityFilterKey, string>>
export const cacheAffinityStateKeys = [
  ...cacheAffinityFilterKeys,
  'model',
  'preset',
  'from_ms',
  'to_ms',
] as const

export interface CacheAffinityState {
  filters: CacheAffinityUrlFilters
  range: TimeRangeState
}
export interface CacheAffinityRoute {
  name: 'modern-cache-affinity'
  query: LocationQueryRaw
}
export interface AffinityLogsRoute {
  name: 'modern-logs'
  query: LocationQueryRaw
}

export function parseCacheAffinityState(query: LocationQuery): CacheAffinityState {
  const filters: CacheAffinityUrlFilters = {}
  for (const key of cacheAffinityFilterKeys) {
    const value = query[key] ?? (key === 'upstream_model' ? query.model : undefined)
    if (typeof value === 'string' && value.trim() && validUsageFilter(key, value.trim()))
      filters[key] = value.trim()
  }
  return { filters, range: readTimeRange(query) as TimeRangeState }
}

function filterQuery(filters: CacheAffinityUrlFilters): LocationQueryRaw {
  const query: LocationQueryRaw = {}
  for (const key of cacheAffinityFilterKeys) if (filters[key]) query[key] = filters[key]
  return query
}

export function serializeCacheAffinityState(state: CacheAffinityState): LocationQueryRaw {
  return { ...filterQuery(state.filters), ...timeRangeQuery(state.range) }
}

export function affinityApiFilters(filters: CacheAffinityUrlFilters): AffinityFilters {
  return {
    group_id: filters.group_id,
    credential_id: filters.credential_id,
    access_key_id: filters.access_key_id,
    model: filters.upstream_model,
  }
}

// 从用量页跳转到缓存与粘性页时携带的筛选与时间范围（渠道筛选不适用于该页面）。
export function cacheAffinityRoute(
  filters: CacheAffinityUrlFilters,
  range: TimeRangeState,
): CacheAffinityRoute {
  return {
    name: 'modern-cache-affinity',
    query: { ...filterQuery(filters), ...timeRangeQuery(range) },
  }
}

// 跳转到请求日志，并按 affinity_outcome 过滤（miss 与 pinned 为分组值）。
export function affinityLogsRoute(
  filters: CacheAffinityUrlFilters,
  range: TimeRangeState,
  outcome: AffinityOutcomeFilter,
): AffinityLogsRoute {
  return {
    name: 'modern-logs',
    query: { ...filterQuery(filters), ...timeRangeQuery(range), affinity_outcome: outcome },
  }
}
