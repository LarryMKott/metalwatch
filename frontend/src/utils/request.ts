import axios, { type AxiosInstance } from 'axios'
import type { ApiError } from '@/types/api'

// 统一 axios 实例：
//  - baseURL 来自环境变量（dev 由 vite 代理，生产同源）
//  - 请求自动带上会话令牌（W11 起管理接口一律鉴权）
//  - 401 清本地令牌并抛事件，由路由守卫跳登录
//  - 错误按后端统一错误体解包，避免调用方处理 axios 的嵌套结构
export const http: AxiosInstance = axios.create({
  baseURL: import.meta.env.VITE_API_BASE ?? '/api/v1',
  timeout: 15000,
  withCredentials: true,
  headers: { 'Content-Type': 'application/json' }
})

// 令牌读写集中在这里：request 与 websocket 共用，避免两处各存一份导致不同步
const TOKEN_KEY = 'mw_token'

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? ''
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY)
}

http.interceptors.request.use((config) => {
  config.headers.set?.('X-Request-ID', `${Date.now()}-${Math.random().toString(16).slice(2, 8)}`)
  const token = getToken()
  if (token) config.headers.set?.('Authorization', `Bearer ${token}`)
  return config
})

http.interceptors.response.use(
  (resp) => resp,
  (error) => {
    const data = error?.response?.data as ApiError | undefined
    if (error?.response?.status === 401) {
      // 令牌过期/被吊销：清掉本地态再抛事件，避免带着死令牌反复重试
      clearToken()
      window.dispatchEvent(new CustomEvent('mw:unauthorized', { detail: data }))
    }
    const message = data?.message ?? error.message ?? '请求失败'
    const code = data?.code ?? 'network_error'
    return Promise.reject(Object.assign(new Error(message), { code, detail: data?.detail, status: error?.response?.status }))
  }
)

export default http
