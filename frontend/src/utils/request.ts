import axios, { type AxiosInstance } from 'axios'
import type { ApiError } from '@/types/api'

// 统一 axios 实例：
//  - baseURL 来自环境变量（dev 由 vite 代理，生产同源）
//  - 401 统一跳转登录（登录页在 W11 交付，先抛事件）
//  - 错误按后端统一错误体解包，避免调用方处理 axios 的嵌套结构
export const http: AxiosInstance = axios.create({
  baseURL: import.meta.env.VITE_API_BASE ?? '/api/v1',
  timeout: 15000,
  withCredentials: true,
  headers: { 'Content-Type': 'application/json' }
})

http.interceptors.request.use((config) => {
  config.headers.set?.('X-Request-ID', `${Date.now()}-${Math.random().toString(16).slice(2, 8)}`)
  return config
})

http.interceptors.response.use(
  (resp) => resp,
  (error) => {
    const data = error?.response?.data as ApiError | undefined
    if (error?.response?.status === 401) {
      window.dispatchEvent(new CustomEvent('mw:unauthorized', { detail: data }))
    }
    const message = data?.message ?? error.message ?? '请求失败'
    const code = data?.code ?? 'network_error'
    return Promise.reject(Object.assign(new Error(message), { code, detail: data?.detail, status: error?.response?.status }))
  }
)

export default http
