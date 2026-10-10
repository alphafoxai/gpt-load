<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { finiteMetric, metricTone, percent, timestampMs } from './format'
import StatusHistory from './StatusHistory.vue'
import { MIRASIM_SOURCE_URL, type NullableMetric, type StatusTimestamp } from './types'
import { useMirasimStatus } from './use-mirasim-status'

const { t, locale } = useI18n()
const { response, isFetching, failed, error, stale, refresh } = useMirasimStatus()
const selected = ref('paid')
const cohorts = ['paid', 'free', 'cloud'] as const
const snapshot = computed(() => response.value?.data)
const cohort = computed(() => snapshot.value?.cohorts.find((entry) => entry.id === selected.value))

function availability(value: NullableMetric | undefined): string {
  return percent(value, locale.value, t('mirasimStatus.unknown'))
}
function latency(value: NullableMetric | undefined): string {
  return finiteMetric(value)
    ? t('mirasimStatus.seconds', {
        value: new Intl.NumberFormat(locale.value, { maximumFractionDigits: 2 }).format(value),
      })
    : t('mirasimStatus.unknown')
}
function date(value: StatusTimestamp | null | undefined): string {
  const ms = timestampMs(value)
  return ms === undefined
    ? t('mirasimStatus.unknown')
    : new Intl.DateTimeFormat(locale.value, {
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      }).format(ms)
}

defineExpose({ refresh, isFetching, response })
</script>

