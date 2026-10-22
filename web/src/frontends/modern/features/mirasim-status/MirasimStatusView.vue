<script setup lang="ts">
import { useTemplateRef } from 'vue'

import { usePageRefresh } from '@modern/app/page-refresh'
import MirasimStatusBoard from '@shared/mirasim-status/MirasimStatusView.vue'
import { timestampMs } from '@shared/mirasim-status/format'

const board = useTemplateRef('board')
usePageRefresh({
  refresh: () => board.value?.refresh(),
  pending: () => board.value?.isFetching ?? false,
  updatedAt: () => timestampMs(board.value?.response?.fetched_at),
})
</script>

<template>
  <MirasimStatusBoard ref="board" />
</template>
