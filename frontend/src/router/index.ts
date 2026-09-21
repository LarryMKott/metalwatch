import { createRouter, createWebHashHistory, type RouteRecordRaw } from 'vue-router'

// 使用 hash 模式：飞牛桌面入口以 iframe 嵌入，hash 路由不依赖服务端 rewrite，
// 刷新/深链都不会 404。
const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/dashboard' },
  { path: '/dashboard', name: 'dashboard', component: () => import('@/views/Dashboard/index.vue'), meta: { title: '监控大盘' } },
  { path: '/asset', name: 'asset', component: () => import('@/views/Asset/index.vue'), meta: { title: '硬件资产' } },
  { path: '/collect', name: 'collect', component: () => import('@/views/Collect/index.vue'), meta: { title: '采集配置' } },
  { path: '/storage', name: 'storage', component: () => import('@/views/Storage/index.vue'), meta: { title: '存储管理' } },
  { path: '/alarm', name: 'alarm', component: () => import('@/views/Alarm/index.vue'), meta: { title: '告警中心' } },
  { path: '/geoip', name: 'geoip', component: () => import('@/views/GeoIP/index.vue'), meta: { title: '地理库管理' } },
  { path: '/report', name: 'report', component: () => import('@/views/Report/index.vue'), meta: { title: '巡检报表' } },
  { path: '/setting', name: 'setting', component: () => import('@/views/Setting/index.vue'), meta: { title: '系统设置' } }
]

const router = createRouter({
  history: createWebHashHistory(),
  routes
})

router.afterEach((to) => {
  const title = (to.meta?.title as string | undefined) ?? ''
  document.title = title ? `${title} · MetalWatch` : 'MetalWatch'
})

export default router
