import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getStorageBackends, getSystemStatus } from '@/api/system'
import type { StorageBackend, SystemStatus } from '@/types/api'

// 全局应用状态：系统健康、存储后端目录、侧边栏折叠等跨页共享状态。
export const useAppStore = defineStore('app', () => {
  const status = ref<SystemStatus | null>(null)
  const backends = ref<StorageBackend[]>([])
  const currentDrivers = ref<{ metadata_driver: string; tsdb_driver: string } | null>(null)
  const loading = ref(false)
  const lastError = ref<string | null>(null)

  async function refreshSystem() {
    loading.value = true
    lastError.value = null
    try {
      status.value = await getSystemStatus()
    } catch (e) {
      lastError.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  async function refreshBackends() {
    try {
      const resp = await getStorageBackends()
      backends.value = resp.backends
      currentDrivers.value = resp.current
    } catch (e) {
      lastError.value = (e as Error).message
    }
  }

  return { status, backends, currentDrivers, loading, lastError, refreshSystem, refreshBackends }
})
