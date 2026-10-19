<script setup lang="ts">
import { SlidersHorizontal } from '@lucide/vue'
import { keepPreviousData, useQuery } from '@tanstack/vue-query'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getCacheAffinity } from '@modern/api/cache-affinity'
import { getGroupWorkspace, groupQueryKey } from '@modern/api/groups'
import { getGroupCredentials } from '@modern/api/group-detail'
import { getLogAccessKeys } from '@modern/api/logs'
import { useMessageSource } from '@modern/app/messages'
import { usePageRefresh } from '@modern/app/page-refresh'
import { resolveTimeRange } from '@modern/app/time-range'
import { useMobileLayout } from '@modern/app/use-mobile-layout'
import { useURLState } from '@modern/app/url-state'
import {
  AppButton,
  AppCollectionState,
  AppDateTimeRangePicker,
  AppFilterSheet,
  AppFilterSummary,
  AppIconButton,
  AppModelSelect,
  AppPanel,
  AppSearchSelect,
} from '@modern/components/ui'
import type { SearchSelectOption } from '@modern/components/ui'
import {
  formatLocalDateTime,
  parseLocalDateTime,
  type DateRangePreset,
} from '@modern/components/ui/date-time'
import { dateFormatter } from '@modern/components/ui/intl-formatters'
import { useApiClient } from '@shared/http/client-context'
import { validUsageFilter } from '@modern/features/usage/usage-state'
import {
  affinityApiFilters,
  cacheAffinityFilterKeys,
  cacheAffinityStateKeys,
  parseCacheAffinityState,
  serializeCacheAffinityState,
  type CacheAffinityFilterKey,
  type CacheAffinityUrlFilters,
} from './cache-affinity-state'
import CacheAffinityKpis from './CacheAffinityKpis.vue'
import CacheAffinityOutcomes from './CacheAffinityOutcomes.vue'
import CacheAffinityCost from './CacheAffinityCost.vue'
import CacheAffinityTrend from './CacheAffinityTrend.vue'
import CacheAffinityBreakdown from './CacheAffinityBreakdown.vue'

const maximumRangeMs = 90 * 86400000
const { t, locale } = useI18n()
const client = useApiClient()
const isMobile = useMobileLayout()
const mobileFilters = ref(false)
const state = useURLState(
  cacheAffinityStateKeys,
  parseCacheAffinityState,
  serializeCacheAffinityState,
)
const rangeTooLong = computed(() => {
  const range = resolveTimeRange(state.value.range)
  return Number(range.to_ms) - Number(range.from_ms) > maximumRangeMs
})
const query = useQuery(
  computed(() => {
    const filters = { ...state.value.filters },
      range = { ...state.value.range }
    return {
      queryKey: ['modern', 'cache-affinity', filters, range],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        getCacheAffinity(
          client,
          { ...affinityApiFilters(filters), ...resolveTimeRange(range) },
          signal,
        ),
      placeholderData: keepPreviousData,
      enabled: !rangeTooLong.value,
    }
  }),
)
const report = computed(() => query.data.value)
const groups = useQuery({
  queryKey: groupQueryKey,
  queryFn: ({ signal }) => getGroupWorkspace(client, signal),
})
const keys = useQuery({
  queryKey: ['modern', 'log-access-key-options'],
  queryFn: ({ signal }) => getLogAccessKeys(client, signal),
})
const groupMap = computed(
  () => new Map(groups.data.value?.items.map((row) => [String(row.id), row])),
)
const credentialLabels = ref(new Map<string, string>())
function label(key: CacheAffinityFilterKey, value: string): string {
  if (key === 'group_id')
    return groupMap.value.get(value)?.name ?? (groups.data.value ? t('logs.deleted') : '—')
  if (key === 'access_key_id')
    return (
      keys.data.value?.find((row) => String(row.id) === value)?.name ??
      (keys.data.value ? t('logs.deleted') : '—')
    )
  if (key === 'credential_id')
    return credentialLabels.value.get(value) ?? t('logs.selectedCredential')
  return value
}
function choices(
  key: 'group_id' | 'access_key_id',
  values: SearchSelectOption[],
  all: string,
): SearchSelectOption[] {
  const selected = state.value.filters[key]
  return [
    { value: '', label: all },
    ...values,
    ...(selected && !values.some((row) => row.value === selected)
      ? [{ value: selected, label: label(key, selected) }]
      : []),
  ]
}
const groupOptions = computed(() =>
  choices(
    'group_id',
    (groups.data.value?.items ?? []).map((row) => ({
      value: String(row.id),
      label: row.name,
      keywords: [row.channelID, row.channelName],
    })),
    t('logs.allGroups'),
  ),
)
const keyOptions = computed(() =>
  choices(
    'access_key_id',
    (keys.data.value ?? []).map((row) => ({
      value: String(row.id),
      label: row.name,
      keywords: [row.suffix],
    })),
    t('logs.allAccessKeys'),
  ),
)
const models = computed(() =>
  (report.value?.breakdowns.model ?? []).map((row) => row.model).filter(Boolean),
)
const loadCredentials = computed(() => {
  const group = Number(state.value.filters.group_id)
  return async (q: string, signal: AbortSignal) => {
    if (!group) return [{ value: '', label: t('logs.all') }]
    const page = await getGroupCredentials(
      client,
      group,
      { q, page: 1, pageSize: 100, sort: 'name', status: '', proxy: '', reset: '' },
      signal,
    )
    const values = page.items.map((row) => ({
      value: String(row.id),
      label: row.label,
    }))
    credentialLabels.value = new Map([
      ...credentialLabels.value,
      ...values.map((row): [string, string] => [row.value, row.label]),
    ])
    return [{ value: '', label: t('logs.all') }, ...values]
  }
})
const from = ref(''),
  to = ref(''),
  preset = ref<DateRangePreset>(),
  model = ref('')
