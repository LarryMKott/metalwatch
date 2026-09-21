import http from '@/utils/request'
import type { Alert, AlertQuery, ThresholdTemplate } from '@/types/api'

// 告警中心接口（docs/04 §3）：事件列表、确认、阈值模板。
export async function listAlerts(params: AlertQuery = {}): Promise<Alert[]> {
  const { data } = await http.get<Alert[]>('/alerts', { params })
  return data
}

export async function ackAlert(id: number): Promise<void> {
  await http.post(`/alerts/${id}/ack`)
}

export async function listAlertTemplates(): Promise<ThresholdTemplate[]> {
  const { data } = await http.get<ThresholdTemplate[]>('/alerts/templates')
  return data
}
