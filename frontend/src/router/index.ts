import { createRouter, createWebHashHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/store/auth'

// 使用 hash 模式：飞牛桌面入口以 iframe 嵌入，hash 路由不依赖服务端 rewrite，
// 刷新/深链都不会 404。
const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/dashboard' },
  { path: '/login', name: 'login', component: () => import('@/views/Login/index.vue'), meta: { title: '登录', public: true } },
  { path: '/dashboard', name: 'dashboard', component: () => import('@/views/Dashboard/index.vue'), meta: { title: '监控大盘' } },
  { path: '/asset', name: 'asset', component: () => import('@/views/Asset/index.vue'), meta: { title: '资产管理' } },
  { path: '/collect', name: 'collect', component: () => import('@/views/Collect/index.vue'), meta: { title: '采集管理' } },
  { path: '/storage', name: 'storage', component: () => import('@/views/Storage/index.vue'), meta: { title: '存储管理' } },
  { path: '/alarm', name: 'alarm', component: () => import('@/views/Alarm/index.vue'), meta: { title: '告警中心' } },
  { path: '/bmc', name: 'bmc', component: () => import('@/views/Bmc/index.vue'), meta: { title: 'BMC 管控' } },
  { path: '/mesh', name: 'mesh', component: () => import('@/views/Mesh/index.vue'), meta: { title: 'Mesh 拓扑' } },
  { path: '/report', name: 'report', component: () => import('@/views/Report/index.vue'), meta: { title: '报表中心' } },
  { path: '/setting', name: 'setting', component: () => import('@/views/Setting/index.vue'), meta: { title: '系统设置' } }
]

const router = createRouter({
  history: createWebHashHistory(),
  routes
})

// W11：管理接口一律鉴权，未登录时把用户挡在登录页，并记住原目标以便回跳。
// 令牌是否有效最终由后端判定；这里只看本地有没有令牌，避免每个路由都打一次 /me。
router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!auth.loaded) await auth.refresh()
  if (to.meta?.public) return true
  if (!auth.isLogin) {
    return { path: '/login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }
  }
  return true
})

router.afterEach((to) => {
  const title = (to.meta?.title as string | undefined) ?? ''
  document.title = title ? `${title} · MetalWatch` : 'MetalWatch'
})

export default router
