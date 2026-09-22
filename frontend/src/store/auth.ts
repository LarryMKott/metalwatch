import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import * as authApi from '@/api/auth'

// 令牌存放位置：localStorage 便于刷新后保持登录态。
// 风险取舍：XSS 可读到令牌；本项目无富文本渲染入口，且令牌 12h 过期，
// 相比每次刷新都要重新登录，这个取舍对单机运维场景更划算。
const TOKEN_KEY = 'mw_token'
const USER_KEY = 'mw_user'

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string>(localStorage.getItem(TOKEN_KEY) ?? '')
  const me = ref<authApi.MeResult | null>(null)
  const loaded = ref(false)

  const isLogin = computed(() => token.value !== '')
  const isAdmin = computed(() => me.value?.is_admin ?? false)
  const canWrite = computed(() => me.value?.can_write ?? false)
  const username = computed(() => me.value?.username ?? '')

  function persist(t: string) {
    token.value = t
    if (t) localStorage.setItem(TOKEN_KEY, t)
    else localStorage.removeItem(TOKEN_KEY)
  }

  async function login(username: string, password: string): Promise<void> {
    const res = await authApi.login({ username, password })
    persist(res.token)
    localStorage.setItem(USER_KEY, res.user.username)
    await refresh()
  }

  async function refresh(): Promise<void> {
    if (!token.value) {
      me.value = null
      loaded.value = true
      return
    }
    try {
      me.value = await authApi.me()
    } catch {
      // 令牌失效（过期/被停用）：静默清理，交给路由守卫跳登录
      persist('')
      me.value = null
    }
    loaded.value = true
  }

  async function logout(): Promise<void> {
    try {
      await authApi.logout()
    } catch {
      // 登出失败也要清本地：后端是无状态令牌，清本地就等于登出
    }
    persist('')
    localStorage.removeItem(USER_KEY)
    me.value = null
  }

  return { token, me, loaded, isLogin, isAdmin, canWrite, username, login, refresh, logout }
})
