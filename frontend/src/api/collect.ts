import http from '@/utils/request'
import type { CollectTask } from '@/types/api'

// 采集管理接口（docs/04 §3）：Agent 接入、IPMI 节点、采集周期、批量发现。
export async function listCollectTasks(): Promise<CollectTask[]> {
  const { data } = await http.get<CollectTask[]>('/collect-tasks')
  return data
}
