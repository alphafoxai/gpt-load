import type { AffinityAggregate, AffinityOutcome } from '@modern/api/cache-affinity'
import { formatNanoUSD } from '@modern/components/ui/format'
import { percentage } from '@modern/features/usage/usage-display'

export { percentage }

// 结果的文案键：'' 表示接入粘性记录前写入的请求，显示为“未记录”。
export function outcomeKey(outcome: AffinityOutcome): Exclude<AffinityOutcome, ''> | 'unrecorded' {
  return outcome === '' ? 'unrecorded' : outcome
}
// 缓存写入 Token（5 分钟 + 1 小时 + 未知）。
export function writeTokens(row: AffinityAggregate): number {
  return row.cache_write_5m_tokens + row.cache_write_1h_tokens + row.cache_write_unknown_tokens
}
// 输入 Token 总量：未缓存输入 + 缓存读取 + 全部缓存写入。
export function totalInputTokens(row: AffinityAggregate): number {
  return row.uncached_input_tokens + row.cache_read_tokens + writeTokens(row)
}
export function cacheReadShare(row: AffinityAggregate): number | null {
  const total = totalInputTokens(row)
  return total ? (row.cache_read_tokens / total) * 100 : null
}
export function stickyRate(row: AffinityAggregate): number | null {
  return row.pinned_count ? (row.hit_count / row.pinned_count) * 100 : null
}
export function shareOf(part: number, whole: number): number | null {
  return whole ? (part / whole) * 100 : null
}
export function averageWriteTokens(row: AffinityAggregate): number | null {
  return row.request_count ? writeTokens(row) / row.request_count : null
}
// 净缓存节省 = 缓存读取节省 − 缓存写入溢价；两者都可能为负（价格表可能设置读取价高于输入价）。
export function netSaving(row: AffinityAggregate): bigint {
  return BigInt(row.cache_read_savings_nano_usd) - BigInt(row.cache_write_premium_nano_usd)
}
// 无缓存估算费用 = 估算费用 + 净缓存节省。
export function costWithoutCache(row: AffinityAggregate): bigint {
  return BigInt(row.estimated_cost_nano_usd) + netSaving(row)
}
export function money(value: bigint | string, locale: string): string {
  return formatNanoUSD(String(value), locale, 'narrowSymbol', 2)
}
