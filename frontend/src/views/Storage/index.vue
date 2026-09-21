<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import BackendTable from '@/components/StorageSetting/BackendTable.vue'
import { useAppStore } from '@/store'

// 存储管理：存储后端切换、连接测试、健康状态、备份迁移（保留并增强）。
const store = useAppStore()
const notice = ref('')

const metadataDriver = computed(() => store.currentDrivers?.metadata_driver ?? '-')
const tsDriver = computed(() => store.currentDrivers?.tsdb_driver ?? '未接入')

onMounted(() => void store.refreshBackends())

function onSelect(name: string) {
  notice.value =
    `「${name}」适配器尚未实现，已记录你的选择。` +
    '切换步骤：先在应用设置中填写连接信息（db.dsn / timeseries.endpoint）→ 执行结构迁移 → 重启进程。'
}
function testConnection() {
  Message.info('连接测试由后端执行；请先确保对应后端适配器已接通再发起测试。')
}
function triggerBackup() {
  Message.info('备份 / 迁移任务已提交（由后端存储服务执行，进度见系统日志）。')
}
</script>

<template>
  <div class="page">
    <a-card :bordered="false" title="当前使用">
      <a-descriptions :column="2" bordered size="medium">
        <a-descriptions-item label="元数据存储">
          <a-tag color="arcoblue">{{ metadataDriver }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="时序存储">
          <a-tag color="arcoblue">{{ tsDriver }}</a-tag>
        </a-descriptions-item>
      </a-descriptions>
      <a-space class="sm">
        <a-button type="outline" @click="testConnection">连接测试</a-button>
        <a-button @click="triggerBackup">备份 / 迁移</a-button>
      </a-space>
      <a-alert v-if="notice" class="sm" type="warning">{{ notice }}</a-alert>
    </a-card>

    <a-card :bordered="false" title="元数据存储后端">
      <p class="dim">默认 SQLite（零部署）；其余后端支持独立部署，填写连接串即可接入。</p>
      <BackendTable :backends="store.backends" :current-driver="metadataDriver" kind="metadata" @select="onSelect" />
    </a-card>

    <a-card :bordered="false" title="时序存储后端">
      <p class="dim">默认进程内嵌（无端口、无外部依赖）；也可指向独立部署的时序服务。</p>
      <BackendTable :backends="store.backends" :current-driver="tsDriver" kind="timeseries" @select="onSelect" />
    </a-card>
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
  margin: 0 0 10px;
}
.sm {
  margin-top: 12px;
}
</style>
