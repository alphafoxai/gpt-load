// Official Mirasim schema 2 metrics, not GPT-Load request statistics.
export type NullableMetric = number | null
export type StatusTimestamp = string | number

export interface MirasimMetrics {
  now: {
    status: string
    availability: NullableMetric
    window: string
  }
  availability: { h24: NullableMetric; d7: NullableMetric }
  latency: { p50: NullableMetric; p95: NullableMetric }
  sameModel: NullableMetric
  intel: {
    full: NullableMetric
    swapped: NullableMetric
    mismatched: NullableMetric
    cut: NullableMetric
  } | null
  // Encoded in tenths of a percent: 1000 = 100%. Null is missing, not zero.
  cells: NullableMetric[]
  merged: unknown[] | null
}

export interface MirasimModel extends MirasimMetrics {
  id: string
  name: string
}

export interface MirasimAgent {
  id: string
  name: string
  summary: MirasimMetrics
  reasons: { class: string; share: NullableMetric }[] | null
  models: MirasimModel[]
}

export interface MirasimSnapshot {
  schema: 2
  generatedAt: StatusTimestamp
  dataThrough: StatusTimestamp
  cellSeconds: number
  cellsStart: StatusTimestamp
  thresholds: { good: number; warn: number; minTurns: number }
  notes: unknown[]
  cohorts: { id: string; state: string; agents: MirasimAgent[] }[]
}

// ApiClient.request already unwraps the standard envelope.data.
export interface MirasimStatusResponse {
  source_url: string
  checked_at: StatusTimestamp
  fetched_at: StatusTimestamp | null
  stale: boolean
  error?: string
  data?: MirasimSnapshot | null
}

export const MIRASIM_SOURCE_URL = 'https://mirasim.ai/zh/status#paid'
