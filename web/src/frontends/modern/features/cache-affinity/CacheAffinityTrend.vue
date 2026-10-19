<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CacheAffinityReport } from '@modern/api/cache-affinity'
import { AppSvg, AppTooltip } from '@modern/components/ui'
import { dateFormatter } from '@modern/components/ui/intl-formatters'
import { formatCompactNumber } from '@modern/components/ui/format'
import { cacheReadShare, percentage, stickyRate } from './cache-affinity-display'

type SeriesId = 'sticky' | 'cacheRead'
interface TrendPoint {
  from: number
  to: number
  requests: number
  sticky: number | null
  cacheRead: number | null
}

const props = defineProps<{ report: CacheAffinityReport }>()
const { t, locale } = useI18n()
const seriesIds = ['sticky', 'cacheRead'] as const
const gridValues = [0, 25, 50, 75, 100]
const host = ref<HTMLElement>()
const width = ref(720)
const hovered = ref<number>()
const focused = ref<number>()
let observer: ResizeObserver | undefined
onMounted(() => {
  observer = new ResizeObserver(([entry]) => {
    if (entry) width.value = Math.max(220, entry.contentRect.width)
  })
  if (host.value) observer.observe(host.value)
})
onBeforeUnmount(() => observer?.disconnect())
watch(
  () => props.report,
  () => {
    hovered.value = undefined
    focused.value = undefined
  },
)

// 横轴按桶宽对齐遍历时间范围；series 只含有请求的桶，缺失的桶保留为空点，不补零
const points = computed<TrendPoint[]>(() => {
  const { from_ms, to_ms, bucket_width_ms } = props.report
  const buckets = new Map(props.report.series.map((row) => [row.bucket_start_ms, row]))
  const result: TrendPoint[] = []
  for (
    let start = Math.floor(from_ms / bucket_width_ms) * bucket_width_ms;
    start < to_ms;
    start += bucket_width_ms
  ) {
    const row = buckets.get(start)
    result.push({
      from: Math.max(start, from_ms),
      to: Math.min(start + bucket_width_ms, to_ms),
      requests: row?.request_count ?? 0,
      sticky: row ? stickyRate(row) : null,
      cacheRead: row ? cacheReadShare(row) : null,
    })
  }
  return result
})
const hasMeasured = computed(() =>
  points.value.some((point) => point.sticky !== null || point.cacheRead !== null),
)
const chartLabel = computed(() =>
  t('cacheAffinity.trend.chartKeyboard', {
    metric: `${t('cacheAffinity.trend.sticky')} / ${t('cacheAffinity.trend.cacheRead')}`,
  }),
)
const legend = computed(() => [
  {
    id: 'sticky',
    label: t('cacheAffinity.trend.sticky'),
    value: percentage(stickyRate(props.report.summary), locale.value),
  },
  {
    id: 'cacheRead',
    label: t('cacheAffinity.trend.cacheRead'),
    value: percentage(cacheReadShare(props.report.summary), locale.value),
  },
])

const height = 240,
  bottom = 204
const top = computed(() => (width.value < 340 ? 56 : 32))
const left = computed(() => {
  const longest = Math.max(...gridValues.map((value) => percentage(value, locale.value).length))
  return Math.max(44, Math.min(108, longest * 7 + 14))
})
const right = computed(() => width.value - 8)
const position = (time: number) =>
  left.value +
  ((right.value - left.value) * (time - props.report.from_ms)) /
    (props.report.to_ms - props.report.from_ms)
