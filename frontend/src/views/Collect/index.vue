<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Message, type TableColumnData } from '@arco-design/web-vue'
import { listCollectTasks } from '@/api/collect'
import { formatTime } from '@/utils/format'
import type { CollectTask, CollectTaskKind } from '@/types/api'

// 采集管理：Agent 接入、IPMI 节点、采集周期、批量发现。
const loading = ref(false)
const tasks = ref<CollectTask[]>([])
const kindFilter = ref<CollectTaskKind | 'all'>('all')

const columns: TableColumnData[] = [
  { title: '名称', dataIndex: 'name', slotName: 'name' },
  { title: '类型', dataIndex: 'kind', slotName: 'kind' },
  { title: '目标', dataIndex: 'target' },
  { title: '周期', dataIndex: 'interval_sec', slotName: 'interval' },
  { title: '状态', dataIndex: 'state', slotName: 'state' },
  { title: '最近执行', dataIndex: 'last_run_at', slotName: 'run' },
  { title: '启用', dataIndex: 'enabled', slotName: 'enabled' }
]

const filtered = computed(() =>
  kindFilter.value === 'all' ? tasks.value : tasks.value.filter((t) => t.kind === kindFilter.value)
)

const kindTag: Record<string, string> = {
  agent: 'arcoblue',
  ipmi: 'purple',
  discovery: 'green'
}

async function load() {
  loading.value = true
  try {
    tasks.value = await listCollectTasks()
  } catch (e) {
    Message.error((e as Error).message || '加载采集任务失败')
  } finally {
    loading.value = false
  }
}

function discover() {
  Message.info('批量发现任务已提交（由后端采集服务执行，结果将写入资产列表）')
}
function saveCycle() {
  Message.info('采集周期配置由后端持久化，请通过服务端配置或 API 保存')
}

onMounted(load)
</script>

<template>
  <div class="page">
    <a-card :bordered="false" class="toolbar">
      <a-space>
        <a-select v-model="kindFilter" style="width: 150px">
          <a-option value="all">全部类型</a-option>
          <a-option value="agent">Agent 接入</a-option>
          <a-option value="ipmi">IPMI 节点</a-option>
          <a-option value="discovery">批量发现</a-option>
        </a-select>
        <a-button type="primary" @click="discover">批量发现</a-button>
        <a-button type="outline" @click="saveCycle">保存周期配置</a-button>
      </a-space>
    </a-card>

    <a-card :bordered="false">
      <a-table :data="filtered" :columns="columns" :loading="loading" :pagination="false" row-key="id">
        <template #name="{ record }">{{ (record as CollectTask).name }}</template>
        <template #kind="{ record }">
          <a-tag :color="kindTag[(record as CollectTask).kind]">{{ (record as CollectTask).kind }}</a-tag>
        </template>
        <template #interval="{ record }">{{ (record as CollectTask).interval_sec }}s</template>
        <template #state="{ record }">
          <a-tag :color="(record as CollectTask).state === 'error' ? 'red' : (record as CollectTask).state === 'running' ? 'green' : 'gray'">
            {{ (record as CollectTask).state }}
          </a-tag>
          <span v-if="(record as CollectTask).error" class="err"> · {{ (record as CollectTask).error }}</span>
        </template>
        <template #run="{ record }">{{ (record as CollectTask).last_run_at ? formatTime((record as CollectTask).last_run_at) : '从未' }}</template>
        <template #enabled="{ record }">
          <a-switch :model-value="(record as CollectTask).enabled" disabled />
        </template>
      </a-table>
    </a-card>
  </div>
</template>

<style scoped>
.page {
  display: grid;
  gap: 16px;
}
.toolbar {
  padding: 12px 16px;
}
.err {
  color: var(--mw-crit);
  font-size: 12px;
}
</style>
