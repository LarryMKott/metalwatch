import http from '@/utils/request'
import type { StorageBackendsResponse, SystemStatus } from '@/types/api'

// 系统与存储相关接口（docs/04 §3）

export async function getSystemStatus(): Promise<SystemStatus> {
  const { data } = await http.get<SystemStatus>('/system/status')
  return data
}

/** 列出所有可选的元数据 / 时序存储后端及其可用状态（含「规划中」的后端） */
export async function getStorageBackends(): Promise<StorageBackendsResponse> {
  const { data } = await http.get<StorageBackendsResponse>('/system/storage/backends')
  return data
}

export async function getHealth(): Promise<{ ok: boolean; [k: string]: unknown }> {
  const { data } = await http.get('/healthz')
  return data as { ok: boolean; [k: string]: unknown }
}
