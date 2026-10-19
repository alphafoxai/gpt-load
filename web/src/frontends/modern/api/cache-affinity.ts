import type { ApiClient } from '@shared/http/client'
import { InvalidResponseError } from '@shared/http/errors'
import { integer, list, oneOf, record, text } from './response'

// 按展示顺序排列；'' 表示接入粘性记录前写入的请求。
export const affinityOutcomes = [
  'hit',
  'continuation',
  'miss_unavailable',
  'miss_excluded',
  'miss_reauthorized',
  'expired',
  'new',
  'not_applicable',
  '',
] as const
export type AffinityOutcome = (typeof affinityOutcomes)[number]
// 请求日志 affinity_outcome 筛选值：全部具体结果，加上 miss（所有 miss_*）与 pinned（所有已绑定结果）。
export const affinityOutcomeFilters = [
  'hit',
  'continuation',
  'miss_unavailable',
  'miss_excluded',
  'miss_reauthorized',
  'expired',
  'new',
  'not_applicable',
  'miss',
  'pinned',
] as const
export type AffinityOutcomeFilter = (typeof affinityOutcomeFilters)[number]
export const pinnedAffinityOutcomes: readonly AffinityOutcome[] = [
  'hit',
  'continuation',
  'miss_unavailable',
  'miss_excluded',
  'miss_reauthorized',
]
export const hitAffinityOutcomes: readonly AffinityOutcome[] = ['hit', 'continuation']
export const missAffinityOutcomes: readonly AffinityOutcome[] = [
  'miss_unavailable',
  'miss_excluded',
  'miss_reauthorized',
]
export const affinityDimensions = ['credential', 'group', 'access_key', 'model'] as const
export type AffinityDimension = (typeof affinityDimensions)[number]
export const affinityFilterKeys = ['group_id', 'credential_id', 'access_key_id', 'model'] as const
export type AffinityFilterKey = (typeof affinityFilterKeys)[number]
export type AffinityFilters = Partial<Record<AffinityFilterKey, string>>

const counts = [
  'request_count',
  'success_count',
  'pinned_count',
  'hit_count',
  'miss_count',
  'uncached_input_tokens',
  'cache_read_tokens',
  'cache_write_5m_tokens',
  'cache_write_1h_tokens',
  'cache_write_unknown_tokens',
  'output_tokens',
] as const
const moneyFields = [
  'estimated_cost_nano_usd',
  'cache_read_savings_nano_usd',
  'cache_write_premium_nano_usd',
] as const
export type AffinityCount = (typeof counts)[number]
export type AffinityMoneyField = (typeof moneyFields)[number]
export type AffinityAggregate = Record<AffinityCount, number> & Record<AffinityMoneyField, string>
export type AffinityOutcomeRow = AffinityAggregate & { outcome: AffinityOutcome }
export type AffinityBucket = AffinityAggregate & { bucket_start_ms: number; bucket_end_ms: number }
export interface AffinityBreakdowns {
  credential: (AffinityAggregate & { credential_id: number; group_id: number })[]
  group: (AffinityAggregate & { group_id: number })[]
  access_key: (AffinityAggregate & { access_key_id: number })[]
  model: (AffinityAggregate & { model: string })[]
}
export interface CacheAffinityReport {
  from_ms: number
  to_ms: number
  observed_at_ms: number
  bucket_width_ms: number
  summary: AffinityAggregate
  outcomes: AffinityOutcomeRow[]
  series: AffinityBucket[]
  breakdowns: AffinityBreakdowns
}

const maximumRangeMs = 90 * 86400000
const hourlyRangeMs = 72 * 3600000
const hourMs = 3600000
const dayMs = 86400000
const breakdownLimit = 50
const unsignedMoney = /^(?:0|[1-9]\d*)$/
const signedMoney = /^(?:0|-?[1-9]\d*)$/

