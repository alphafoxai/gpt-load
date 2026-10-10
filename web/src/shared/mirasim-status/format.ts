import type { NullableMetric, StatusTimestamp } from './types'

export function finiteMetric(value: NullableMetric | undefined): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

export function percent(
  value: NullableMetric | undefined,
  locale: string,
  missing: string,
): string {
  return finiteMetric(value)
    ? `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(value)}%`
    : missing
}

export function timestampMs(value: StatusTimestamp | null | undefined): number | undefined {
  if (value === null || value === undefined || value === '') return undefined
  const result =
    typeof value === 'number' ? (value < 1e12 ? value * 1000 : value) : Date.parse(value)
  return Number.isFinite(result) ? result : undefined
}

export function historyCells(cells: readonly NullableMetric[] | undefined): NullableMetric[] {
  return Array.from({ length: 48 }, (_, index) => {
    const value = cells?.[index]
    return finiteMetric(value) && value >= 0 && value <= 1000 ? value / 10 : null
  })
}

export function metricTone(value: NullableMetric | undefined, good: number, warn: number): string {
  if (!finiteMetric(value)) return 'unknown'
  return value >= good ? 'good' : value >= warn ? 'warn' : 'low'
}
