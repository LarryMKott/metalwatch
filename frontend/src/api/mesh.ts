import http from '@/utils/request'
import type { MeshRoutes, MeshTopology } from '@/types/api'

// Mesh 拓扑接口（docs/04 §3）：网络拓扑可视化、链路状态、路由表。
export async function getMeshTopology(): Promise<MeshTopology> {
  const { data } = await http.get<MeshTopology>('/mesh/topology')
  return data
}

export async function getMeshRoutes(): Promise<MeshRoutes> {
  const { data } = await http.get<MeshRoutes>('/mesh/routes')
  return data
}
