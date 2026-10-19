<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AffinityAggregate } from '@modern/api/cache-affinity'
import { AppTooltip } from '@modern/components/ui'
import { formatCompactNumber } from '@modern/components/ui/format'
import {
  costWithoutCache,
  money,
  netSaving,
  percentage,
  shareOf,
  writeTokens,
} from './cache-affinity-display'

const props = defineProps<{ summary: AffinityAggregate }>()
const { t, locale } = useI18n()
const compact = (value: number) => formatCompactNumber(value, locale.value)
const writeParts = computed(() => [
  { id: 'write5m', value: props.summary.cache_write_5m_tokens },
  { id: 'write1h', value: props.summary.cache_write_1h_tokens },
  { id: 'writeUnknown', value: props.summary.cache_write_unknown_tokens },
])
</script>

<template>
  <div class="modern-cache-cost">
    <dl class="modern-cache-cost-totals">
      <div>
        <dt>{{ t('cacheAffinity.cost.withCache') }}</dt>
        <dd>{{ money(summary.estimated_cost_nano_usd, locale) }}</dd>
      </div>
      <div>
        <dt>
          <AppTooltip :label="t('cacheAffinity.cost.withoutCacheHint')" touchable>
            <span tabindex="0">{{ t('cacheAffinity.cost.withoutCache') }}</span>
          </AppTooltip>
        </dt>
        <dd>{{ money(costWithoutCache(summary), locale) }}</dd>
      </div>
      <div>
        <dt>{{ t('cacheAffinity.cost.savings') }}</dt>
        <dd>{{ money(summary.cache_read_savings_nano_usd, locale) }}</dd>
      </div>
      <div>
        <dt>{{ t('cacheAffinity.cost.premium') }}</dt>
        <dd>{{ money(summary.cache_write_premium_nano_usd, locale) }}</dd>
      </div>
      <div class="is-net">
        <dt>{{ t('cacheAffinity.cost.net') }}</dt>
        <dd>{{ money(netSaving(summary), locale) }}</dd>
      </div>
    </dl>
    <section class="modern-cache-cost-writes" :aria-label="t('cacheAffinity.cost.writes')">
      <h3>{{ t('cacheAffinity.cost.writes') }}</h3>
      <dl>
        <div v-for="part in writeParts" :key="part.id">
          <dt>{{ t('cacheAffinity.cost.' + part.id) }}</dt>
          <dd>
            {{ compact(part.value) }}
            <span>{{ percentage(shareOf(part.value, writeTokens(summary)), locale) }}</span>
          </dd>
        </div>
      </dl>
    </section>
  </div>
</template>

<style scoped>
.modern-cache-cost {
  display: grid;
  gap: var(--modern-space-4);
  min-width: 0;
}
.modern-cache-cost dl {
  margin: 0;
}
.modern-cache-cost-totals {
  display: grid;
  gap: var(--modern-space-1);
}
.modern-cache-cost-totals > div,
.modern-cache-cost-writes dl > div {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: baseline;
  gap: var(--modern-space-2);
  padding-block: var(--modern-space-1);
  font-size: var(--modern-font-size-secondary);
}
.modern-cache-cost-totals dt,
.modern-cache-cost-writes dt {
  min-width: 0;
  color: var(--modern-muted);
}
.modern-cache-cost-totals dd,
.modern-cache-cost-writes dd {
  margin: 0;
  color: var(--modern-text);
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
  text-align: right;
}
.modern-cache-cost-totals dd span,
.modern-cache-cost-writes dd span {
  margin-left: var(--modern-space-2);
  color: var(--modern-muted);
}
.modern-cache-cost-totals .is-net dd {
  font-weight: var(--modern-weight-semibold);
}
.modern-cache-cost-writes {
  padding-top: var(--modern-space-3);
  border-top: var(--modern-line-width) solid var(--modern-border);
}
.modern-cache-cost-writes h3 {
  margin-bottom: var(--modern-space-2);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  font-weight: var(--modern-weight-regular);
}
</style>
