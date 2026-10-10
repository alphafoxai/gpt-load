import { computed, onMounted, onScopeDispose, ref, shallowRef } from 'vue'

import { useApiClient } from '@shared/http/client-context'
import type { MirasimStatusResponse } from './types'

export function useMirasimStatus() {
  const api = useApiClient()
  const response = shallowRef<MirasimStatusResponse>()
  const isFetching = ref(false)
  const failed = ref(false)
  const error = ref('')
  const stale = computed(() =>
    Boolean(response.value?.data && (failed.value || response.value.stale)),
  )
  let interval: ReturnType<typeof setInterval> | undefined
  let controller: AbortController | undefined
  let disposed = false

  async function refresh(): Promise<void> {
    if (isFetching.value || disposed) return
    isFetching.value = true
    controller = new AbortController()
    const timeout = setTimeout(() => controller?.abort(), 20_000)
    try {
      const next = await api.request<MirasimStatusResponse>('/api/mirasim/status', {
        signal: controller.signal,
      })
      if (disposed) return
      if (next.data && (next.data.schema !== 2 || !Array.isArray(next.data.cohorts))) {
        throw new Error('Unsupported Mirasim status schema')
      }
      failed.value = !next.data || next.stale || Boolean(next.error)
      error.value = next.error ?? ''
      // Preserve the last usable snapshot even if a later response has no data.
      response.value = {
        ...next,
        data: next.data ?? response.value?.data,
        fetched_at: next.data ? next.fetched_at : (response.value?.fetched_at ?? null),
      }
    } catch (cause) {
      if (disposed) return
      failed.value = true
      error.value = cause instanceof Error ? cause.message : ''
    } finally {
      clearTimeout(timeout)
      if (!disposed) isFetching.value = false
    }
  }

  onMounted(() => {
    void refresh()
    // Explicitly required for this external status board, independent of local queries.
    interval = setInterval(() => void refresh(), 60_000)
  })
  onScopeDispose(() => {
    disposed = true
    clearInterval(interval)
    controller?.abort()
  })

  return { response, isFetching, failed, error, stale, refresh }
}
