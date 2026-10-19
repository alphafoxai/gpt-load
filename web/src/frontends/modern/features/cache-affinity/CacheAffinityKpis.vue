<script setup lang="ts">
import { Database, PiggyBank, Magnet, Repeat, CircleAlert } from '@lucide/vue'
import type { Component } from 'vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import type { AffinityAggregate } from '@modern/api/cache-affinity'
import { AppIcon, AppTooltip } from '@modern/components/ui'
import { formatCompactNumber } from '@modern/components/ui/format'
import type { TimeRangeState } from '@modern/app/time-range'
import {
  cacheReadShare,
  money,
  netSaving,
  percentage,
  shareOf,
  stickyRate,
} from './cache-affinity-display'
import {
  affinityLogsRoute,
  type AffinityLogsRoute,
  type CacheAffinityUrlFilters,
} from './cache-affinity-state'

const props = defineProps<{
  summary: AffinityAggregate
  filters: CacheAffinityUrlFilters
  range: TimeRangeState
}>()
const { t, locale } = useI18n()
const compact = (value: number) => formatCompactNumber(value, locale.value)
interface KpiCard {
  id: 'cacheRead' | 'netSaving' | 'sticky' | 'followUps' | 'misses'
  icon: Component
  value: string
  detail: string
  link?: AffinityLogsRoute
}
const cards = computed<KpiCard[]>(() => {
  const row = props.summary
  return [
    {
      id: 'cacheRead',
      icon: Database,
      value: percentage(cacheReadShare(row), locale.value),
      detail: t('cacheAffinity.kpiDetail.cacheRead', { count: compact(row.cache_read_tokens) }),
    },
    {
      id: 'netSaving',
      icon: PiggyBank,
      value: money(netSaving(row), locale.value),
      detail: t('cacheAffinity.kpiDetail.netSaving', {
        savings: money(row.cache_read_savings_nano_usd, locale.value),
        premium: money(row.cache_write_premium_nano_usd, locale.value),
      }),
    },
    {
      id: 'sticky',
      icon: Magnet,
      value: percentage(stickyRate(row), locale.value),
      detail: t('cacheAffinity.kpiDetail.sticky', {
        hits: compact(row.hit_count),
        pinned: compact(row.pinned_count),
      }),
    },
    {
      id: 'followUps',
      icon: Repeat,
      value: compact(row.pinned_count),
      detail: t('cacheAffinity.kpiDetail.followUps', {
        share: percentage(shareOf(row.pinned_count, row.request_count), locale.value),
      }),
      link: affinityLogsRoute(props.filters, props.range, 'pinned'),
    },
    {
      id: 'misses',
      icon: CircleAlert,
      value: compact(row.miss_count),
      detail: t('cacheAffinity.kpiDetail.misses', {
        share: percentage(shareOf(row.miss_count, row.pinned_count), locale.value),
      }),
      link: affinityLogsRoute(props.filters, props.range, 'miss'),
    },
  ]
})
</script>

<template>
  <div class="modern-cache-kpis">
    <article v-for="card in cards" :key="card.id" class="modern-cache-kpi" :data-kind="card.id">
      <div class="modern-cache-kpi-label">
        <span class="modern-cache-kpi-symbol"><AppIcon :icon="card.icon" size="sm" /></span>
        <AppTooltip :label="t('cacheAffinity.hints.' + card.id)" touchable>
          <span tabindex="0">{{ t('cacheAffinity.kpi.' + card.id) }}</span>
        </AppTooltip>
      </div>
      <strong class="modern-cache-kpi-value">{{ card.value }}</strong>
      <span class="modern-cache-kpi-detail">
        <RouterLink v-if="card.link" class="modern-cache-kpi-link" :to="card.link">{{
          card.detail
        }}</RouterLink>
        <template v-else>{{ card.detail }}</template>
      </span>
    </article>
  </div>
</template>

<style scoped>
.modern-cache-kpis {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(176px, 1fr));
  gap: var(--modern-space-3);
}
.modern-cache-kpi {
  --modern-cache-kpi-tone: var(--modern-accent);
  position: relative;
  isolation: isolate;
  display: flex;
  flex-direction: column;
  gap: var(--modern-space-3);
  min-width: 0;
  overflow: hidden;
  padding: var(--modern-space-4) var(--modern-space-5);
  border: var(--modern-line-width) solid var(--modern-border);
  border-radius: var(--modern-radius-panel);
  background: var(--modern-surface);
}
.modern-cache-kpi::before {
  position: absolute;
  inset: 0;
  z-index: var(--modern-layer-underlay);
  background: radial-gradient(
    ellipse at top right,
    color-mix(in srgb, var(--modern-cache-kpi-tone) 8%, var(--modern-surface)),
    transparent 75%
  );
  content: '';
  pointer-events: none;
}
.modern-cache-kpi[data-kind='cacheRead'],
.modern-cache-kpi[data-kind='sticky'] {
  --modern-cache-kpi-tone: var(--modern-chart-cache);
}
.modern-cache-kpi[data-kind='netSaving'] {
  --modern-cache-kpi-tone: var(--modern-chart-output);
}
.modern-cache-kpi[data-kind='followUps'] {
  --modern-cache-kpi-tone: var(--modern-chart-input);
}
.modern-cache-kpi[data-kind='misses'] {
  --modern-cache-kpi-tone: var(--modern-warning);
}
.modern-cache-kpi-label {
  display: flex;
  align-items: center;
  gap: var(--modern-space-2);
  min-width: 0;
  color: var(--modern-muted);
  font-size: var(--modern-font-size-secondary);
}
.modern-cache-kpi-symbol {
  display: grid;
  width: var(--modern-control-xs);
  height: var(--modern-control-xs);
  flex: none;
  place-items: center;
  border-radius: var(--modern-radius-control);
  color: var(--modern-cache-kpi-tone);
  background: color-mix(in srgb, var(--modern-cache-kpi-tone) 8%, var(--modern-surface));
}
.modern-cache-kpi-value {
  min-width: 0;
  color: var(--modern-text);
  font-size: var(--modern-font-size-title);
  font-weight: var(--modern-weight-semibold);
  line-height: var(--modern-leading-title);
  letter-spacing: var(--modern-tracking-title);
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
}
.modern-cache-kpi-detail {
  margin-top: auto;
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  line-height: var(--modern-leading-compact);
  overflow-wrap: anywhere;
}
.modern-cache-kpi-link {
  color: inherit;
  text-decoration: underline;
  text-decoration-color: var(--modern-border);
  text-underline-offset: var(--modern-space-1);
}
.modern-cache-kpi-link:hover {
  color: var(--modern-accent);
}
</style>
