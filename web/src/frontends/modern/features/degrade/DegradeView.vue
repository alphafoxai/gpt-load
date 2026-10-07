<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMutation, useQuery, useQueryClient } from '@tanstack/vue-query'
import { CircleCheck, CircleX, FlaskConical, Play, RotateCw } from '@lucide/vue'
import {
  getDegradeBoard,
  saveDegradeSchedule,
  startDegradeRun,
  type DegradeCredential,
} from '@modern/api/degrade'
import { useApiClient } from '@shared/http/client-context'
import { usePageRefresh } from '@modern/app/page-refresh'
import { AppButton, AppIcon, AppSelect } from '@modern/components/ui'

const { t } = useI18n()
const client = useApiClient()
const queryClient = useQueryClient()
const selected = ref<number[]>([])
const model = ref('gpt-6-luna')
const interval = ref('30')

const board = useQuery({
  queryKey: ['modern', 'degrade'],
  queryFn: ({ signal }) => getDegradeBoard(client, signal),
  refetchInterval: 4000,
})
const rows = computed(() => board.data.value?.credentials ?? [])
const schedule = computed(() => board.data.value?.schedule)
const shownModel = computed(() => model.value.trim() || schedule.value?.model || 'gpt-6-luna')
const intervalMinutes = computed(() => Number(interval.value) || 30)

const intervals = [
  { value: '15', label: '15 分钟' },
  { value: '30', label: '30 分钟' },
  { value: '60', label: '1 小时' },
  { value: '180', label: '3 小时' },
  { value: '360', label: '6 小时' },
  { value: '1440', label: '24 小时' },
]
watch(schedule, (value) => {
  if (!value) return
  model.value = value.model
  interval.value = String(Math.round(value.intervalMS / 60_000))
})

function verdict(row: DegradeCredential) {
  if (row.running) return { tone: 'running', label: '测试中' }
  const latest = row.latest
  if (!latest) return { tone: 'none', label: '尚未测试' }
  if (latest.degraded === true)
    return {
      tone: 'bad',
      label: `${latest.prediction || '未知模型'} ${(latest.probability * 100).toFixed(1)}%`,
      detail: '与测试模型不一致',
    }
  if (latest.degraded === false)
    return {
      tone: 'ok',
      label: `${latest.prediction} ${(latest.probability * 100).toFixed(1)}%`,
      detail: '与测试模型一致',
    }
  return { tone: 'unknown', label: latest.error || '没有结论' }
}

function when(value: number) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(value)
}

const run = useMutation({
  mutationFn: (ids: number[]) => startDegradeRun(client, shownModel.value, ids),
  onSuccess: () => queryClient.invalidateQueries({ queryKey: ['modern', 'degrade'] }),
})
const save = useMutation({
  mutationFn: () =>
    saveDegradeSchedule(client, {
      enabled: schedule.value?.enabled ?? false,
      interval_ms: intervalMinutes.value * 60_000,
      model: shownModel.value,
    }),
  onSuccess: () => queryClient.invalidateQueries({ queryKey: ['modern', 'degrade'] }),
})
const toggle = useMutation({
  mutationFn: (enabled: boolean) =>
    saveDegradeSchedule(client, {
      enabled,
      interval_ms: intervalMinutes.value * 60_000,
      model: shownModel.value,
    }),
  onSuccess: () => queryClient.invalidateQueries({ queryKey: ['modern', 'degrade'] }),
})

function toggleRow(id: number) {
  selected.value = selected.value.includes(id)
    ? selected.value.filter((item) => item !== id)
    : [...selected.value, id]
}

usePageRefresh({ refresh: () => board.refetch(), pending: () => board.isFetching.value })
</script>

