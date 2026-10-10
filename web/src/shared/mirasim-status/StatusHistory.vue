<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { historyCells, metricTone, percent, timestampMs } from './format'
import type { NullableMetric, StatusTimestamp } from './types'

const props = defineProps<{
  name: string
  cells: NullableMetric[]
  start: StatusTimestamp
  cellSeconds: number
  good: number
  warn: number
}>()
const { t, locale } = useI18n()
const values = computed(() => historyCells(props.cells))
const activeIndex = ref(0)
const highlighted = ref<number | null>(null)

function label(index: number): string {
  const start = timestampMs(props.start)
  const time =
    start === undefined
      ? t('mirasimStatus.cellIndex', { index: index + 1 })
      : new Intl.DateTimeFormat(locale.value, {
          month: 'short',
          day: 'numeric',
          hour: '2-digit',
          minute: '2-digit',
        }).format(start + index * props.cellSeconds * 1000)
  return t('mirasimStatus.cellLabel', {
    time,
    value: percent(values.value[index], locale.value, t('mirasimStatus.unknown')),
  })
}

function focusCell(index: number): void {
  activeIndex.value = index
  highlighted.value = index
}

function navigate(event: KeyboardEvent, index: number): void {
  let target: number
  if (event.key === 'ArrowRight') target = Math.min(47, index + 1)
  else if (event.key === 'ArrowLeft') target = Math.max(0, index - 1)
  else if (event.key === 'Home') target = 0
  else if (event.key === 'End') target = 47
  else return
  event.preventDefault()
  activeIndex.value = target
  const cell = (event.currentTarget as HTMLElement).parentElement?.children.item(target)
  if (cell instanceof HTMLElement) cell.focus()
}
</script>

<template>
  <div class="history">
    <div
      class="history__cells"
      role="group"
      :aria-label="t('mirasimStatus.historyLabel', { name })"
    >
      <button
        v-for="(value, index) in values"
        :key="index"
        type="button"
        class="history__cell"
        :class="`history__cell--${metricTone(value, good, warn)}`"
        :aria-label="label(index)"
        :tabindex="index === activeIndex ? 0 : -1"
        @mouseenter="highlighted = index"
        @mouseleave="highlighted = null"
        @focus="focusCell(index)"
        @blur="highlighted = null"
        @click="highlighted = index"
        @keydown="navigate($event, index)"
      />
    </div>
    <div class="history__value" aria-live="polite">
      {{ highlighted === null ? '\u00a0' : label(highlighted) }}
    </div>
  </div>
</template>

<style scoped>
.history {
  min-width: 0;
}
.history__cells {
  display: grid;
  grid-template-columns: repeat(48, minmax(0, 1fr));
  gap: 2px;
}
.history__cell {
  display: block;
  width: 100%;
  height: 24px;
  min-width: 0;
  padding: 0;
  border: 0;
  border-radius: 2px;
  cursor: pointer;
  background: var(--ms-neutral);
}
.history__cell--good {
  background: var(--ms-success);
}
.history__cell--warn {
  background: var(--ms-warning);
}
.history__cell--low {
  background: var(--ms-danger);
}
.history__cell:focus-visible {
  outline: 2px solid var(--ms-text);
  outline-offset: 2px;
}
.history__cell:hover {
  outline: 2px solid var(--ms-text);
}
.history__value {
  min-height: 1.5em;
  margin-top: 4px;
  color: var(--ms-muted);
  font-size: var(--ms-small);
  overflow-wrap: anywhere;
}
</style>
