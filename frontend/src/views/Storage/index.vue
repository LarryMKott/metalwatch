<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import BackendTable from '@/components/StorageSetting/BackendTable.vue'
import { useAppStore } from '@/store'

// 存储管理：数据全部来自 GET /api/v1/system/storage/backends（已实现），
// 展示「当前生效的驱动」与「全部可选后端及其可用状态」。
// 切换动作需要后端支持（W1c 的适配器实现），因此这里只做提示，不做假切换。
const store = useAppStore()
const notice = ref('')

onMounted(() => void store.refreshBackends())

const metadataDriver = computed(() => store.currentDrivers?.metadata_driver ?? '-')
const tsDriver = computed(() => store.currentDrivers?.tsdb_driver ?? '未接入')

function onSelect(name: string) {
  notice.value =
    `「${name}」适配器尚未实现，已记录你的选择。` +
    '切换步骤：先在应用设置中填写连接信息（db.dsn / timeseries.endpoint）→ 执行结构迁移 → 重启进程。'
}
</script>

<template>
  <div class="grid">
    <section class="mw-card">
      <h2>当前使用</h2>
      <p class="dim">
        元数据存储：<strong>{{ metadataDriver }}</strong> ·
        时序存储：<strong>{{ tsDriver }}</strong>
      </p>
      <p v-if="notice" class="notice">{{ notice }}</p>
    </section>

    <section class="mw-card">
      <h2>元数据存储后端</h2>
      <p class="dim">默认 SQLite（零部署）；其余后端支持独立部署，填写连接串即可接入。</p>
      <BackendTable
        :backends="store.backends"
        :current-driver="metadataDriver"
        kind="metadata"
        @select="onSelect"
      />
    </section>

    <section class="mw-card">
      <h2>时序存储后端</h2>
      <p class="dim">默认进程内嵌（无端口、无外部依赖）；也可指向独立部署的时序服务。</p>
      <BackendTable
        :backends="store.backends"
        :current-driver="tsDriver"
        kind="timeseries"
        @select="onSelect"
      />
    </section>
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  gap: 16px;
}
h2 {
  margin: 0 0 8px;
  font-size: 15px;
  font-weight: 600;
}
.dim {
  color: var(--mw-text-dim);
  font-size: 12px;
  margin: 0 0 10px;
}
.notice {
  margin: 10px 0 0;
  padding: 8px 10px;
  border: 1px solid var(--mw-border);
  border-left: 3px solid var(--mw-warn);
  border-radius: 6px;
  color: var(--mw-text-dim);
  font-size: 12px;
  background: var(--mw-panel-2);
}
</style>
