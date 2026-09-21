<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Message, type TableColumnData } from '@arco-design/web-vue'
import { getGeoIpStats } from '@/api/geoip'
import { formatTime } from '@/utils/format'
import type { GeoIpStats } from '@/types/api'

// 系统设置：GeoIP 库管理、用户权限、系统日志。
const loading = ref(false)
const stats = ref<GeoIpStats | null>(null)
const tab = ref('geoip')

const coverageColumns: TableColumnData[] = [
  { title: '国家/地区', dataIndex: 'country' },
  { title: '记录数', dataIndex: 'count', slotName: 'count' }
]

async function loadGeo() {
  loading.value = true
  try {
    stats.value = await getGeoIpStats()
  } catch (e) {
    stats.value = null
    Message.error((e as Error).message || '加载 GeoIP 统计失败')
  } finally {
    loading.value = false
  }
}

onMounted(loadGeo)
</script>

<template>
  <div class="page">
    <a-tabs v-model:active-key="tab">
      <a-tab-pane key="geoip" title="GeoIP 库管理">
        <a-card :bordered="false">
          <a-spin :loading="loading">
            <a-descriptions v-if="stats" :column="2" bordered size="medium">
              <a-descriptions-item label="库版本">{{ stats.library_version }}</a-descriptions-item>
              <a-descriptions-item label="构建日期">{{ stats.build_date || '-' }}</a-descriptions-item>
              <a-descriptions-item label="总记录数">{{ stats.total_records }}</a-descriptions-item>
              <a-descriptions-item label="覆盖国家/地区">{{ stats.country_count }}</a-descriptions-item>
              <a-descriptions-item label="更新时间">{{ stats.updated_at ? formatTime(stats.updated_at) : '-' }}</a-descriptions-item>
            </a-descriptions>
            <a-empty v-else description="暂无 GeoIP 数据" />
          </a-spin>
        </a-card>
        <a-card :bordered="false" title="覆盖分布" class="sm">
          <a-table :data="stats?.coverage_by_country ?? []" :columns="coverageColumns" :loading="loading" :pagination="false" row-key="country">
            <template #count="{ record }">{{ (record as GeoIpStats['coverage_by_country'][number]).count }}</template>
          </a-table>
        </a-card>
      </a-tab-pane>

      <a-tab-pane key="rbac" title="用户权限">
        <a-alert type="info">账号与角色、API Token 管理将在后续版本接入，当前展示占位。</a-alert>
      </a-tab-pane>

      <a-tab-pane key="log" title="系统日志">
        <a-alert type="info">系统日志（操作审计 / 运行日志）查看将在后续版本接入，当前展示占位。</a-alert>
      </a-tab-pane>
    </a-tabs>
  </div>
</template>

<style scoped>
.page {
  display: block;
}
.sm {
  margin-top: 16px;
}
</style>
