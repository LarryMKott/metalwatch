<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Message } from '@arco-design/web-vue'
import { useAppStore } from '@/store'
import { fromNow } from '@/utils/format'
import { AlertSocket, type AlertEvent } from '@/utils/websocket'

// 应用外壳：左侧导航 + 顶部状态 + 内容区。
// 顶部状态直接反映后端 /system/status，便于在飞牛应用面板之外自查。
const store = useAppStore()
const route = useRoute()
const router = useRouter()
const collapsed = ref(false)

const nav = [
  { key: '/dashboard', label: '监控大盘' },
  { key: '/asset', label: '资产管理' },
  { key: '/collect', label: '采集管理' },
  { key: '/storage', label: '存储管理' },
  { key: '/alarm', label: '告警中心' },
  { key: '/bmc', label: 'BMC 管控' },
  { key: '/mesh', label: 'Mesh 拓扑' },
  { key: '/report', label: '报表中心' },
  { key: '/setting', label: '系统设置' }
]

const activeKey = computed(() => route.path)
const driver = computed(() => store.currentDrivers?.metadata_driver ?? '-')
const backendTone = computed(() => (store.lastError ? 'error' : 'success'))

let timer: number | null = null
let socket: AlertSocket | null = null

function onAlertEvent(evt: AlertEvent) {
  if (evt.event === 'alert.firing' || evt.event === 'agent.offline' || evt.event === 'agent.online') {
    void store.refreshSystem()
  }
}

function onMenuClick(key: string) {
  if (key !== route.path) router.push(key)
}

async function refresh() {
  try {
    await Promise.all([store.refreshSystem(), store.refreshBackends()])
  } catch {
    Message.error('刷新系统状态失败')
  }
}

onMounted(async () => {
  await Promise.all([store.refreshSystem(), store.refreshBackends()])
  timer = window.setInterval(() => void store.refreshSystem(), 30_000)

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
  <a-layout class="shell">
    <a-layout-sider
      v-model:collapsed="collapsed"
      :width="208"
      :collapsed-width="56"
      collapsible
      breakpoint="lg"
      class="side"
    >
      <div class="brand">
        <span class="logo">MW</span>
        <span v-show="!collapsed" class="brand-name">MetalWatch</span>
      </div>
      <a-menu
        :selected-keys="[activeKey]"
        :collapsed="collapsed"
        accordion
        class="menu"
        @menu-item-click="onMenuClick"
      >
        <a-menu-item v-for="item in nav" :key="item.key">{{ item.label }}</a-menu-item>
      </a-menu>
    </a-layout-sider>

    <a-layout>
      <a-layout-header class="topbar">
        <div class="top-left">
          <a-tag color="arcoblue">{{ driver }}</a-tag>
          <a-tag>v{{ store.status?.version ?? '-' }}</a-tag>
          <a-tag>主机 {{ store.status?.host_count ?? '-' }}</a-tag>
          <a-tag :color="backendTone">{{ store.lastError ? '后端异常' : '后端正常' }}</a-tag>
        </div>
        <div class="top-right">
          <span v-if="store.lastError" class="err dim">{{ store.lastError }}</span>
          <span class="dim">更新于 {{ fromNow(store.status?.server_time) }}</span>
          <a-button size="mini" type="outline" @click="refresh">刷新</a-button>
        </div>
      </a-layout-header>

      <a-layout-content class="content">
        <RouterView />
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

<style scoped>
.shell {
  height: 100%;
}
.side {
  background: var(--mw-panel);
  border-right: 1px solid var(--mw-border);
}
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 56px;
  padding: 0 16px;
  border-bottom: 1px solid var(--mw-border);
}
.logo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 8px;
  background: var(--mw-accent);
  color: #02131c;
  font-weight: 700;
  font-size: 13px;
}
.brand-name {
  font-weight: 600;
  color: var(--mw-text);
}
.menu {
  background: transparent;
}
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 56px;
  padding: 0 18px;
  background: var(--mw-panel);
  border-bottom: 1px solid var(--mw-border);
}
.top-left,
.top-right {
  display: flex;
  align-items: center;
  gap: 8px;
}
.dim {
  color: var(--mw-text-dim);
  font-size: 12px;
}
.err {
  color: var(--mw-crit);
}
.content {
  padding: 18px;
  overflow: auto;
}
</style>
