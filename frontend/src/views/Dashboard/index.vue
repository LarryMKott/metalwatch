<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import BaseChart from '@/components/Chart/BaseChart.vue'
import { getOverview } from '@/api/overview'
import { listHosts } from '@/api/host'
import { useAppStore } from '@/store'
import { formatDuration } from '@/utils/format'
import type { Host, Overview } from '@/types/api'

// 监控大盘：资产总览、在线状态、故障统计、告警趋势、地域分布。
// 数据来自 GET /api/v1/overview 与 GET /api/v1/hosts（地域分布由资产列表聚合）。
const store = useAppStore()
const overview = ref<Overview | null>(null)
const hosts = ref<Host[]>([])
const loading = ref(false)

const stats = computed(() => [
  { label: '资产总数', value: overview.value?.host_total ?? 0, tone: 'arcoblue' },
  { label: '在线', value: overview.value?.host_online ?? 0, tone: 'green' },
  { label: '离线', value: overview.value?.host_offline ?? 0, tone: 'red' },
  { label: '活跃告警', value: overview.value?.alert_active ?? 0, tone: 'orange' },
  { label: '运行时长', value: formatDuration(store.status?.uptime_sec), tone: 'gray' }
])

const AXIS = { axisLine: { lineStyle: { color: '#334155' } }, axisLabel: { color: '#94a3b8' } }
const SPLIT = { splitLine: { lineStyle: { color: '#1e293b' } } }

const trendOption = computed(() => {
  const s = overview.value?.series ?? []
  return {
    backgroundColor: 'transparent',
    grid: { left: 48, right: 16, top: 36, bottom: 28 },
    tooltip: { trigger: 'axis' },
    legend: { top: 0, textStyle: { color: '#94a3b8' }, data: ['在线', '离线', '告警'] },
    xAxis: { type: 'time', ...AXIS },
    yAxis: { type: 'value', ...AXIS, ...SPLIT },
    series: [
      { name: '在线', type: 'line', smooth: true, showSymbol: false, data: s.map((p) => [p.ts, p.online]), itemStyle: { color: '#22c55e' }, lineStyle: { width: 1.6 } },
      { name: '离线', type: 'line', smooth: true, showSymbol: false, data: s.map((p) => [p.ts, p.offline]), itemStyle: { color: '#ef4444' }, lineStyle: { width: 1.6 } },
      { name: '告警', type: 'line', smooth: true, showSymbol: false, data: s.map((p) => [p.ts, p.alert]), itemStyle: { color: '#f59e0b' }, lineStyle: { width: 1.6 } }
    ]
  }
})

const geoOption = computed(() => {
  const map = new Map<string, number>()
  for (const h of hosts.value) {
    const c = h.geo_country || '未知'
    map.set(c, (map.get(c) ?? 0) + 1)
  }
  const data = [...map.entries()].map(([name, value]) => ({ name, value }))
  return {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'item' },
    legend: { show: false },
    series: [
      {
        name: '地域分布',
        type: 'pie',
        radius: ['38%', '68%'],
        center: ['50%', '52%'],
        label: { color: '#cbd5e1' },
        labelLine: { lineStyle: { color: '#475569' } },
        data: data.length ? data : [{ name: '暂无数据', value: 1, itemStyle: { color: '#1e293b' } }]
      }
    ]
  }
})

async function load() {
  loading.value = true
  try {
    const [ov, hs] = await Promise.all([getOverview(), listHosts({ limit: 500, offset: 0 })])
    overview.value = ov
    hosts.value = hs.items
  } catch (e) {
    Message.error((e as Error).message || '加载大盘数据失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <a-spin :loading="loading" tip="加载中…" class="page-spin">
    <a-row :gutter="16" class="cards">
      <a-col v-for="s in stats" :key="s.label" :xs="12" :sm="8" :md="8" :lg="4">
        <a-card class="stat" :bordered="false">
          <a-statistic v-if="typeof s.value === 'number'" :title="s.label" :value="s.value" :value-from="0" animation />
          <div v-else class="text-stat">
            <div class="t-val">{{ s.value }}</div>
            <div class="t-label">{{ s.label }}</div>
          </div>
        </a-card>
      </a-col>
    </a-row>

    <a-row :gutter="16" class="charts">
      <a-col :xs="24" :lg="14">
        <a-card title="告警趋势" :bordered="false">
          <BaseChart :option="trendOption" height="300px" />
        </a-card>
      </a-col>
      <a-col :xs="24" :lg="10">
        <a-card title="地域分布" :bordered="false">
          <BaseChart :option="geoOption" height="300px" />
        </a-card>
      </a-col>
    </a-row>

    <a-alert v-if="store.lastError" type="warning" class="warn">
      后端返回异常：{{ store.lastError }}（可能为后端未启动；界面已按空数据渲染）
    </a-alert>
  </a-spin>
</template>

<style scoped>
.page-spin {
  width: 100%;
}
.cards {
  margin-bottom: 16px;
}
.stat :deep(.arco-statistic-title) {
  color: var(--mw-text-dim);
}
.text-stat .t-val {
  font-size: 22px;
  font-weight: 600;
  color: var(--mw-text);
}
.text-stat .t-label {
  color: var(--mw-text-dim);
  font-size: 12px;
}
.charts {
  margin-bottom: 16px;
}
.warn {
  margin-top: 8px;
}
</style>