let modelTimer: number | undefined
watch(
  () => state.value.filters.upstream_model,
  (value) => {
    window.clearTimeout(modelTimer)
    model.value = value ?? ''
  },
  { immediate: true },
)
watch(
  () => state.value.range,
  () => {
    const range = resolveTimeRange(state.value.range)
    from.value = formatLocalDateTime(Number(range.from_ms))
    to.value = formatLocalDateTime(Number(range.to_ms))
    preset.value = state.value.range.preset
  },
  { immediate: true },
)
onScopeDispose(() => window.clearTimeout(modelTimer))
const modelError = computed(() =>
  model.value && !validUsageFilter('upstream_model', model.value.trim())
    ? t('logs.errors.invalidText')
    : undefined,
)
function setFilter(key: CacheAffinityFilterKey, value: string): void {
  const filters: CacheAffinityUrlFilters = { ...state.value.filters }
  if (value) filters[key] = value
  else delete filters[key]
  if (key === 'group_id') delete filters.credential_id
  state.value = { ...state.value, filters }
}
function setModel(value: string): void {
  model.value = value
  window.clearTimeout(modelTimer)
  if (!modelError.value)
    modelTimer = window.setTimeout(() => setFilter('upstream_model', value.trim()), 200)
}
function applyDate(): void {
  const start = parseLocalDateTime(from.value),
    end = parseLocalDateTime(to.value)
  if (!start || !end || start >= end) return
  const range = preset.value
    ? { preset: preset.value }
    : { from_ms: String(start.getTime()), to_ms: String(end.getTime()) }
  if (JSON.stringify(range) === JSON.stringify(state.value.range)) {
    // 再次点击当前快捷日期，也要按当前时间重新查询。
    void query.refetch()
  } else {
    state.value = { ...state.value, range }
  }
}
function resetMobileFilters(): void {
  window.clearTimeout(modelTimer)
  model.value = ''
  state.value = { ...state.value, filters: {} }
}
function reset(key?: string): void {
  if (!key) {
    state.value = { ...state.value, filters: {}, range: { preset: '24h' } }
    return
  }
  if (key === 'time') state.value = { ...state.value, range: { preset: '24h' } }
  else setFilter(key as CacheAffinityFilterKey, '')
}
const filterSummary = computed(() => [
  ...(state.value.range.preset !== '24h'
    ? [
        {
          key: 'time',
          label: t('logs.timeRange'),
          value: state.value.range.preset
            ? t('ui.date.ranges.' + state.value.range.preset)
            : `${from.value} – ${to.value}`,
        },
      ]
    : []),
  ...cacheAffinityFilterKeys.flatMap((key) =>
    state.value.filters[key]
      ? [
          {
            key,
            label: t('cacheAffinity.filters.' + key),
            value: label(key, state.value.filters[key]!),
          },
        ]
      : [],
  ),
])
// 自动刷新只更新报告，保留筛选和滚动位置，不刷新筛选候选项。
const requestIdentity = computed(() => JSON.stringify([state.value.filters, state.value.range]))
const silentRequest = ref<string>()
const fetching = computed(
  () => query.isFetching.value && silentRequest.value !== requestIdentity.value,
)
async function autoRefresh(): Promise<boolean> {
  const identity = requestIdentity.value
  silentRequest.value = identity
  try {
    return !(await query.refetch({ cancelRefetch: false })).isError
  } finally {
    if (silentRequest.value === identity) silentRequest.value = undefined
  }
}
async function refresh(): Promise<void> {
  await Promise.all([query.refetch(), groups.refetch(), keys.refetch()])
}
usePageRefresh({
  refresh,
  pending: () => fetching.value,
  updatedAt: () => report.value?.observed_at_ms,
  autoRefresh,
})
useMessageSource(() =>
  query.isError.value && report.value
    ? { tone: 'warning', text: t('logs.stale'), action: { label: t('ui.retry'), run: refresh } }
    : undefined,
)
useMessageSource(() =>
  groups.isError.value || keys.isError.value
    ? {
        tone: 'warning',
        text: t('logs.catalogFailed'),
        action: { label: t('ui.retry'), run: refresh },
      }
    : undefined,
)
const period = computed(() => {
  if (!report.value) return ''
  const format = dateFormatter(locale.value, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  })
  return `${format.format(report.value.from_ms)} – ${format.format(report.value.to_ms)}`
})
const bucketLabel = computed(() => {
  const minutes = (report.value?.bucket_width_ms ?? 0) / 60000
  return minutes >= 1440
    ? t('usage.bucketDays', { count: minutes / 1440 })
    : minutes >= 60
      ? t('usage.bucketHours', { count: minutes / 60 })
      : t('usage.bucket', { minutes })
})
</script>

