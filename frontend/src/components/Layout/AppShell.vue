<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import { useAppStore } from '@/store'
import { fromNow } from '@/utils/format'
import { AlertSocket, type AlertEvent } from '@/utils/websocket'

// 应用外壳：左侧导航 + 顶部状态 + 内容区。
// 顶部状态直接反映后端 /healthz 与 /system/status，便于在飞牛应用面板之外自查。
const store = useAppStore()

const nav = [
  { to: '/dashboard', label: '监控大盘' },
  { to: '/asset', label: '硬件资产' },
  { to: '/collect', label: '采集配置' },
  { to: '/storage', label: '存储管理' },
  { to: '/alarm', label: '告警中心' },
  { to: '/geoip', label: '地理库' },
  { to: '/report', label: '巡检报表' },
  { to: '/setting', label: '系统设置' }
]

const driver = computed(() => store.currentDrivers?.metadata_driver ?? '-')

let timer: number | null = null
let socket: AlertSocket | null = null

function onAlertEvent(evt: AlertEvent) {
  // 收到关键事件就刷新顶部状态，让运维无需等待下一次轮询
  if (evt.event === 'alert.firing' || evt.event === 'agent.offline' || evt.event === 'agent.online') {
    void store.refreshSystem()
  }
}

onMounted(async () => {
  await Promise.all([store.refreshSystem(), store.refreshBackends()])
  timer = window.setInterval(() => void store.refreshSystem(), 30_000)

  // 实时告警 WS 端点由 W7 交付；默认按环境变量开关，避免开发期控制台刷重连错误
  if (import.meta.env.VITE_WS_ENABLED === 'true') {
    socket = new AlertSocket([onAlertEvent])
    socket.connect()
  }
})

onUnmounted(() => {
  if (timer !== null) window.clearInterval(timer)
  socket?.close()
  socket = null
})
</script>

<template>
  <div class="shell">
    <aside class="side">
      <div class="brand">
        <strong>MetalWatch</strong>
        <span class="mw-tag">{{ driver }}</span>
      </div>
      <nav>
        <RouterLink v-for="item in nav" :key="item.to" :to="item.to" class="nav-item">
          {{ item.label }}
        </RouterLink>
      </nav>
    </aside>

    <main class="main">
      <header class="topbar">
        <div>
          <span class="mw-tag">v{{ store.status?.version ?? '-' }}</span>
          <span class="mw-tag">主机 {{ store.status?.host_count ?? '-' }}</span>
          <span class="mw-tag" :class="store.lastError ? 'crit' : 'ok'">
            {{ store.lastError ? '后端异常' : '后端正常' }}
          </span>
        </div>
        <div class="dim">更新于 {{ fromNow(store.status?.server_time) }}</div>
      </header>

      <div class="content">
        <RouterView />
      </div>
    </main>
  </div>
</template>

<style scoped>
.shell {
  display: grid;
  grid-template-columns: 208px 1fr;
  height: 100%;
}
.side {
  border-right: 1px solid var(--mw-border);
  padding: 16px 12px;
  background: var(--mw-panel);
}
.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 18px;
}
nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.nav-item {
  color: var(--mw-text-dim);
  padding: 8px 10px;
  border-radius: 6px;
}
.nav-item:hover {
  background: var(--mw-panel-2);
  color: var(--mw-text);
}
.nav-item.router-link-active {
  background: var(--mw-panel-2);
  color: var(--mw-accent);
}
.main {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.topbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  padding: 12px 18px;
  border-bottom: 1px solid var(--mw-border);
}
.content {
  padding: 18px;
  overflow: auto;
}
.dim {
  color: var(--mw-text-dim);
  font-size: 12px;
}
@media (max-width: 720px) {
  .shell {
    grid-template-columns: 1fr;
  }
  .side {
    border-right: none;
    border-bottom: 1px solid var(--mw-border);
  }
  nav {
    flex-direction: row;
    overflow-x: auto;
  }
}
</style>
