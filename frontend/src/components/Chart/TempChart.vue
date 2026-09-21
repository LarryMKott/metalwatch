<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { DataZoomComponent, GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

// 传感器曲线组件：按需注册 ECharts 模块，避免全量打包（全量约 1MB+）。
export interface Series {
  name: string
  /** [时间戳(ms), 数值] */
  points: [number, number][]
}

const props = withDefaults(
  defineProps<{
    title?: string
    series?: Series[]
    unit?: string
    thresholdWarn?: number | null
    thresholdCrit?: number | null
  }>(),
  { title: '', series: () => [], unit: '', thresholdWarn: null, thresholdCrit: null }
)

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, DataZoomComponent, CanvasRenderer])

const el = ref<HTMLDivElement | null>(null)
let chart: echarts.ECharts | null = null

function render() {
  if (!chart) return
  const crit = props.thresholdCrit ?? Number.POSITIVE_INFINITY
  const warn = props.thresholdWarn ?? Number.POSITIVE_INFINITY

  chart.setOption(
    {
      backgroundColor: 'transparent',
      grid: { left: 52, right: 16, top: 32, bottom: 46 },
      tooltip: { trigger: 'axis' },
      legend: { top: 0, textStyle: { color: '#94a3b8' }, icon: 'roundRect' },
      xAxis: {
        type: 'time',
        axisLine: { lineStyle: { color: '#334155' } },
        axisLabel: { color: '#94a3b8' },
        splitLine: { show: false }
      },
      yAxis: {
        type: 'value',
        name: props.unit,
        nameTextStyle: { color: '#94a3b8' },
        axisLabel: { color: '#94a3b8' },
        splitLine: { lineStyle: { color: '#1e293b' } }
      },
      dataZoom: [{ type: 'inside' }, { type: 'slider', height: 18, bottom: 8 }],
      visualMap: props.thresholdWarn
        ? {
            show: false,
            dimension: 1,
            // 监控习惯：越限用红/橙，正常用绿
            pieces: [
              { gt: crit, color: '#ef4444' },
              { gt: warn, lte: crit, color: '#f59e0b' },
              { lte: warn, color: '#22c55e' }
            ],
            seriesIndex: props.series.map((_, i) => i)
          }
        : undefined,
      series: props.series.map((s) => ({
        name: s.name,
        type: 'line' as const,
        showSymbol: false,
        smooth: true,
        lineStyle: { width: 1.6 },
        data: s.points
      }))
    },
    true
  )
}

function resize() {
  chart?.resize()
}

onMounted(() => {
  if (!el.value) return
  chart = echarts.init(el.value)
  render()
  window.addEventListener('resize', resize)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', resize)
  chart?.dispose()
  chart = null
})

watch(() => props.series, render, { deep: true })
</script>

<template>
  <section class="mw-card">
    <h3 v-if="title">{{ title }}</h3>
    <div ref="el" class="chart" />
  </section>
</template>

<style scoped>
h3 {
  margin: 0 0 6px;
  font-size: 14px;
  font-weight: 600;
  color: #cbd5e1;
}
.chart {
  width: 100%;
  height: 280px;
}
</style>