<template>
  <section class="mirasim-status" aria-labelledby="mirasim-status-title">
    <header class="ms-heading">
      <div>
        <h1 id="mirasim-status-title">{{ t('mirasimStatus.title') }}</h1>
        <p class="ms-muted">{{ t('mirasimStatus.description') }}</p>
      </div>
      <a class="ms-source" :href="MIRASIM_SOURCE_URL" target="_blank" rel="noopener noreferrer">
        {{ t('mirasimStatus.source') }}
      </a>
    </header>

    <div class="ms-toolbar">
      <div class="ms-cohorts" role="group" :aria-label="t('mirasimStatus.cohort')">
        <button
          v-for="id in cohorts"
          :key="id"
          type="button"
          :aria-pressed="selected === id"
          @click="selected = id"
        >
          {{ t(`mirasimStatus.${id}`) }}
        </button>
      </div>
      <button type="button" :disabled="isFetching" @click="refresh()">
        {{
          t(
            isFetching
              ? 'mirasimStatus.refreshing'
              : failed
                ? 'mirasimStatus.retry'
                : 'mirasimStatus.refresh',
          )
        }}
      </button>
    </div>
    <p class="ms-muted ms-small">{{ t('mirasimStatus.refreshHint') }}</p>

    <div
      class="ms-feedback"
      :class="{ 'ms-feedback--warning': stale, 'ms-feedback--error': failed && !snapshot }"
      role="status"
      aria-live="polite"
    >
      <strong v-if="stale">{{ t('mirasimStatus.stale') }}</strong>
      <strong v-else-if="failed && !snapshot">{{ t('mirasimStatus.initialError') }}</strong>
      <span v-else-if="!snapshot">{{ t('mirasimStatus.loading') }}</span>
      <span v-else>{{ t('mirasimStatus.fresh') }}</span>
      <span v-if="isFetching && snapshot"> · {{ t('mirasimStatus.refreshing') }}</span>
      <p v-if="failed && error" class="ms-error-detail">{{ error }}</p>
    </div>

    <dl v-if="response" class="ms-timestamps">
      <div>
        <dt>{{ t('mirasimStatus.checkedAt') }}</dt>
        <dd>{{ date(response.checked_at) }}</dd>
      </div>
      <div>
        <dt>{{ t('mirasimStatus.fetchedAt') }}</dt>
        <dd>{{ date(response.fetched_at) }}</dd>
      </div>
      <div>
        <dt>{{ t('mirasimStatus.generatedAt') }}</dt>
        <dd>{{ date(snapshot?.generatedAt) }}</dd>
      </div>
      <div>
        <dt>{{ t('mirasimStatus.dataThrough') }}</dt>
        <dd>{{ date(snapshot?.dataThrough) }}</dd>
      </div>
    </dl>

    <div v-if="snapshot" class="ms-content" :aria-busy="isFetching">
      <p v-if="cohort" class="ms-muted">
        {{ t('mirasimStatus.cohortState', { state: cohort.state }) }}
      </p>
      <p v-if="!cohort?.agents.length" class="ms-empty">{{ t('mirasimStatus.empty') }}</p>
      <div v-else class="ms-agents">
        <article v-for="agent in cohort.agents" :key="`${selected}-${agent.id}`" class="ms-card">
          <header class="ms-agent-heading">
            <h2>{{ agent.name }}</h2>
            <span
              class="ms-status"
              :class="`ms-status--${metricTone(agent.summary.now.availability, snapshot.thresholds.good, snapshot.thresholds.warn)}`"
            >
              {{ t('mirasimStatus.reportedStatus', { status: agent.summary.now.status }) }}
            </span>
          </header>
          <dl class="ms-metrics">
            <div class="ms-metric--primary">
              <dt>{{ t('mirasimStatus.now') }}</dt>
              <dd>{{ availability(agent.summary.now.availability) }}</dd>
            </div>
            <div>
              <dt>{{ t('mirasimStatus.h24') }}</dt>
              <dd>{{ availability(agent.summary.availability.h24) }}</dd>
            </div>
            <div>
              <dt>{{ t('mirasimStatus.d7') }}</dt>
              <dd>{{ availability(agent.summary.availability.d7) }}</dd>
            </div>
            <div>
              <dt>{{ t('mirasimStatus.p50') }}</dt>
              <dd>{{ latency(agent.summary.latency.p50) }}</dd>
            </div>
            <div>
              <dt>{{ t('mirasimStatus.p95') }}</dt>
              <dd>{{ latency(agent.summary.latency.p95) }}</dd>
            </div>
          </dl>
          <div class="ms-reasons">
            <h3>{{ t('mirasimStatus.reasons') }}</h3>
            <ul v-if="agent.reasons?.length">
              <li v-for="reason in agent.reasons" :key="reason.class">
                {{ reason.class }} · {{ availability(reason.share) }}
              </li>
            </ul>
            <span v-else class="ms-muted">{{ t('mirasimStatus.noReasons') }}</span>
          </div>
          <h3>{{ t('mirasimStatus.history') }}</h3>
          <StatusHistory
            :name="agent.name"
            :cells="agent.summary.cells"
            :start="snapshot.cellsStart"
            :cell-seconds="snapshot.cellSeconds"
            :good="snapshot.thresholds.good"
            :warn="snapshot.thresholds.warn"
          />

          <div
            v-if="agent.models.length"
            class="ms-table-scroll"
            tabindex="0"
            role="region"
            :aria-label="`${agent.name} · ${t('mirasimStatus.models')}`"
          >
            <table>
              <caption>
                {{
                  agent.name
                }}
                ·
                {{
                  t('mirasimStatus.models')
                }}
              </caption>
              <thead>
                <tr>
                  <th scope="col">{{ t('mirasimStatus.model') }}</th>
                  <th scope="col">{{ t('mirasimStatus.now') }}</th>
                  <th scope="col">{{ t('mirasimStatus.h24') }}</th>
                  <th scope="col">{{ t('mirasimStatus.d7') }}</th>
                  <th scope="col">{{ t('mirasimStatus.p50') }}</th>
                  <th scope="col">{{ t('mirasimStatus.p95') }}</th>
                </tr>
              </thead>
              <tbody>
                <template v-for="model in agent.models" :key="model.id">
                  <tr>
                    <th scope="row">{{ model.name }}</th>
                    <td>{{ availability(model.now.availability) }}</td>
                    <td>{{ availability(model.availability.h24) }}</td>
                    <td>{{ availability(model.availability.d7) }}</td>
                    <td>{{ latency(model.latency.p50) }}</td>
                    <td>{{ latency(model.latency.p95) }}</td>
                  </tr>
                  <tr class="ms-history-row">
                    <td colspan="6">
                      <span class="ms-small ms-muted"
                        >{{ model.name }} · {{ t('mirasimStatus.history') }}</span
                      >
                      <StatusHistory
                        :name="model.name"
                        :cells="model.cells"
                        :start="snapshot.cellsStart"
                        :cell-seconds="snapshot.cellSeconds"
                        :good="snapshot.thresholds.good"
                        :warn="snapshot.thresholds.warn"
                      />
                    </td>
                  </tr>
                </template>
              </tbody>
            </table>
          </div>
          <p v-else class="ms-muted">{{ t('mirasimStatus.noModels') }}</p>
        </article>
      </div>
      <footer class="ms-legend ms-small">
        <p>{{ t('mirasimStatus.historyHelp') }}</p>
        <ul>
          <li>
            <i class="ms-dot ms-dot--good" aria-hidden="true" />{{
              t('mirasimStatus.good', { value: snapshot.thresholds.good })
            }}
          </li>
          <li>
            <i class="ms-dot ms-dot--warn" aria-hidden="true" />{{
              t('mirasimStatus.warn', snapshot.thresholds)
            }}
          </li>
          <li>
            <i class="ms-dot ms-dot--low" aria-hidden="true" />{{
              t('mirasimStatus.low', { value: snapshot.thresholds.warn })
            }}
          </li>
          <li><i class="ms-dot" aria-hidden="true" />{{ t('mirasimStatus.unknown') }}</li>
        </ul>
        <p>{{ t('mirasimStatus.thresholds', snapshot.thresholds) }}</p>
      </footer>
      <aside v-if="snapshot.notes.length" class="ms-notes">
        <h2>{{ t('mirasimStatus.notes') }}</h2>
        <ul>
          <li v-for="(note, index) in snapshot.notes" :key="index">{{ note }}</li>
        </ul>
      </aside>
    </div>
  </section>