<template>
  <div class="modern-cache-affinity-workspace">
    <form
      class="modern-cache-affinity-filters"
      role="search"
      :aria-label="t('cacheAffinity.search')"
      @submit.prevent
    >
      <AppDateTimeRangePicker
        v-model:from="from"
        v-model:to="to"
        :mobile="isMobile"
        :preset="preset"
        :label="t('logs.timeRange')"
        size="md"
        label-hidden
        class="modern-cache-affinity-date"
        @update:preset="preset = $event"
        @apply="applyDate"
      />
      <AppIconButton
        v-if="isMobile"
        :icon="SlidersHorizontal"
        :variant="cacheAffinityFilterKeys.some((key) => state.filters[key]) ? 'brand' : 'ghost'"
        :label="t('ui.mobile.filters')"
        @click="mobileFilters = true"
      />
      <AppFilterSheet
        v-model:open="mobileFilters"
        :mobile="isMobile"
        :title="t('ui.mobile.filters')"
        :active-count="cacheAffinityFilterKeys.filter((key) => state.filters[key]).length"
        @reset="resetMobileFilters"
      >
        <AppSearchSelect
          :model-value="state.filters.group_id ?? ''"
          :options="groupOptions"
          :label="t('cacheAffinity.filters.group_id')"
          :label-hidden="!isMobile"
          :loading="groups.isFetching.value"
          class="modern-cache-affinity-choice"
          @update:model-value="setFilter('group_id', $event)"
        />
        <AppSearchSelect
          :key="state.filters.group_id || 'all'"
          :model-value="state.filters.credential_id ?? ''"
          :options="[{ value: '', label: t('usage.allCredentials') }]"
          :load-options="loadCredentials"
          :selected-option="
            state.filters.credential_id
              ? {
                  value: state.filters.credential_id,
                  label: label('credential_id', state.filters.credential_id),
                }
              : undefined
          "
          :label="t('cacheAffinity.filters.credential_id')"
          :label-hidden="!isMobile"
          :placeholder="t(state.filters.group_id ? 'logs.all' : 'logs.selectGroupFirst')"
          :disabled="!state.filters.group_id"
          class="modern-cache-affinity-choice"
          @update:model-value="setFilter('credential_id', $event)"
        />
        <AppSearchSelect
          :model-value="state.filters.access_key_id ?? ''"
          :options="keyOptions"
          :label="t('cacheAffinity.filters.access_key_id')"
          :label-hidden="!isMobile"
          :loading="keys.isFetching.value"
          class="modern-cache-affinity-choice"
          @update:model-value="setFilter('access_key_id', $event)"
        />
        <AppModelSelect
          :model-value="model"
          :models="models"
          :label="t('cacheAffinity.filters.upstream_model')"
          :error="modelError"
          :label-hidden="!isMobile"
          fuzzy
          class="modern-cache-affinity-model"
          @update:model-value="setModel"
        />
      </AppFilterSheet>
    </form>
    <div class="modern-cache-affinity-filterbar">
      <AppFilterSummary :items="filterSummary" @remove="reset" @reset="reset()" />
    </div>
    <AppCollectionState
      v-if="rangeTooLong"
      :title="t('cacheAffinity.rangeLimit')"
      :description="t('cacheAffinity.rangeLimitHint')"
    />
    <AppCollectionState
      v-else-if="!report"
      :loading="query.isPending.value"
      :error="query.isError.value"
      :title="t(query.isError.value ? 'cacheAffinity.failed' : 'cacheAffinity.loading')"
    >
      <AppButton v-if="query.isError.value" @click="refresh">{{ t('ui.retry') }}</AppButton>
    </AppCollectionState>
    <div
      v-else
      class="modern-cache-affinity-report"
      :aria-busy="query.isFetching.value"
      :class="{ 'is-updating': query.isPlaceholderData.value }"
    >
      <AppCollectionState
        v-if="report.summary.request_count === 0"
        :title="t('cacheAffinity.empty')"
        :description="t('cacheAffinity.emptyHint')"
      />
      <template v-else>
        <CacheAffinityKpis
          :summary="report.summary"
          :filters="state.filters"
          :range="state.range"
        />
        <div class="modern-cache-affinity-grid">
          <AppPanel
            :title="t('cacheAffinity.outcomesTitle')"
            compact
            class="modern-cache-affinity-outcomes"
          >
            <CacheAffinityOutcomes
              :outcomes="report.outcomes"
              :summary="report.summary"
              :filters="state.filters"
              :range="state.range"
            />
          </AppPanel>
          <AppPanel
            :title="t('cacheAffinity.cost.title')"
            compact
            class="modern-cache-affinity-cost"
          >
            <CacheAffinityCost :summary="report.summary" />
          </AppPanel>
        </div>
        <AppPanel :title="t('cacheAffinity.trend.title')" compact>
          <div class="modern-cache-affinity-chart-context">
            <span>{{ period }}</span>
            <span>{{ bucketLabel }}</span>
          </div>
          <CacheAffinityTrend :report="report" />
        </AppPanel>
        <AppPanel :title="t('cacheAffinity.breakdown.title')" compact>
          <CacheAffinityBreakdown :breakdowns="report.breakdowns" />
        </AppPanel>
      </template>
    </div>
  </div>
