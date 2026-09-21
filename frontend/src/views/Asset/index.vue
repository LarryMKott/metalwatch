<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { Message, type TableColumnData } from '@arco-design/web-vue'
import BaseChart from '@/components/Chart/BaseChart.vue'
import { getHostChanges, getHostMetrics, listHosts } from '@/api/host'
import { formatTime, fromNow, statusTone } from '@/utils/format'
import type { ChangeRecord, Host, HostStatus, MetricSeries } from '@/types/api'

// 资产管理：服务器列表、硬件详情、传感器时序曲线、硬件变更记录。
const loading = ref(false)
const data = ref<Host[]>([])
const q = ref('')
const stateFilter = ref<HostStatus | 'all'>('all')

const pagination = reactive({ total: 0, current: 1, pageSize: 20, showTotal: true })

const columns: TableColumnData[] = [
  { title: '主机名', dataIndex: 'hostname', slotName: 'hostname' },
  { title: 'IP', dataIndex: 'primary_ip' },
  { title: '状态', dataIndex: 'status', slotName: 'status' },
  { title: '机房', dataIndex: 'site' },
  { title: '系统', dataIndex: 'os_type', slotName: 'os' },
  { title: 'Agent', dataIndex: 'collect_agent', slotName: 'agent' },
  { title: '最近上报', dataIndex: 'last_seen_at', slotName: 'seen' }
]

const toneClass: Record<string, string> = { ok: 'tone-ok', warn: 'tone-warn', crit: 'tone-crit', muted: 'tone-muted' }

async function load() {
  loading.value = true
  try {
    const resp = await listHosts({
      q: q.value || undefined,
      state: stateFilter.value === 'all' ? undefined : stateFilter.value,
      limit: pagination.pageSize,
      offset: (pagination.current - 1) * pagination.pageSize
    })
    data.value = resp.items
    pagination.total = resp.total
  } catch (e) {
    Message.error((e as Error).message || '加载资产列表失败')
  } finally {
    loading.value = false
  }
}

function onSearch() {
  pagination.current = 1
  void load()
}
function onPageChange(page: number) {
  pagination.current = page
  void load()
}
function onPageSizeChange(size: number) {
  pagination.pageSize = size
  pagination.current = 1
  void load()
}

// ---- 详情抽屉 ----
const drawerVisible = ref(false)
const current = ref<Host | null>(null)
const tab = ref('overview')
const changes = ref<ChangeRecord[]>([])
const changesLoading = ref(false)

const metricOptions = [
  { label: 'CPU 温度', value: 'cpu_temp_celsius' },
  { label: '内存温度', value: 'mem_temp_celsius' },
  { label: 'CPU 利用率', value: 'cpu_usage_percent' },
  { label: '内存利用率', value: 'mem_usage_percent' },
  { label: '磁盘温度', value: 'disk_temp_celsius' }
]
const rangeOptions = [
  { label: '近 1 小时', value: 3600 },
  { label: '近 6 小时', value: 21600 },
  { label: '近 24 小时', value: 86400 }
]
const metric = ref('cpu_temp_celsius')
const range = ref(3600)
const series = ref<MetricSeries | null>(null)
const metricLoading = ref(false)

const sensorOption = computed(() => {
  const pts = series.value?.points ?? []
  return {
    backgroundColor: 'transparent',
    grid: { left: 52, right: 16, top: 24, bottom: 40 },
    tooltip: { trigger: 'axis' },
    xAxis: { type: 'time', axisLine: { lineStyle: { color: '#334155' } }, axisLabel: { color: '#94a3b8' } },
    yAxis: { type: 'value', name: series.value?.unit ?? '', nameTextStyle: { color: '#94a3b8' }, axisLabel: { color: '#94a3b8' }, splitLine: { lineStyle: { color: '#1e293b' } } },
    dataZoom: [{ type: 'inside' }, { type: 'slider', height: 16, bottom: 8 }],
    series: [{ name: metric.value, type: 'line', smooth: true, showSymbol: false, data: pts, lineStyle: { width: 1.6, color: '#38bdf8' }, itemStyle: { color: '#38bdf8' } }]
  }
})

async function openDetail(row: Host) {
  current.value = row
  drawerVisible.value = true
  tab.value = 'overview'
  series.value = null
  await loadChanges(row.id)
}
function onRowClick(record: unknown) {
  void openDetail(record as Host)
}
async function loadChanges(id: number) {
  changesLoading.value = true
  try {
    changes.value = await getHostChanges(id)
  } catch (e) {
    changes.value = []
    Message.error((e as Error).message || '加载变更记录失败')
  } finally {
    changesLoading.value = false
  }
}
async function loadMetrics() {
  if (!current.value) return
  metricLoading.value = true
  try {
    const to = new Date()
    const from = new Date(to.getTime() - range.value * 1000)
    series.value = await getHostMetrics(current.value.id, {
      metric: metric.value,
      from: from.toISOString(),
      to: to.toISOString(),
      step: range.value <= 3600 ? '60s' : '300s'
    })
  } catch (e) {
    series.value = null
    Message.error((e as Error).message || '加载时序曲线失败')
  } finally {
    metricLoading.value = false
  }
}

const changeColumns: TableColumnData[] = [
  { title: '检测时间', dataIndex: 'detected_at', slotName: 'detected_at' },
  { title: '类别', dataIndex: 'category' },
  { title: '字段', dataIndex: 'field' },
  { title: '旧值', dataIndex: 'old_value', slotName: 'old' },
  { title: '新值', dataIndex: 'new_value', slotName: 'new' },
  { title: '来源', dataIndex: 'source' }
]