</template>

<style scoped>
.mirasim-status {
  --ms-text: var(--modern-text, var(--color-text));
  --ms-muted: var(--modern-muted, var(--color-text-muted));
  --ms-surface: var(--modern-surface, var(--color-surface));
  --ms-subtle: var(--modern-subtle, var(--color-surface-sunken));
  --ms-border: var(--modern-border, var(--color-border-subtle));
  --ms-action: var(--modern-accent, var(--color-action));
  --ms-success: var(--modern-success, var(--color-success));
  --ms-warning: var(--modern-warning, var(--color-warning));
  --ms-danger: var(--modern-danger, var(--color-danger));
  --ms-neutral: var(--modern-scrollbar, var(--color-border-control));
  --ms-radius: var(--modern-radius-panel, var(--radius-card));
  --ms-small: var(--modern-font-size-small, var(--text-meta));
  color: var(--ms-text);
  max-width: 1280px;
  margin: 0 auto;
  padding: 24px;
  line-height: 1.6;
}
.ms-heading,
.ms-toolbar,
.ms-agent-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}
h1,
h2,
h3,
p,
dl,
dd {
  margin: 0;
}
h1 {
  font-size: var(--modern-font-size-title, var(--title-lede));
}
h2 {
  font-size: var(--modern-font-size-section, var(--title-section));
}
h3 {
  font-size: inherit;
  font-weight: 600;
  margin-bottom: 8px;
}
.ms-heading {
  align-items: flex-start;
  margin-bottom: 24px;
}
.ms-heading p {
  max-width: 760px;
  margin-top: 8px;
}
.ms-source {
  color: var(--ms-action);
  white-space: nowrap;
  padding-block: 8px;
  text-underline-offset: 3px;
}
.ms-muted,
dt {
  color: var(--ms-muted);
}
.ms-small {
  font-size: var(--ms-small);
}
.ms-toolbar {
  margin-bottom: 8px;
}
.ms-cohorts {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
button {
  font: inherit;
  color: var(--ms-text);
  background: var(--ms-surface);
  border: 1px solid var(--ms-border);
  border-radius: var(--modern-radius-control, var(--radius-control));
  padding: 8px 16px;
  min-height: 44px;
  cursor: pointer;
}
button:hover,
button[aria-pressed='true'] {
  background: var(--ms-subtle);
  border-color: var(--ms-action);
}
button[aria-pressed='true'] {
  color: var(--ms-action);
  font-weight: 600;
}
button:disabled {
  cursor: wait;
  color: var(--ms-muted);
}
button:focus-visible,
a:focus-visible,
.ms-table-scroll:focus-visible {
  outline: 2px solid var(--ms-action);
  outline-offset: 3px;
}
.ms-feedback {
  margin-block: 16px;
  padding: 12px 16px;
  border: 1px solid var(--ms-border);
  border-radius: var(--ms-radius);
  background: var(--ms-subtle);
}
.ms-feedback--warning {
  border-color: var(--ms-warning);
}
.ms-feedback--error {
  border-color: var(--ms-danger);
}
.ms-error-detail {
  color: var(--ms-muted);
  overflow-wrap: anywhere;
  margin-top: 4px;
}
.ms-timestamps {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 24px;
  font-size: var(--ms-small);
}
.ms-timestamps dd {
  overflow-wrap: anywhere;
}
.ms-agents {
  display: grid;
  gap: 20px;
  margin-top: 12px;
}
.ms-card {
  min-width: 0;
  background: var(--ms-surface);
  border: 1px solid var(--ms-border);
  border-radius: var(--ms-radius);
  padding: 20px;
}
.ms-status {
  font-size: var(--ms-small);
  color: var(--ms-muted);
}
.ms-status--good {
  color: var(--ms-success);
}
.ms-status--warn {
  color: var(--ms-warning);
}
.ms-status--low {
  color: var(--ms-danger);
}
.ms-metrics {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 16px;
  margin-block: 20px;
}
.ms-metrics dt {
  font-size: var(--ms-small);
}
.ms-metrics dd {
  font-size: 20px;
  font-variant-numeric: tabular-nums;
}
.ms-metric--primary dd {
  font-weight: 600;
}
.ms-reasons {
  margin-bottom: 20px;
  font-size: var(--ms-small);
}
.ms-reasons ul,
.ms-legend ul {
  display: flex;
  gap: 8px 20px;
  flex-wrap: wrap;
  padding: 0;
  list-style: none;
  margin: 0;
}
.ms-table-scroll {
  overflow-x: auto;
  margin-top: 16px;
}
table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--ms-small);
  min-width: 620px;
}
caption {
  text-align: left;
  font-weight: 600;
  padding-block: 8px;
}
th,
td {
  padding: 10px 8px;
  text-align: left;
  font-variant-numeric: tabular-nums;
}
thead {
  color: var(--ms-muted);
  background: var(--ms-subtle);
}
tbody th {
  max-width: 180px;
  overflow-wrap: anywhere;
}
.ms-history-row td {
  border-bottom: 1px solid var(--ms-border);
  padding-top: 0;
}
.ms-history-row span {
  display: block;
  margin-bottom: 4px;
}
.ms-legend {
  color: var(--ms-muted);
  margin-top: 20px;
}
.ms-legend ul {
  margin-block: 8px;
}
.ms-legend li {
  display: flex;
  align-items: center;
  gap: 6px;
}
.ms-dot {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 2px;
  background: var(--ms-neutral);
}
.ms-dot--good {
  background: var(--ms-success);
}
.ms-dot--warn {
  background: var(--ms-warning);
}
.ms-dot--low {
  background: var(--ms-danger);
}
.ms-notes {
  margin-top: 20px;
  overflow-wrap: anywhere;
}
.ms-empty {
  padding: 32px 16px;
  text-align: center;
  color: var(--ms-muted);
}
@media (max-width: 760px) {
  .mirasim-status {
    padding: 16px 12px;
  }
  .ms-card {
    padding: 16px 12px;
  }
  .ms-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .ms-metric--primary {
    grid-column: 1 / -1;
  }
  .ms-timestamps {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
