import http from '@/utils/request'
import type { BmcAuditEntry, BmcCapability, BmcCommandInput, BmcCommandResult } from '@/types/api'

// BMC 管控接口（docs/04 §3）：能力探测、指令下发、操作审计。
export async function getBmcCapability(hostId: number): Promise<BmcCapability> {
  const { data } = await http.get<BmcCapability>(`/bmc/${hostId}/capability`)
  return data
}

export async function sendBmcCommand(hostId: number, input: BmcCommandInput): Promise<BmcCommandResult> {
  const { data } = await http.post<BmcCommandResult>(`/bmc/${hostId}/command`, input)
  return data
}

export async function listBmcAudit(): Promise<BmcAuditEntry[]> {
  const { data } = await http.get<BmcAuditEntry[]>('/bmc/audit')
  return data
}
