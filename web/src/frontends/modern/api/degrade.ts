import type { ApiClient } from '@shared/http/client'
import { InvalidResponseError } from '@shared/http/errors'
import { integer, list, record, text } from './response'

export interface DegradeSchedule {
  enabled: boolean
  intervalMS: number
  model: string
  channelID: string
  nextRunAtMS: number
  lastRunAtMS: number
  running: boolean
}

export interface DegradeResult {
  id: string
  credentialID: number
  groupID: number
  model: string
  status: string
  degraded: boolean | null
  error: string
  startedAtMS: number
  durationMS: number
  inputTokens: number
  outputTokens: number
  prediction: string
  probability: number
  family: string
  familyProbability: number
}

export interface DegradeCredential {
  credentialID: number
  groupID: number
  groupName: string
  identity: string
  plan: string
  status: string
  weight: number
  running: boolean
  latest: DegradeResult | null
}

export interface DegradeBoard {
  schedule: DegradeSchedule
  credentials: DegradeCredential[]
  history: DegradeResult[]
}

function nullableBoolean(value: unknown): boolean | null {
  if (value == null) return null
  if (typeof value !== 'boolean') throw new InvalidResponseError()
  return value
}

function finite(value: unknown): number {
  const parsed = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(parsed)) throw new InvalidResponseError()
  return parsed
}

function result(value: unknown): DegradeResult {
  const row = record(value)
  return {
    id: text(row.id),
    credentialID: integer(row.credential_id),
    groupID: integer(row.group_id),
    model: text(row.model),
    status: text(row.status),
    degraded: nullableBoolean(row.degraded),
    error: typeof row.error === 'string' ? row.error : '',
    startedAtMS: integer(row.started_at_ms),
    durationMS: integer(row.duration_ms),
    inputTokens: integer(row.input_tokens ?? 0),
    outputTokens: integer(row.output_tokens ?? 0),
    prediction: typeof row.prediction === 'string' ? row.prediction : '',
    probability: finite(row.probability ?? 0),
    family: typeof row.family === 'string' ? row.family : '',
    familyProbability: finite(row.family_probability ?? 0),
  }
}

function schedule(value: unknown): DegradeSchedule {
  const row = record(value)
  return {
    enabled: row.enabled === true,
    intervalMS: integer(row.interval_ms),
    model: text(row.model),
    channelID: text(row.channel_id),
    nextRunAtMS: integer(row.next_run_at_ms ?? 0),
    lastRunAtMS: integer(row.last_run_at_ms ?? 0),
    running: row.running === true,
  }
}

export async function getDegradeBoard(
  client: ApiClient,
  signal?: AbortSignal,
): Promise<DegradeBoard> {
  const payload = record(await client.request('/api/degrade', { signal }))
  return {
    schedule: schedule(payload.schedule),
    credentials: list(payload.credentials).map((item) => {
      const row = record(item)
      return {
        credentialID: integer(row.credential_id),
        groupID: integer(row.group_id),
        groupName: text(row.group_name),
        identity: text(row.identity),
        plan: typeof row.plan === 'string' ? row.plan : '',
        status: text(row.status),
        weight: integer(row.weight),
        running: row.running === true,
        latest: row.latest == null ? null : result(row.latest),
      }
    }),
    history: list(payload.history).map(result),
  }
}

export function startDegradeRun(client: ApiClient, model: string, credentialIDs: number[]) {
  return client.request('/api/degrade/runs', {
    method: 'POST',
    json: { model, credential_ids: credentialIDs, all: credentialIDs.length === 0 },
  })
}

export function saveDegradeSchedule(
  client: ApiClient,
  patch: { enabled?: boolean; interval_ms?: number; model?: string },
) {
  return client.request('/api/degrade/schedule', { method: 'PUT', json: patch })
}
