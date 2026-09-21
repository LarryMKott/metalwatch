import http from '@/utils/request'
import type { Overview } from '@/types/api'

// 大盘聚合接口（docs/04 §3）：资产总览 / 在线状态 / 故障统计 / 告警趋势。
export async function getOverview(): Promise<Overview> {
  const { data } = await http.get<Overview>('/overview')
  return data
}
