<script setup lang="ts">
import { computed, onMounted } from 'vue'
import TempChart, { type Series } from '@/components/Chart/TempChart.vue'
import { useAppStore } from '@/store'
import { formatBytes, formatDuration } from '@/utils/format'

// 监控大盘：上半部分为**真实**系统状态（来自 GET /api/v1/system/status），
// 下半部分曲线的数据源由 W1b（内嵌时序存储）落地后经 /api/v1/hosts/{id}/metrics 提供，
// 因此这里先渲染示例数据并在界面标注，避免误认为已经接上真实指标。
const store = useAppStore()
onMounted(() => void store.refreshSystem())

const cards = computed(() => [
  { label: '服务器总数', value: String(store.status?.host_count ?? '-'), tone: 'normal' },
  { label: '运行时长', value: formatDuration(store.status?.uptime_sec), tone: 'ok' },
  { label: '堆内存', value: formatBytes(store.status?.heap_bytes), tone: 'muted' },
  { label: 'Goroutine', value: String(store.status?.goroutines ?? '-'), tone: 'muted' }
])

const drivers = computed(() => ({
  metadata: String(store.status?.storage?.['metadata_driver'] ?? '-'),
  tsdb: String(store.status?.storage?.['tsdb_driver'] ?? '未接入')
}))

// 示例曲线（明确标注，不作为真实指标展示）
const demoSeries: Series[] = (() => {
  const now = Date.now()
  const build = (name: string, base: number, jitter: number): Series => ({
    name,
    points: Array.from({ length: 60 }, (_, i) => {
      const v = base + Math.sin(i / 6) * jitter + (Math.random() - 0.5) * jitter
      return [now - (60 - i) * 60_000, Math.round(v * 10) / 10] as [number, number]
    })
  })
  return [build('Socket0', 47, 3), build('Socket1', 52, 4), build('DIMM_A1', 61, 5)]
})()
</script>

<template>
  <div class="grid">
    <section class="mw-card">
      <h2>系统状态</h2>
      <div class="cards">
        <div v-for="c in cards" :key="c.label" class="stat" :class="c.tone">
          <div class="value">{{ c.value }}</div>
          <div class="label">{{ c.label }}</div>
        </div>
      </div>
      <p class="dim">
        元数据存储：<strong>{{ drivers.metadata }}</strong> ·
        时序存储：<strong>{{ drivers.tsdb }}</strong>
        <span v-if="store.lastError" class="mw-tag crit" style="margin-left: 8px">{{ store.lastError }}</span>
      </p>
    </section>

    <div>
      <div class="hint">
        <span class="mw-tag warn">示例数据</span>
        曲线数据源待 W1b（内嵌时序存储）落地后接入；届时将改为按主机查询真实采样。
      </div>
      <TempChart title="CPU / 内存温度（示例）" :series="demoSeries" unit="°C" :threshold-warn="65" :threshold-crit="80" />
    </div>
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  gap: 16px;
}
h2 {
  margin: 0 0 12px;
  font-size: 15px;
  font-weight: 600;
}
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 12px;
}
.stat {
  background: var(--mw-panel-2);
  border: 1px solid var(--mw-border);
  border-left-width: 3px;
  border-radius: 8px;
  padding: 10px 12px;
}
.stat .value {
  font-size: 22px;
  font-weight: 600;
}
.stat .label {
  color: var(--mw-text-dim);
  font-size: 12px;
}
.stat.normal {
  border-left-color: var(--mw-accent);
}
.stat.ok {
  border-left-color: var(--mw-ok);
}
.stat.muted {
  border-left-color: var(--mw-border);
}
.dim {
  color: var(--mw-text-dim);
  font-size: 12px;
  margin: 12px 0 0;
}
.hint {
  color: var(--mw-text-dim);
  font-size: 12px;
  margin-bottom: 8px;
}
</style>