// 纵轴固定为 0–100%
const y = (value: number) => bottom - ((bottom - top.value) * value) / 100
const coordinates = computed(() =>
  points.value.map((point) => {
    const start = position(point.from),
      end = position(point.to)
    const ys: Record<SeriesId, number | null> = {
      sticky: point.sticky === null ? null : y(point.sticky),
      cacheRead: point.cacheRead === null ? null : y(point.cacheRead),
    }
    return { ...point, x: (start + end) / 2, ys }
  }),
)
// 值为 null 的点断开折线：不连线、不画点
function linePlot(id: SeriesId) {
  const segments: { line: string; area: string }[] = []
  const dots: { key: number; x: number; y: number }[] = []
  let line = '',
    first = 0,
    last = 0
  function finish(): void {
    if (line) segments.push({ line, area: `${line} L${last},${bottom} L${first},${bottom} Z` })
    line = ''
  }
  for (const point of coordinates.value) {
    const value = point.ys[id]
    if (value === null) {
      finish()
      continue
    }
    if (!line) first = point.x
    last = point.x
    line += `${line ? ' L' : 'M'}${point.x},${value}`
    dots.push({ key: point.from, x: point.x, y: value })
  }
  finish()
  return { segments, dots }
}
const plots = computed(() => seriesIds.map((id) => ({ id, ...linePlot(id) })))
const selected = computed(() => hovered.value ?? focused.value)
const active = computed(() =>
  selected.value === undefined ? undefined : coordinates.value[selected.value],
)
const markers = computed(() => {
  const current = active.value
  if (!current) return []
  return seriesIds.flatMap((id) => {
    const value = current.ys[id]
    return value === null ? [] : [{ id, x: current.x, y: value }]
  })
})
const ticks = computed(() => {
  const count = width.value < 450 ? 3 : 5
  return Array.from({ length: count }, (_, index) => {
    const fraction = index / (count - 1)
    return {
      x: left.value + fraction * (right.value - left.value),
      time: props.report.from_ms + fraction * (props.report.to_ms - props.report.from_ms),
    }
  })
})
function clock(value: number): string {
  return dateFormatter(
    locale.value,
    props.report.to_ms - props.report.from_ms > 2 * 86400000
      ? { month: 'short', day: 'numeric' }
      : { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' },
  ).format(value)
}
const pointLabels = computed(() => {
  const day = dateFormatter(locale.value, { month: 'short', day: 'numeric' })
  const time = dateFormatter(locale.value, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  return points.value.map((point) => {
    const endDay = day.format(point.from) === day.format(point.to) ? '' : day.format(point.to) + ' '
    return [
      t('cacheAffinity.trend.point', {
        start: `${day.format(point.from)} ${time.format(point.from)}`,
        end: `${endDay}${time.format(point.to)}`,
      }),
      `${t('cacheAffinity.trend.sticky')} ${percentage(point.sticky, locale.value)}`,
      `${t('cacheAffinity.trend.cacheRead')} ${percentage(point.cacheRead, locale.value)}`,
      `${t('cacheAffinity.trend.requests')} ${formatCompactNumber(point.requests, locale.value)}`,
    ].join('\n')
  })
})
const tooltip = computed(() => pointLabels.value[selected.value ?? 0])
function move(event: PointerEvent): void {
  const bounds = (event.currentTarget as HTMLElement).getBoundingClientRect()
  if (!bounds.width || !points.value.length) return
  const fraction = Math.max(0, Math.min(1, (event.clientX - bounds.left) / bounds.width))
  const at = props.report.from_ms + fraction * (props.report.to_ms - props.report.from_ms)
  let low = 0,
    high = points.value.length
  while (low < high) {
    const middle = Math.floor((low + high) / 2)
    if (points.value[middle]!.to <= at) low = middle + 1
    else high = middle
  }
  hovered.value = Math.min(low, points.value.length - 1)
}
function focus(): void {
  focused.value = selected.value ?? 0
}
function navigate(event: KeyboardEvent): void {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  if (event.key === 'Escape') {
    hovered.value = undefined
    focused.value = undefined
    return
  }
  let next = selected.value ?? 0
  if (event.key === 'ArrowLeft') next--
  else if (event.key === 'ArrowRight') next++
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = points.value.length - 1
  else return
  event.preventDefault()
  hovered.value = undefined
  focused.value = Math.max(0, Math.min(points.value.length - 1, next))
}
</script>

<template>
  <div ref="host" class="modern-cache-trend" role="group" :aria-label="chartLabel">
    <div v-if="legend.length" class="modern-cache-trend-legend">
      <span v-for="item in legend" :key="item.id" class="modern-cache-trend-legend-item">
        <i :data-series="item.id" aria-hidden="true" />
        <span>{{ item.label }}</span>
        <strong>{{ item.value }}</strong>
      </span>
    </div>
    <AppSvg :viewBox="`0 0 ${width} ${height}`" aria-hidden="true" focusable="false">
      <g v-for="value in gridValues" :key="value" class="modern-cache-trend-gridline">
        <line :x1="left" :x2="right" :y1="y(value)" :y2="y(value)" />
        <text :x="left - 10" :y="y(value) + 4" text-anchor="end">{{
          percentage(value, locale)
        }}</text>
      </g>
      <g v-for="plot in plots" :key="plot.id" :data-series="plot.id">
        <template v-for="(segment, index) in plot.segments" :key="index">
          <path :d="segment.area" class="modern-cache-trend-area" />
          <path :d="segment.line" class="modern-cache-trend-line" />
        </template>
        <circle
          v-for="dot in plot.dots"
          :key="dot.key"
          :cx="dot.x"
          :cy="dot.y"
          r="2"
          class="modern-cache-trend-dot"
        />
      </g>
      <g v-if="active">
        <line
          :x1="active.x"
          :x2="active.x"
          :y1="top"
          :y2="bottom"
          class="modern-cache-trend-crosshair"
        />
        <circle
          v-for="marker in markers"
          :key="marker.id"
          :cx="marker.x"
          :cy="marker.y"
          r="4"
          class="modern-cache-trend-selected-dot"
          :data-series="marker.id"
        />
      </g>
      <text
        v-for="(tick, index) in ticks"
        :key="index"
        :x="tick.x"
        :y="height - 10"
        :text-anchor="index === 0 ? 'start' : index === ticks.length - 1 ? 'end' : 'middle'"
        class="modern-cache-trend-axis"
        >{{ clock(tick.time) }}</text
      >
    </AppSvg>
    <span v-if="!report.summary.request_count || !hasMeasured" class="modern-cache-trend-empty">
      {{
        t(
          !report.summary.request_count
            ? 'cacheAffinity.empty'
            : 'cacheAffinity.trend.noMeasuredData',
        )
      }}
    </span>
    <AppTooltip v-if="coordinates.length" :label="tooltip" side="top">
      <span
        class="modern-cache-trend-hit"
        role="img"
        :aria-label="tooltip"
        tabindex="0"
        :style="{
          left: (left / width) * 100 + '%',
          width: ((right - left) / width) * 100 + '%',
          top: (top / height) * 100 + '%',
          height: ((bottom - top) / height) * 100 + '%',
        }"
        @pointermove="move"
        @pointerleave="hovered = undefined"
        @focus="focus"
        @blur="focused = undefined"
        @keydown="navigate"
      />
    </AppTooltip>
  </div>
</template>

<style scoped>
.modern-cache-trend {
  position: relative;
  min-width: 0;
  color: var(--modern-accent);
}
.modern-cache-trend [data-series='cacheRead'] {
  color: var(--modern-chart-cache);
}
.modern-cache-trend-legend {
  position: absolute;
  top: 0;
  left: 0;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--modern-space-1) var(--modern-space-5);
  max-width: 100%;
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
.modern-cache-trend-legend-item {
  display: inline-flex;
  align-items: center;
  gap: var(--modern-space-1-5);
  white-space: nowrap;
}
.modern-cache-trend-legend-item i {
  width: var(--modern-space-2);
  height: var(--modern-space-2);
  border-radius: var(--modern-space-0-5);
  background: currentColor;
}
.modern-cache-trend-legend-item strong {
  color: var(--modern-text);
  font-weight: var(--modern-weight-medium);
  font-variant-numeric: tabular-nums;
}
.modern-cache-trend svg {
  display: block;
  width: 100%;
  height: 240px;
  overflow: visible;
}
.modern-cache-trend-gridline line {
  stroke: color-mix(in srgb, var(--modern-border) 75%, var(--modern-surface));
  stroke-width: var(--modern-line-width);
  stroke-dasharray: 2 5;
}
.modern-cache-trend-gridline text,
.modern-cache-trend-axis {
  fill: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  font-variant-numeric: tabular-nums;
}
.modern-cache-trend-area {
  fill: var(--modern-chart-area-soft-fill);
}
.modern-cache-trend-line {
  fill: none;
  stroke: currentColor;
  stroke-width: var(--modern-trend-stroke);
  stroke-linejoin: round;
  stroke-linecap: round;
}
.modern-cache-trend-dot {
  fill: currentColor;
}
.modern-cache-trend-selected-dot {
  fill: currentColor;
  stroke: var(--modern-surface);
  stroke-width: var(--modern-focus-width);
}
.modern-cache-trend-crosshair {
  stroke: var(--modern-tooltip-border);
  stroke-width: var(--modern-line-width);
  stroke-dasharray: 3 4;
}
.modern-cache-trend-hit {
  position: absolute;
  cursor: crosshair;
  outline: none;
}
.modern-cache-trend-hit:focus-visible {
  outline: var(--modern-line-width) dashed var(--modern-tooltip-border);
  outline-offset: 0;
}
.modern-cache-trend-empty {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  pointer-events: none;
  color: var(--modern-muted);
  font-size: var(--modern-font-size-secondary);
}
</style>
