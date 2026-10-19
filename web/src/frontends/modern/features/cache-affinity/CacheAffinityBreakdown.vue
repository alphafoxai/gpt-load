<script setup lang="ts">
import { ArrowDown, ArrowUp, ArrowUpDown } from '@lucide/vue'
import { useQueries, useQuery } from '@tanstack/vue-query'
import { computed, ref, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { affinityDimensions } from '@modern/api/cache-affinity'
import type {
  AffinityAggregate,
  AffinityBreakdowns,
  AffinityDimension,
} from '@modern/api/cache-affinity'
import { getGroupCredentials } from '@modern/api/group-detail'
import { getGroupWorkspace, groupQueryKey } from '@modern/api/groups'
import type { GroupRow } from '@modern/api/groups'
import { getLogAccessKeys } from '@modern/api/logs'
import {
  AppButton,
  AppChannelIcon,
  AppOverflowText,
  AppSegmentedControl,
} from '@modern/components/ui'
import { formatCompactNumber } from '@modern/components/ui/format'
import { useApiClient } from '@shared/http/client-context'
import { cacheReadShare, money, netSaving, percentage, stickyRate } from './cache-affinity-display'

const props = defineProps<{ breakdowns: AffinityBreakdowns }>()

const { t, locale } = useI18n()
const client = useApiClient()

// 服务端每个维度最多返回 50 行，达到上限时提示可能不完整。
const breakdownLimit = 50

type AffinitySortKey = 'request_count' | 'miss_count'
type SortDirection = 'asc' | 'desc'

interface BreakdownLine {
  key: string
  row: AffinityAggregate
  name: string
  detail?: string
  channel?: GroupRow
}

// 标签页只保存在组件状态中，不写入 URL。
const dimension = ref<AffinityDimension>('group')
const dimensionOptions = computed(() =>
  affinityDimensions.map((value) => ({
    value,
    label: t('cacheAffinity.breakdown.dimensions.' + value),
  })),
)
function selectDimension(value: string): void {
  dimension.value = affinityDimensions.find((item) => item === value) ?? dimension.value
}

const groups = useQuery({
  queryKey: groupQueryKey,
  queryFn: ({ signal }) => getGroupWorkspace(client, signal),
})
const keys = useQuery({
  queryKey: ['modern', 'log-access-key-options'],
  queryFn: ({ signal }) => getLogAccessKeys(client, signal),
})
const groupMap = computed(
  () => new Map(groups.data.value?.items.map((row) => [row.id, row] as const)),
)
const keyMap = computed(() => new Map(keys.data.value?.map((row) => [row.id, row] as const)))

// 凭据名称按分组读取：只在凭据标签页可见时请求，每个分组只请求一次。
const credentialGroupIDs = computed(() =>
  dimension.value === 'credential'
    ? [...new Set(props.breakdowns.credential.map((row) => row.group_id))]
    : [],
)
const credentialLists = useQueries({
  queries: computed(() =>
    credentialGroupIDs.value.map((groupID) => ({
      queryKey: ['modern', 'cache-affinity', 'credentials', groupID],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        getGroupCredentials(
          client,
          groupID,
          { q: '', page: 1, pageSize: 100, sort: 'name', status: '', proxy: '', reset: '' },
          signal,
        ),
    })),
  ),
})
// 分组 -> 凭据 ID -> 标签；查询未返回的凭据回退为 #id。
const credentialLabels = computed(() => {
  const labels = new Map<number, Map<number, string>>()
  credentialGroupIDs.value.forEach((groupID, index) => {
    const items = credentialLists.value[index]?.data?.items
    if (items) labels.set(groupID, new Map(items.map((row) => [row.id, row.label] as const)))
  })
  return labels
})

const lines = computed<BreakdownLine[]>(() => {
  const current = dimension.value
  switch (current) {
    case 'credential':
      return props.breakdowns.credential.map((row) => ({
        key: `${row.group_id}:${row.credential_id}`,
        row,
        name:
          credentialLabels.value.get(row.group_id)?.get(row.credential_id) ??
          `#${row.credential_id}`,
        detail: groupMap.value.get(row.group_id)?.name,
      }))
    case 'group':
      return props.breakdowns.group.map((row) => {
        const group = groupMap.value.get(row.group_id)
        return {
          key: String(row.group_id),
          row,
          name: group?.name ?? `#${row.group_id}`,
          channel: group,
        }
      })
    case 'access_key':
      return props.breakdowns.access_key.map((row) => ({
        key: String(row.access_key_id),
        row,
        name: keyMap.value.get(row.access_key_id)?.name ?? `#${row.access_key_id}`,
      }))
    default:
      return props.breakdowns.model.map((row) => ({
        key: row.model,
        row,
        name: row.model || t('cacheAffinity.breakdown.unknownModel'),
      }))
  }
})

// 默认按请求数降序；客户端排序稳定，相同数值保持服务端顺序。
const sort = ref<{ key: AffinitySortKey; direction: SortDirection }>({
  key: 'request_count',
  direction: 'desc',
})
const sorted = computed(() => {
  const { key, direction } = sort.value
  const sign = direction === 'asc' ? 1 : -1
  return [...lines.value].sort((a, b) => sign * (a.row[key] - b.row[key]))
})
function toggleSort(key: AffinitySortKey): void {
  const direction: SortDirection =
    sort.value.key === key && sort.value.direction === 'desc' ? 'asc' : 'desc'
  sort.value = { key, direction }
}
function ariaSort(key: AffinitySortKey): 'ascending' | 'descending' | 'none' {
  if (sort.value.key !== key) return 'none'
  return sort.value.direction === 'asc' ? 'ascending' : 'descending'
}
function sortIcon(key: AffinitySortKey): Component {
  if (sort.value.key !== key) return ArrowUpDown
  return sort.value.direction === 'asc' ? ArrowUp : ArrowDown
}
</script>

<template>
  <div class="modern-cache-affinity-breakdown">
    <AppSegmentedControl
      class="modern-cache-affinity-breakdown-tabs"
      :label="t('cacheAffinity.breakdown.title')"
      :model-value="dimension"
      :options="dimensionOptions"
      @update:model-value="selectDimension"
    />
    <p v-if="!sorted.length" class="modern-cache-affinity-breakdown-empty">
      {{ t('cacheAffinity.breakdown.empty') }}
    </p>
    <div
      v-else
      class="modern-cache-affinity-breakdown-scroll"
      role="region"
      tabindex="0"
      :aria-label="t('cacheAffinity.breakdown.dimensions.' + dimension)"
    >
      <table class="modern-cache-affinity-breakdown-table">
        <thead>
          <tr>
            <th scope="col">{{ t('cacheAffinity.breakdown.columns.name') }}</th>
            <th
              scope="col"
              class="modern-cache-affinity-breakdown-number"
              :aria-sort="ariaSort('request_count')"
            >
              <AppButton
                variant="text"
                size="xs"
                :icon="sortIcon('request_count')"
                @click="toggleSort('request_count')"
                >{{ t('cacheAffinity.breakdown.columns.requests') }}</AppButton
              >
            </th>
            <th scope="col" class="modern-cache-affinity-breakdown-number">
              {{ t('cacheAffinity.breakdown.columns.followUps') }}
            </th>
            <th scope="col" class="modern-cache-affinity-breakdown-number">
              {{ t('cacheAffinity.breakdown.columns.sticky') }}
            </th>
            <th
              scope="col"
              class="modern-cache-affinity-breakdown-number"
              :aria-sort="ariaSort('miss_count')"
            >
              <AppButton
                variant="text"
                size="xs"
                :icon="sortIcon('miss_count')"
                @click="toggleSort('miss_count')"
                >{{ t('cacheAffinity.breakdown.columns.misses') }}</AppButton
              >
            </th>
            <th scope="col" class="modern-cache-affinity-breakdown-number">
              {{ t('cacheAffinity.breakdown.columns.cacheRead') }}
            </th>
            <th scope="col" class="modern-cache-affinity-breakdown-number">
              {{ t('cacheAffinity.breakdown.columns.netSaving') }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="line in sorted" :key="line.key">
            <td class="modern-cache-affinity-breakdown-name">
              <div class="modern-cache-affinity-breakdown-identity">
                <AppChannelIcon
                  v-if="line.channel"
                  :icon="line.channel.channelIcon"
                  :mark="line.channel.channelMark"
                  :name="line.channel.channelName"
                  size="sm"
                />
                <span class="modern-cache-affinity-breakdown-label">
                  <AppOverflowText :text="line.name" />
                  <AppOverflowText
                    v-if="line.detail"
                    class="modern-cache-affinity-breakdown-detail"
                    :text="line.detail"
                  />
                </span>
              </div>
            </td>
            <td class="modern-cache-affinity-breakdown-number">
              {{ formatCompactNumber(line.row.request_count, locale) }}
            </td>
            <td class="modern-cache-affinity-breakdown-number">
              {{ formatCompactNumber(line.row.pinned_count, locale) }}
            </td>
            <td class="modern-cache-affinity-breakdown-number">
              {{ percentage(stickyRate(line.row), locale) }}
            </td>
            <td class="modern-cache-affinity-breakdown-number">
              {{ formatCompactNumber(line.row.miss_count, locale) }}
            </td>
            <td class="modern-cache-affinity-breakdown-number">
              {{ percentage(cacheReadShare(line.row), locale) }}
            </td>
            <td class="modern-cache-affinity-breakdown-number">
              {{ money(netSaving(line.row), locale) }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-if="lines.length === breakdownLimit" class="modern-cache-affinity-breakdown-note">
      {{ t('cacheAffinity.breakdown.truncated', { count: breakdownLimit }) }}
    </p>
  </div>
</template>

<style scoped>
.modern-cache-affinity-breakdown {
  display: grid;
  min-width: 0;
  gap: var(--modern-space-3);
}
.modern-cache-affinity-breakdown-tabs {
  max-width: 100%;
}
.modern-cache-affinity-breakdown-scroll {
  min-width: 0;
  overflow-x: auto;
}
.modern-cache-affinity-breakdown-scroll:focus-visible {
  outline: var(--modern-line-width) solid var(--modern-accent);
  outline-offset: calc(var(--modern-space-1) * -1);
}
.modern-cache-affinity-breakdown-table {
  width: 100%;
  min-width: 640px;
  border-collapse: collapse;
  text-align: left;
  font-size: var(--modern-font-size-secondary);
  font-variant-numeric: tabular-nums;
}
.modern-cache-affinity-breakdown-table th {
  padding-bottom: var(--modern-space-2);
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  font-weight: var(--modern-weight-regular);
  white-space: nowrap;
}
.modern-cache-affinity-breakdown-table td {
  border-top: var(--modern-line-width) solid
    color-mix(in srgb, var(--modern-border) 65%, var(--modern-surface));
  padding-block: var(--modern-space-2);
  vertical-align: middle;
}
.modern-cache-affinity-breakdown-table tbody tr:hover {
  background: var(--modern-control-hover);
}
.modern-cache-affinity-breakdown-number {
  text-align: right;
  white-space: nowrap;
}
.modern-cache-affinity-breakdown-name {
  width: 100%;
  max-width: 0;
}
.modern-cache-affinity-breakdown-identity {
  display: flex;
  align-items: center;
  gap: var(--modern-space-2);
  min-width: 0;
}
.modern-cache-affinity-breakdown-label {
  display: grid;
  flex: 1;
  min-width: 0;
}
.modern-cache-affinity-breakdown-detail {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
}
.modern-cache-affinity-breakdown-empty,
.modern-cache-affinity-breakdown-note {
  margin: 0;
  color: var(--modern-muted);
  font-size: var(--modern-font-size-secondary);
}
.modern-cache-affinity-breakdown-empty {
  padding-block: var(--modern-space-8);
  text-align: center;
}
</style>
