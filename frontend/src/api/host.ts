import http from '@/utils/request'
import type { Host, CreateHostInput, PageResult } from '@/types/api'

// 资产相关接口（docs/04 §3）

export interface HostQuery {
  q?: string
  status?: string
  page?: number
  page_size?: number
}

export async function listHosts(params: HostQuery = {}): Promise<PageResult<Host>> {
  const { data } = await http.get<PageResult<Host>>('/hosts', { params })
  return data
}

export async function getHost(id: number): Promise<Host> {
  const { data } = await http.get<Host>(`/hosts/${id}`)
  return data
}

export async function createHost(input: CreateHostInput): Promise<Host> {
  const { data } = await http.post<Host>('/hosts', input)
  return data
}

export async function deleteHost(id: number): Promise<void> {
  await http.delete(`/hosts/${id}`)
}

export async function exportAssets(format: 'csv' | 'xlsx' | 'json', maskSecret = true): Promise<Blob> {
  const { data } = await http.get('/assets/export', {
    params: { format, mask_secret: maskSecret },
    responseType: 'blob'
  })
  return data as Blob
}
