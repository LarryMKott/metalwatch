import http from '@/utils/request'
import type { CreateReportInput, ReportJob } from '@/types/api'

// 报表中心接口（docs/04 §3）：硬件巡检报告、资产清单导出。
export async function createReport(input: CreateReportInput): Promise<ReportJob> {
  const { data } = await http.post<ReportJob>('/reports', input)
  return data
}

/** 直接下载已生成的报表（后端返回文件流，按 blob 处理） */
export async function downloadReport(jobId: number, format: string): Promise<Blob> {
  const { data } = await http.get(`/reports/${jobId}/download`, { params: { format }, responseType: 'blob' })
  return data as Blob
}
