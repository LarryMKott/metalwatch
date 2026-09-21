<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Message, type TableColumnData } from '@arco-design/web-vue'
import { ackAlert, listAlerts, listAlertTemplates } from '@/api/alarm'
import { formatTime } from '@/utils/format'
import type { Alert, ThresholdTemplate } from '@/types/api'

// 告警中心：告警事件列表、阈值模板、维护窗口、通知渠道。
const loading = ref(false)
const alerts = ref<Alert[]>([])
const stateFilter = ref<'active' | 'all'>('active')
const tab = ref('active')

const columns: TableColumnData[] = [
  { title: '级别', dataIndex: 'severity', slotName: 'severity' },
  { title: '规则', dataIndex: 'rule' },
  { title: '主机', dataIndex: 'host_name', slotName: 'host' },
  { title: '状态', dataIndex: 'state', slotName: 'state' },
  { title: '触发时间', dataIndex: 'fired_at', slotName: 'fired' },
  { title: '当前值', dataIndex: 'value', slotName: 'value' },
  { title: '说明', dataIndex: 'message' },
  { title: '操作', slotName: 'action', width: 90 }
]

const severityTag: Record<string, string> = { critical: 'red', major: 'orange', minor: 'arcoblue', info: 'gray' }
const stateTag: Record<string, string> = { active: 'red', acked: 'gray', resolved: 'green', suppressed: 'gray' }

const filtered = computed(() => {
  const f = stateFilter.value
  return f === 'all' ? alerts.value : alerts.value.filter((a) => a.state === f)
})

const templates = ref<ThresholdTemplate[]>([])
const templatesLoading = ref(false)
const templateColumns: TableColumnData[] = [
  { title: '名称', dataIndex: 'name' },
  { title: '指标', dataIndex: 'metric' },
  { title: '条件', dataIndex: 'cond', slotName: 'cond' },
  { title: '级别', dataIndex: 'severity', slotName: 'sev' },
  { title: '持续', dataIndex: 'for_duration' },
  { title: '启用', dataIndex: 'enabled', slotName: 'enabled' }
]

async function load() {
  loading.value = true
  try {
    const f = stateFilter.value
    alerts.value = await listAlerts({ state: f === 'all' ? undefined : f })
  } catch (e) {
    Message.error((e as Error).message || '加载告警失败')
  } finally {
    loading.value = false
  }
}
async function loadTemplates() {
  templatesLoading.value = true
  try {
    templates.value = await listAlertTemplates()
  } catch (e) {
    templates.value = []
    Message.error((e as Error).message || '加载阈值模板失败')
  } finally {
    templatesLoading.value = false
  }
}
async function onAck(id: number) {
  try {
    await ackAlert(id)
    Message.success('已确认')
    await load()
  } catch (e) {
    Message.error((e as Error).message || '确认失败')
  }
}

onMounted(() => {
  void load()
  void loadTemplates()
})
</script>

<template>
  <div class="page">
    <a-tabs v-model:active-key="tab">
      <a-tab-pane key="active" title="告警事件">
        <a-card :bordered="false" class="toolbar">
          <a-space>
            <a-radio-group v-model="stateFilter" type="button" @change="load">
              <a-radio value="active">活跃</a-radio>
              <a-radio value="all">全部</a-radio>
            </a-radio-group>
            <a-button type="outline" @click="load">刷新</a-button>
          </a-space>
        </a-card>
        <a-card :bordered="false">
          <a-table :data="filtered" :columns="columns" :loading="loading" :pagination="false" row-key="id">
            <template #severity="{ record }">
              <a-tag :color="severityTag[(record as Alert).severity]">{{ (record as Alert).severity }}</a-tag>
            </template>
            <template #host="{ record }">{{ (record as Alert).host_name || '-' }}</template>
            <template #state="{ record }">
              <a-tag :color="stateTag[(record as Alert).state]">{{ (record as Alert).state }}</a-tag>
            </template>
            <template #fired="{ record }">{{ formatTime((record as Alert).fired_at) }}</template>
            <template #value="{ record }">{{ (record as Alert).value ?? '-' }}</template>
            <template #action="{ record }">
              <a-button
                v-if="(record as Alert).state === 'active'"
                size="mini"
                type="outline"
                @click="onAck((record as Alert).id)"
              >
                确认
              </a-button>
              <span v-else class="dim">—</span>
            </template>
          </a-table>
        </a-card>
      </a-tab-pane>

      <a-tab-pane key="templates" title="阈值模板">
        <a-card :bordered="false">
          <a-table :data="templates" :columns="templateColumns" :loading="templatesLoading" :pagination="false" row-key="id">
            <template #cond="{ record }">
              {{ (record as ThresholdTemplate).metric }} {{ (record as ThresholdTemplate).op }} {{ (record as ThresholdTemplate).threshold }}
            </template>
            <template #sev="{ record }">
              <a-tag :color="severityTag[(record as ThresholdTemplate).severity]">{{ (record as ThresholdTemplate).severity }}</a-tag>
            </template>
            <template #enabled="{ record }">
              <a-switch :model-value="(record as ThresholdTemplate).enabled" disabled />
            </template>
          </a-table>
        </a-card>
      </a-tab-pane>

      <a-tab-pane key="maintenance" title="维护窗口">
        <a-alert type="info">维护窗口（维护期内抑制告警）将在后续版本接入，当前展示占位。</a-alert>
      </a-tab-pane>

      <a-tab-pane key="notify" title="通知渠道">
        <a-alert type="info">通知渠道（邮件 / Webhook / 企业微信）配置将在后续版本接入，当前展示占位。</a-alert>
      </a-tab-pane>
    </a-tabs>
  </div>
</template>

<style scoped>
.page {
  display: block;
}
.toolbar {
  padding: 12px 16px;
  margin-bottom: 16px;
}
.dim {
  color: var(--mw-text-dim);
}
</style>