</template>

<style scoped>
.modern-cache-affinity-workspace {
  display: flex;
  flex: 1;
  min-height: 0;
  min-width: 0;
  flex-direction: column;
}
.modern-cache-affinity-filters {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--modern-space-3);
  padding: var(--modern-space-5) 0 var(--modern-space-3);
}
.modern-cache-affinity-date {
  flex: 2 1 240px;
  min-width: 0;
}
.modern-cache-affinity-model {
  flex: 1.3 1 200px;
  min-width: 0;
}
.modern-cache-affinity-choice {
  flex: 1 1 180px;
  min-width: 0;
}
.modern-cache-affinity-choice + .modern-cache-affinity-choice {
  flex-basis: 144px;
}
.modern-cache-affinity-filterbar {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--modern-space-3);
  border-bottom: var(--modern-line-width) solid var(--modern-border);
  background: var(--modern-canvas);
  padding-bottom: var(--modern-space-3);
}
.modern-cache-affinity-report {
  display: grid;
  flex: 1;
  align-content: start;
  gap: var(--modern-page-gap);
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding-block: var(--modern-space-4) var(--modern-space-6);
  scrollbar-gutter: var(--modern-scrollbar-gutter);
}
.modern-cache-affinity-report.is-updating {
  opacity: var(--modern-opacity-quiet);
}
.modern-cache-affinity-grid {
  display: grid;
  grid-template-columns: minmax(0, 2fr) minmax(300px, 1fr);
  gap: var(--modern-space-4);
  align-items: start;
}
.modern-cache-affinity-outcomes,
.modern-cache-affinity-cost {
  min-width: 0;
}
.modern-cache-affinity-chart-context {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: var(--modern-space-2);
  margin-bottom: var(--modern-space-3);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  font-variant-numeric: tabular-nums;
}
@media (max-width: 1150px) {
  .modern-cache-affinity-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
@media (max-width: 760px) {
  .modern-cache-affinity-workspace {
    flex: none;
  }
  .modern-cache-affinity-report {
    flex: none;
    overflow: visible;
  }
  .modern-cache-affinity-filters {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--modern-space-2);
    padding-top: var(--modern-space-3);
  }
  .modern-cache-affinity-date,
  .modern-cache-affinity-model {
    grid-column: 1 / -1;
  }
}
@media (max-width: 760px) {
  .modern-cache-affinity-filters {
    grid-template-columns: minmax(0, 1fr) auto;
  }
  .modern-cache-affinity-date {
    grid-column: auto;
  }
}
</style>