function money(value: unknown, signed = false): string {
  const amount = text(value)
  if (!(signed ? signedMoney : unsignedMoney).test(amount)) throw new InvalidResponseError()
  return amount
}
function aggregate(row: Record<string, unknown>): AffinityAggregate {
  const values = Object.fromEntries(counts.map((key) => [key, integer(row[key])])) as Record<
    AffinityCount,
    number
  >
  const result: AffinityAggregate = {
    ...values,
    estimated_cost_nano_usd: money(row.estimated_cost_nano_usd),
    cache_read_savings_nano_usd: money(row.cache_read_savings_nano_usd, true),
    cache_write_premium_nano_usd: money(row.cache_write_premium_nano_usd, true),
  }
  if (
    result.success_count > result.request_count ||
    result.pinned_count > result.request_count ||
    result.hit_count > result.pinned_count ||
    result.miss_count !== result.pinned_count - result.hit_count
  )
    throw new InvalidResponseError()
  return result
}
function outcomeRow(value: unknown): AffinityOutcomeRow {
  const row = record(value)
  const outcome = oneOf(row.outcome, affinityOutcomes)
  const result = aggregate(row)
  const requests = result.request_count
  // 每个结果只包含该结果自身的请求，因此绑定、命中、未命中计数只能是 0 或全部。
  if (
    requests === 0 ||
    result.pinned_count !== (pinnedAffinityOutcomes.includes(outcome) ? requests : 0) ||
    result.hit_count !== (hitAffinityOutcomes.includes(outcome) ? requests : 0) ||
    result.miss_count !== (missAffinityOutcomes.includes(outcome) ? requests : 0)
  )
    throw new InvalidResponseError()
  return { ...result, outcome }
}
function sumsMatch(rows: AffinityAggregate[], summary: AffinityAggregate): boolean {
  return (
    counts.every((key) => rows.reduce((sum, row) => sum + row[key], 0) === summary[key]) &&
    moneyFields.every(
      (key) => rows.reduce((sum, row) => sum + BigInt(row[key]), 0n) === BigInt(summary[key]),
    )
  )
}
function ranking<T extends AffinityAggregate>(
  value: unknown,
  parse: (row: Record<string, unknown>) => T,
  identity: (item: T) => string,
  order: (a: T, b: T) => number,
): T[] {
  const items = list(value).map((entry) => parse(record(entry)))
  if (items.length > breakdownLimit) throw new InvalidResponseError()
  if (new Set(items.map(identity)).size !== items.length) throw new InvalidResponseError()
  for (let index = 1; index < items.length; index++) {
    const previous = items[index - 1]!,
      current = items[index]!
    if (
      previous.request_count < current.request_count ||
      (previous.request_count === current.request_count && order(previous, current) >= 0)
    )
      throw new InvalidResponseError()
  }
  return items
}

export async function getCacheAffinity(
  client: ApiClient,
  filters: AffinityFilters & { from_ms: string; to_ms: string },
  signal: AbortSignal,
): Promise<CacheAffinityReport> {
  const params = new URLSearchParams({ from_ms: filters.from_ms, to_ms: filters.to_ms })
  for (const key of affinityFilterKeys) if (filters[key]) params.set(key, filters[key]!)
  const row = record(await client.request(`/api/modern/cache-affinity?${params}`, { signal }))
  const from = integer(row.from_ms),
    to = integer(row.to_ms)
  const width = integer(row.bucket_width_ms, 1)
  if (
    from !== Math.floor(Number(filters.from_ms) / hourMs) * hourMs ||
    to !== Number(filters.to_ms) ||
    to <= from ||
    to - from > maximumRangeMs ||
    width !== (to - from <= hourlyRangeMs ? hourMs : dayMs)
  )
    throw new InvalidResponseError()
  const summary = aggregate(record(row.summary))
  let previousEnd = from
  const series = list(row.series).map((value) => {
    const point = record(value)
    const start = integer(point.bucket_start_ms),
      end = integer(point.bucket_end_ms)
    if (
      start < previousEnd ||
      end <= start ||
      end > to ||
      start !== Math.max(from, Math.floor(start / width) * width)
    )
      throw new InvalidResponseError()
    previousEnd = end
    return { ...aggregate(point), bucket_start_ms: start, bucket_end_ms: end }
  })
  let lastOutcome = -1
  const outcomes = list(row.outcomes).map((value) => {
    const item = outcomeRow(value)
    const index = affinityOutcomes.indexOf(item.outcome)
    if (index <= lastOutcome) throw new InvalidResponseError()
    lastOutcome = index
    return item
  })
  if (!sumsMatch(outcomes, summary)) throw new InvalidResponseError()
  const source = record(row.breakdowns)
  return {
    from_ms: from,
    to_ms: to,
    observed_at_ms: integer(row.observed_at_ms),
    bucket_width_ms: width,
    summary,
    outcomes,
    series,
    breakdowns: {
      credential: ranking(
        source.credential,
        (entry) => ({
          ...aggregate(entry),
          credential_id: integer(entry.credential_id, 1),
          group_id: integer(entry.group_id, 1),
        }),
        (item) => String(item.credential_id),
        (a, b) => a.credential_id - b.credential_id,
      ),
      group: ranking(
        source.group,
        (entry) => ({ ...aggregate(entry), group_id: integer(entry.group_id, 1) }),
        (item) => String(item.group_id),
        (a, b) => a.group_id - b.group_id,
      ),
      access_key: ranking(
        source.access_key,
        (entry) => ({ ...aggregate(entry), access_key_id: integer(entry.access_key_id, 1) }),
        (item) => String(item.access_key_id),
        (a, b) => a.access_key_id - b.access_key_id,
      ),
      model: ranking(
        source.model,
        (entry) => ({ ...aggregate(entry), model: text(entry.model) }),
        (item) => item.model,
        (a, b) => (a.model < b.model ? -1 : a.model > b.model ? 1 : 0),
      ),
    },
  }
}