<template>
  <section class="degrade">
    <p class="degrade-intro">通过三次数字生成挑战，识别最接近的模型家族与版本。只记录结果，不改变凭证权重。</p>
    <form class="degrade-toolbar" @submit.prevent="run.mutate(selected)">
      <label>
        <span>模型</span>
        <input v-model="model" :placeholder="schedule?.model || 'gpt-6-luna'" />
      </label>
      <AppSelect v-model="interval" label="巡检间隔" :options="intervals" />
      <AppButton type="button" variant="ghost" @click="save.mutate()">保存设置</AppButton>
      <AppButton type="button" :variant="schedule?.enabled ? 'danger' : 'ghost'" @click="toggle.mutate(!schedule?.enabled)">
        {{ schedule?.enabled ? '停止定时巡检' : '开启定时巡检' }}
      </AppButton>
      <AppButton type="submit" :icon="Play" :loading="run.isPending.value">
        测试{{ selected.length ? `所选 (${selected.length})` : `全部 (${rows.length})` }}
      </AppButton>
      <button class="degrade-refresh" type="button" aria-label="刷新" @click="board.refetch()">
        <AppIcon :icon="RotateCw" />
      </button>
    </form>
    <p v-if="schedule?.enabled" class="degrade-note">
      下次巡检 {{ when(schedule.nextRunAtMS) }} · 每个凭证 3 次请求
    </p>
    <div class="degrade-table">
      <div class="degrade-head">
        <span></span><span>凭证</span><span>归因结果</span><span>测试模型</span><span>测试时间</span><span>权重</span><span></span>
      </div>
      <article v-for="row in rows" :key="row.credentialID" class="degrade-row">
        <input type="checkbox" :checked="selected.includes(row.credentialID)" @change="toggleRow(row.credentialID)" />
        <div>
          <strong>{{ row.identity }}</strong>
          <small>{{ row.groupName }}<template v-if="row.plan"> · {{ row.plan }}</template> · {{ row.status }}</small>
        </div>
        <div :class="`verdict is-${verdict(row).tone}`">
          <AppIcon v-if="verdict(row).tone === 'ok'" :icon="CircleCheck" />
          <AppIcon v-else-if="verdict(row).tone === 'bad'" :icon="CircleX" />
          <AppIcon v-else :icon="FlaskConical" />
          <span>
            <b>{{ verdict(row).label }}</b>
            <small v-if="verdict(row).detail">{{ verdict(row).detail }}</small>
          </span>
        </div>
        <span>{{ row.latest?.model || '—' }}</span>
        <span>{{ when(row.latest?.startedAtMS || 0) }}</span>
        <span class="mono">{{ row.weight }}</span>
        <AppButton type="button" variant="ghost" :disabled="row.running" @click="run.mutate([row.credentialID])">测试</AppButton>
      </article>
      <p v-if="!rows.length" class="degrade-empty">{{ board.isLoading.value ? '加载中…' : '没有可测试的 Codex 凭证' }}</p>
    </div>
  </section>
</template>

<style scoped>
.degrade { display: grid; gap: var(--modern-space-4); }
.degrade-intro { margin: 0; color: var(--modern-muted); }
.degrade-toolbar, .degrade-head, .degrade-row {
  display: grid; grid-template-columns: 28px minmax(180px, 1.4fr) minmax(220px, 1.6fr) 140px 120px 70px 96px;
  gap: var(--modern-space-3); align-items: center;
}
.degrade-toolbar { grid-template-columns: 180px 150px auto auto auto 36px; padding: var(--modern-space-4);
  border: var(--modern-line-width) solid var(--modern-border); border-radius: var(--modern-radius-panel); background: var(--modern-surface); }
.degrade-toolbar label { display: grid; gap: var(--modern-space-1); color: var(--modern-muted); font-size: var(--modern-font-size-caption); }
.degrade-toolbar input { min-height: var(--modern-control-md); border: var(--modern-line-width) solid var(--modern-border);
  border-radius: var(--modern-radius-control); padding: 0 var(--modern-space-3); background: var(--modern-canvas); color: var(--modern-text); }
.degrade-note { margin: 0; color: var(--modern-muted); font-size: var(--modern-font-size-small); }
.degrade-table { border: var(--modern-line-width) solid var(--modern-border); border-radius: var(--modern-radius-panel); background: var(--modern-surface); overflow: hidden; }
.degrade-head, .degrade-row { padding: var(--modern-space-3) var(--modern-space-4); }
.degrade-head { color: var(--modern-muted); font-size: var(--modern-font-size-caption); border-bottom: var(--modern-line-width) solid var(--modern-border); }
.degrade-row + .degrade-row { border-top: var(--modern-line-width) solid var(--modern-border); }
.degrade-row strong, .degrade-row small { display: block; }
.degrade-row small { color: var(--modern-muted); }
.verdict { display: flex; gap: var(--modern-space-2); align-items: center; }
.verdict b, .verdict small { display: block; }
.verdict small { color: var(--modern-muted); font-weight: 400; }
.is-ok { color: var(--modern-success, #1f8a4c); }
.is-bad { color: var(--modern-danger, #c2410c); }
.is-running { color: var(--modern-accent); }
.mono { font-variant-numeric: tabular-nums; }
.degrade-refresh { display: grid; place-items: center; width: 36px; height: 36px; border: 0; border-radius: 999px; background: transparent; color: var(--modern-muted); }
.degrade-empty { margin: 0; padding: var(--modern-space-6); color: var(--modern-muted); }
@media (max-width: 960px) {
  .degrade-toolbar, .degrade-head, .degrade-row { grid-template-columns: 1fr; }
  .degrade-head { display: none; }
}
</style>
