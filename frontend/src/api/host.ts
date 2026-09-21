import http from '@/utils/request'
import type { ChangeRecord, CreateHostInput, Host, HostMetricsQuery, HostQuery, MetricSeries, Paginated } from '@/types/api'

// 资产 / 主机相关接口（docs/04 §3）
// 分页采用后端约定的 limit/offset 游标。

export async function listHosts(params: HostQuery = {}): Promise<Paginated<Host>> {
  const { data } = await http.get<Paginated<Host>>('/hosts', { params })
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

/** 传感器时序曲线：?metric=cpu_temp_celsius&from=&to=&step=60s */
export async function getHostMetrics(id: number, query: HostMetricsQuery): Promise<MetricSeries> {
  const { data } = await http.get<MetricSeries>(`/hosts/${id}/metrics`, { params: query })
  return data
}

/** 硬件变更记录 */
export async function getHostChanges(id: number): Promise<ChangeRecord[]> {
  const { data } = await http.get<ChangeRecord[]>(`/hosts/${id}/changes`)
  return data
}
