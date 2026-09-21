<script setup lang="ts">
import type { StorageBackend } from '@/types/api'

// 存储后端动态表单：把后端 adapter.Catalog 的可用状态直接渲染出来，
// 明确区分「已实现」与「规划中」，避免用户切到尚未接通的驱动。
const props = defineProps<{
  backends: StorageBackend[]
  currentDriver: string
  kind: 'metadata' | 'timeseries'
}>()

defineEmits<{ (e: 'select', name: string): void }>()

const rows = (): StorageBackend[] => props.backends.filter((b) => b.kind === props.kind)
</script>

<template>
  <table class="mw-table">
    <thead>
      <tr>
        <th>驱动</th>
        <th>部署形态</th>
        <th>状态</th>
        <th>说明</th>
        <th></th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="b in rows()" :key="b.name">
        <td>
          <strong>{{ b.name }}</strong>
          <span v-if="b.name === currentDriver" class="mw-tag ok" style="margin-left: 6px">当前</span>
        </td>
        <td>
          <span class="mw-tag">{{ b.deployment === 'builtin' ? '随应用内嵌' : '独立部署' }}</span>
        </td>
        <td>
          <span class="mw-tag" :class="b.implemented ? 'ok' : 'warn'">
            {{ b.implemented ? '可用' : '规划中' }}
          </span>
          <span v-if="!b.implemented && b.work_item" class="dim" style="margin-left: 6px">{{ b.work_item }}</span>
        </td>
        <td style="color: var(--mw-text-dim)">{{ b.note }}</td>
        <td>
          <button class="mw-btn" :disabled="!b.implemented || b.name === currentDriver" @click="$emit('select', b.name)">
            {{ b.name === currentDriver ? '使用中' : '切换' }}
          </button>
        </td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.dim {
  color: var(--mw-text-dim);
  font-size: 12px;
}
</style>
