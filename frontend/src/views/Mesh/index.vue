<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Message, type TableColumnData } from '@arco-design/web-vue'
import BaseChart from '@/components/Chart/BaseChart.vue'
import { getMeshRoutes, getMeshTopology } from '@/api/mesh'
import type { MeshRoutes, MeshTopology } from '@/types/api'

// Mesh 拓扑：网络拓扑可视化（ECharts graph）、链路状态、路由表查看。
const loading = ref(false)
const topo = ref<MeshTopology | null>(null)
const routes = ref<MeshRoutes | null>(null)

const linkColumns: TableColumnData[] = [
  { title: '源', dataIndex: 'from' },
  { title: '目标', dataIndex: 'to' },
  { title: 'RTT', dataIndex: 'rtt_ms', slotName: 'rtt' },
  { title: '丢包率', dataIndex: 'loss_rate', slotName: 'loss' }
]
const routeColumns: TableColumnData[] = [
  { title: '目的节点', dataIndex: 'dst_agent' },
  { title: '下一跳', dataIndex: 'next_hop' },
  { title: '跳数', dataIndex: 'hop_count' },
  { title: '开销', dataIndex: 'cost' },
  { title: '路径', dataIndex: 'path', slotName: 'path' }
]

const graphOption = computed(() => {
  const t = topo.value
  if (!t) return { series: [] }
  const nodes = t.nodes.map((n) => ({
    id: n.agent_id,
    name: n.hostname,
    category: n.online ? 0 : 1,
    symbolSize: 30,
    value: n.ip
  }))
  const links = t.links.map((l) => ({ source: l.from, target: l.to, value: `${l.rtt_ms}ms` }))
  return {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'item' },
    legend: [{ data: ['在线', '离线'], textStyle: { color: '#94a3b8' }, top: 4 }],
    series: [
      {
        type: 'graph',
        layout: 'force',
        roam: true,
        label: { show: true, color: '#cbd5e1' },
        categories: [{ name: '在线' }, { name: '离线' }],
        force: { repulsion: 160, edgeLength: 130 },
        lineStyle: { color: '#475569', curveness: 0.1 },
        emphasis: { focus: 'adjacency' },
        data: nodes,
        links
      }
    ]
  }
})

async function load() {
  loading.value = true
  try {
    const [t, r] = await Promise.all([getMeshTopology(), getMeshRoutes()])
    topo.value = t
    routes.value = r
  } catch (e) {
    Message.error((e as Error).message || '加载 Mesh 数据失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <a-card :bordered="false" title="网络拓扑">
      <a-spin :loading="loading">
        <BaseChart :option="graphOption" height="420px" />
        <a-empty v-if="!topo" description="暂无拓扑数据" />
      </a-spin>
    </a-card>

    <a-row :gutter="16">
      <a-col :xs="24" :lg="12">
        <a-card :bordered="false" title="链路状态">
          <a-table
            :data="topo?.links ?? []"
            :columns="linkColumns"
            :loading="loading"
            :pagination="false"
            row-key="from"
          >
            <template #rtt="{ record }">{{ (record as MeshTopology['links'][number]).rtt_ms }} ms</template>
            <template #loss="{ record }">{{ ((record as MeshTopology['links'][number]).loss_rate * 100).toFixed(1) }}%</template>
          </a-table>
        </a-card>
      </a-col>
      <a-col :xs="24" :lg="12">
        <a-card :bordered="false" title="路由表">
          <a-table
            :data="routes?.entries ?? []"
            :columns="routeColumns"
            :loading="loading"
            :pagination="false"
            row-key="dst_agent"
          >
            <template #path="{ record }">{{ (record as MeshRoutes['entries'][number]).path.join(' → ') }}</template>
          </a-table>
          <p v-if="routes" class="dim">
            版本 {{ routes.version }} · 计算于 {{ routes.computed_at }} · 最大跳数 {{ routes.max_hops }}
          </p>
        </a-card>
      </a-col>
    </a-row>
  </div>
</template>

<style scoped>
.page {
  display: grid;
  gap: 16px;
}
.dim {
  color: var(--mw-text-dim);
  font-size: 12px;
  margin: 10px 0 0;
}
</style>