onMounted(load)
</script>

<template>
  <div class="page">
    <a-card :bordered="false" class="toolbar">
      <a-space>
        <a-input v-model="q" placeholder="搜索主机名 / IP" allow-clear style="width: 240px" @press-enter="onSearch" />
        <a-select v-model="stateFilter" style="width: 140px">
          <a-option value="all">全部状态</a-option>
          <a-option value="online">在线</a-option>
          <a-option value="offline">离线</a-option>
          <a-option value="unknown">未知</a-option>
        </a-select>
        <a-button type="primary" @click="onSearch">查询</a-button>
      </a-space>
    </a-card>

    <a-card :bordered="false">
      <a-table
        :data="data"
        :columns="columns"
        :loading="loading"
        :pagination="pagination"
        row-key="id"
        :row-class="() => 'row-click'"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
        @row-click="onRowClick"
      >
        <template #hostname="{ record }">
          <a-link @click.stop="openDetail(record as Host)">{{ (record as Host).hostname }}</a-link>
        </template>
        <template #status="{ record }">
          <span :class="['dot', toneClass[statusTone((record as Host).status)]]" />
          {{ (record as Host).status }}
        </template>
        <template #os="{ record }">
          {{ (record as Host).os_type }}{{ (record as Host).os_version ? ' ' + (record as Host).os_version : '' }}
        </template>
        <template #agent="{ record }">
          <a-tag :color="(record as Host).collect_agent ? 'green' : 'gray'">
            {{ (record as Host).collect_agent ? '已接入' : '未接入' }}
          </a-tag>
        </template>
        <template #seen="{ record }">
          {{ fromNow((record as Host).last_seen_at) }}
        </template>
      </a-table>
    </a-card>

    <a-drawer
      v-model:visible="drawerVisible"
      :width="720"
      :title="current ? current.hostname : '资产详情'"
      :footer="false"
    >
      <a-tabs v-model:active-key="tab">
        <a-tab-pane key="overview" title="硬件概览">
          <a-descriptions v-if="current" :column="2" bordered size="medium">
            <a-descriptions-item label="主机名">{{ current.hostname }}</a-descriptions-item>
            <a-descriptions-item label="IP">{{ current.primary_ip }}</a-descriptions-item>
            <a-descriptions-item label="BMC IP">{{ current.bmc_ip || '-' }}</a-descriptions-item>
            <a-descriptions-item label="序列号">{{ current.sn || '-' }}</a-descriptions-item>
            <a-descriptions-item label="机房/机柜">{{ current.site || '-' }}{{ current.rack ? ' / ' + current.rack : '' }}</a-descriptions-item>
            <a-descriptions-item label="状态">{{ current.status }}</a-descriptions-item>
            <a-descriptions-item label="系统">{{ current.os_type }}{{ current.os_version ? ' ' + current.os_version : '' }}</a-descriptions-item>
            <a-descriptions-item label="Agent 版本">{{ current.agent_version || '-' }}</a-descriptions-item>
            <a-descriptions-item label="IPMI 采集">{{ current.collect_ipmi ? '是' : '否' }}</a-descriptions-item>
            <a-descriptions-item label="地域">{{ current.geo_country || '-' }}</a-descriptions-item>
            <a-descriptions-item label="创建时间">{{ formatTime(current.created_at) }}</a-descriptions-item>
            <a-descriptions-item label="更新时间">{{ formatTime(current.updated_at) }}</a-descriptions-item>
          </a-descriptions>
          <a-alert class="sm" type="info">SMART 详情随硬件采集能力落地后在「传感器」页补充。</a-alert>
        </a-tab-pane>

        <a-tab-pane key="sensor" title="传感器曲线">
          <a-space class="sm">
            <a-select v-model="metric" :options="metricOptions" style="width: 180px" @change="loadMetrics" />
            <a-select v-model="range" :options="rangeOptions" style="width: 150px" @change="loadMetrics" />
            <a-button type="outline" :loading="metricLoading" @click="loadMetrics">刷新</a-button>
          </a-space>
          <a-spin :loading="metricLoading" class="chart-wrap">
            <BaseChart v-if="series" :option="sensorOption" height="300px" />
            <a-empty v-else description="暂无时序数据" />
          </a-spin>
        </a-tab-pane>

        <a-tab-pane key="changes" title="硬件变更记录">
          <a-table
            :data="changes"
            :columns="changeColumns"
            :loading="changesLoading"
            :pagination="false"
            row-key="id"
          >
            <template #detected_at="{ record }">{{ formatTime((record as ChangeRecord).detected_at) }}</template>
            <template #old="{ record }">{{ (record as ChangeRecord).old_value || '-' }}</template>
            <template #new="{ record }">{{ (record as ChangeRecord).new_value || '-' }}</template>
          </a-table>
        </a-tab-pane>
      </a-tabs>
    </a-drawer>
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
.row-click {
  cursor: pointer;
}
.dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 6px;
}
.tone-ok {
  background: var(--mw-ok);
}
.tone-warn {
  background: var(--mw-warn);
}
.tone-crit {
  background: var(--mw-crit);
}
.tone-muted {
  background: var(--mw-text-dim);
}
.sm {
  margin-top: 12px;
}
.chart-wrap {
  margin-top: 12px;
  display: block;
}
</style>
