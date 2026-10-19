<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import type { AffinityAggregate, AffinityOutcomeRow } from '@modern/api/cache-affinity'
import { AppTooltip } from '@modern/components/ui'
import { formatCompactNumber } from '@modern/components/ui/format'
import type { TimeRangeState } from '@modern/app/time-range'
import {
  averageWriteTokens,
  cacheReadShare,
  money,
  netSaving,
  outcomeKey,
  percentage,
  shareOf,
} from './cache-affinity-display'
import {
  affinityLogsRoute,
  type AffinityLogsRoute,
  type CacheAffinityUrlFilters,
} from './cache-affinity-state'

const props = defineProps<{
  outcomes: AffinityOutcomeRow[]
  summary: AffinityAggregate
  filters: CacheAffinityUrlFilters
  range: TimeRangeState
}>()
const { t, locale } = useI18n()
const compact = (value: number) => formatCompactNumber(value, locale.value)
// '' 是接入功能前的记录，无法按结果筛选，因此不生成日志链接。
function logsRoute(row: AffinityOutcomeRow): AffinityLogsRoute | undefined {
  return row.outcome === '' ? undefined : affinityLogsRoute(props.filters, props.range, row.outcome)
}
</script>

<template>
  <div class="modern-cache-outcomes">
    <div class="modern-cache-outcome-bar" aria-hidden="true">
      <span
        v-for="row in outcomes"
        :key="row.outcome"
        :data-outcome="row.outcome || 'unrecorded'"
        :style="{ width: shareOf(row.request_count, summary.request_count) + '%' }"
      />
    </div>
    <div class="modern-cache-outcome-scroll">
      <table class="modern-cache-outcome-table" :aria-label="t('cacheAffinity.outcomesTitle')">
        <colgroup>
          <col class="modern-cache-outcome-name-column" />
          <col class="modern-cache-outcome-number-column" />
          <col class="modern-cache-outcome-number-column" />
          <col class="modern-cache-outcome-number-column" />
          <col class="modern-cache-outcome-number-column" />
          <col class="modern-cache-outcome-number-column" />
        </colgroup>
        <thead>
          <tr>
            <th scope="col">{{ t('cacheAffinity.outcomeColumns.outcome') }}</th>
            <th scope="col">{{ t('cacheAffinity.outcomeColumns.requests') }}</th>
            <th scope="col">{{ t('cacheAffinity.outcomeColumns.share') }}</th>
            <th scope="col">{{ t('cacheAffinity.outcomeColumns.cacheRead') }}</th>
            <th scope="col">{{ t('cacheAffinity.outcomeColumns.cacheWrite') }}</th>
            <th scope="col">{{ t('cacheAffinity.outcomeColumns.netSaving') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in outcomes"
            :key="row.outcome"
            :data-outcome="row.outcome || 'unrecorded'"
          >
            <th scope="row" class="modern-cache-outcome-name">
              <AppTooltip
                :label="t('cacheAffinity.outcomeHints.' + outcomeKey(row.outcome))"
                touchable
              >
                <span class="modern-cache-outcome-chip" aria-hidden="true" />
              </AppTooltip>
              <RouterLink
                v-if="logsRoute(row)"
                class="modern-cache-outcome-link"
                :to="logsRoute(row)!"
                >{{ t('cacheAffinity.outcomes.' + outcomeKey(row.outcome)) }}</RouterLink
              >
              <span v-else>{{ t('cacheAffinity.outcomes.' + outcomeKey(row.outcome)) }}</span>
            </th>
            <td class="modern-cache-number">{{ compact(row.request_count) }}</td>
            <td class="modern-cache-number">
              {{ percentage(shareOf(row.request_count, summary.request_count), locale) }}
            </td>
            <td class="modern-cache-number">{{ percentage(cacheReadShare(row), locale) }}</td>
            <td class="modern-cache-number">
              {{ averageWriteTokens(row) === null ? '—' : compact(averageWriteTokens(row)!) }}
            </td>
            <td class="modern-cache-number">{{ money(netSaving(row), locale) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.modern-cache-outcomes {
  display: grid;
  gap: var(--modern-space-4);
  min-width: 0;
}
.modern-cache-outcome-bar {
  display: flex;
  height: var(--modern-space-3);
  overflow: hidden;
  border-radius: var(--modern-radius-small);
  background: var(--modern-progress-track);
}
.modern-cache-outcome-bar span {
  display: block;
  height: 100%;
  min-width: 0;
  background: var(--modern-outcome-tone, var(--modern-muted));
}
.modern-cache-outcome-scroll {
  overflow-x: auto;
}
.modern-cache-outcome-table {
  width: 100%;
  min-width: 560px;
  border-collapse: collapse;
  text-align: left;
  font-size: var(--modern-font-size-secondary);
}
.modern-cache-outcome-name-column {
  width: 30%;
}
.modern-cache-outcome-number-column {
  width: 14%;
}
.modern-cache-outcome-table th {
  padding-bottom: var(--modern-space-2);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  font-weight: var(--modern-weight-regular);
}
.modern-cache-outcome-table td,
.modern-cache-outcome-table tbody th {
  padding-block: var(--modern-space-2);
  border-top: var(--modern-line-width) solid
    color-mix(in srgb, var(--modern-border) 65%, var(--modern-surface));
  vertical-align: middle;
}
.modern-cache-outcome-table tbody tr:hover {
  background: var(--modern-control-hover);
}
.modern-cache-outcome-name {
  display: flex;
  align-items: center;
  gap: var(--modern-space-2);
  min-width: 0;
  font-weight: var(--modern-weight-regular);
}
.modern-cache-outcome-chip {
  display: block;
  width: var(--modern-space-2);
  height: var(--modern-space-2);
  flex: none;
  border-radius: var(--modern-radius-round);
  background: var(--modern-outcome-tone, var(--modern-muted));
}
.modern-cache-outcome-link {
  min-width: 0;
  color: var(--modern-text);
  overflow-wrap: anywhere;
  text-decoration: underline;
  text-decoration-color: var(--modern-border);
  text-underline-offset: var(--modern-space-1);
}
.modern-cache-outcome-link:hover {
  color: var(--modern-accent);
}
.modern-cache-number {
  color: var(--modern-text);
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
}
.modern-cache-outcome-table [data-outcome='hit'] {
  --modern-outcome-tone: var(--modern-chart-cache);
}
.modern-cache-outcome-table [data-outcome='continuation'] {
  --modern-outcome-tone: var(--modern-accent);
}
.modern-cache-outcome-table [data-outcome='miss_unavailable'] {
  --modern-outcome-tone: var(--modern-warning);
}
.modern-cache-outcome-table [data-outcome='miss_excluded'] {
  --modern-outcome-tone: var(--modern-chart-output);
}
.modern-cache-outcome-table [data-outcome='miss_reauthorized'] {
  --modern-outcome-tone: var(--modern-danger);
}
.modern-cache-outcome-table [data-outcome='expired'] {
  --modern-outcome-tone: var(--modern-chart-write);
}
.modern-cache-outcome-table [data-outcome='new'] {
  --modern-outcome-tone: var(--modern-chart-input);
}
.modern-cache-outcome-table [data-outcome='not_applicable'],
.modern-cache-outcome-table [data-outcome='unrecorded'] {
  --modern-outcome-tone: var(--modern-status-neutral);
}
.modern-cache-outcome-bar [data-outcome='hit'] {
  --modern-outcome-tone: var(--modern-chart-cache);
}
.modern-cache-outcome-bar [data-outcome='continuation'] {
  --modern-outcome-tone: var(--modern-accent);
}
.modern-cache-outcome-bar [data-outcome='miss_unavailable'] {
  --modern-outcome-tone: var(--modern-warning);
}
.modern-cache-outcome-bar [data-outcome='miss_excluded'] {
  --modern-outcome-tone: var(--modern-chart-output);
}
.modern-cache-outcome-bar [data-outcome='miss_reauthorized'] {
  --modern-outcome-tone: var(--modern-danger);
}
.modern-cache-outcome-bar [data-outcome='expired'] {
  --modern-outcome-tone: var(--modern-chart-write);
}
.modern-cache-outcome-bar [data-outcome='new'] {
  --modern-outcome-tone: var(--modern-chart-input);
}
.modern-cache-outcome-bar [data-outcome='not_applicable'],
.modern-cache-outcome-bar [data-outcome='unrecorded'] {
  --modern-outcome-tone: var(--modern-status-neutral);
}
</style>
