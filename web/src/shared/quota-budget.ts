import { InvalidResponseError } from '@shared/http/errors'

export interface QuotaBudget {
  enabled: boolean
  five_hour_percent: number
  seven_day_percent: number
  observation_max_age_seconds: number
}
export const quotaBudgetNumbers = {
  five_hour_percent: { min: 1, max: 100 },
  seven_day_percent: { min: 1, max: 100 },
  observation_max_age_seconds: { min: 1, max: 86400 },
} as const
export type QuotaBudgetNumber = keyof typeof quotaBudgetNumbers
export function validQuotaBudget(value: QuotaBudget): boolean {
  return (
    typeof value.enabled === 'boolean' &&
    Object.entries(quotaBudgetNumbers).every(([key, rule]) => {
      const number = value[key as QuotaBudgetNumber]
      return Number.isSafeInteger(number) && number >= rule.min && number <= rule.max
    })
  )
}
export function readQuotaBudget(value: unknown): QuotaBudget {
  if (value === null || typeof value !== 'object' || Array.isArray(value))
    throw new InvalidResponseError()
  const row = value as Record<string, unknown>
  const keys = ['enabled', ...Object.keys(quotaBudgetNumbers)]
  if (
    Object.keys(row).length !== keys.length ||
    Object.keys(row).some((key) => !keys.includes(key))
  )
    throw new InvalidResponseError()
  const result = row as unknown as QuotaBudget
  if (!validQuotaBudget(result)) throw new InvalidResponseError()
  return {
    enabled: result.enabled,
    five_hour_percent: result.five_hour_percent,
    seven_day_percent: result.seven_day_percent,
    observation_max_age_seconds: result.observation_max_age_seconds,
  }
}
