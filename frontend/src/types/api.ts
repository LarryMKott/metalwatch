// 与后端 REST 契约一一对应的类型定义（docs/04 §3）。

export type HostStatus = 'online' | 'offline' | 'unknown'
export type OSType = 'linux' | 'windows' | 'unknown'

export interface Host {
  id: number
  hostname: string
  primary_ip: string
  bmc_ip?: string
  sn?: string
  smbios_uuid?: string
  site?: string
  rack?: string
  rack_unit?: number
  os_type: OSType
  os_version?: string
  collect_agent: boolean
  collect_ipmi: boolean
  agent_version?: string
  status: HostStatus
  last_seen_at?: string
  geo_country?: string
  remark?: string
  created_at: string
  updated_at: string
}

export interface CreateHostInput {
  hostname: string
  primary_ip: string
  bmc_ip?: string
  sn?: string
  smbios_uuid?: string
  site?: string
  rack?: string
  rack_unit?: number
  os_type?: OSType
  collect_ipmi?: boolean
  remark?: string
}

export interface PageResult<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}

/** 存储后端可用状态（后端 adapter.Catalog 的结构） */
export interface StorageBackend {
  name: string
  kind: 'metadata' | 'timeseries'
  implemented: boolean
  deployment: 'builtin' | 'external'
  note: string
  work_item?: string
}

export interface StorageBackendsResponse {
  current: { metadata_driver: string; tsdb_driver: string }
  backends: StorageBackend[]
}

export interface SystemStatus {
  version: string
  uptime_sec: number
  goroutines: number
  heap_bytes: number
  sys_bytes: number
  storage: Record<string, unknown>
  host_count: number
  collect: Record<string, number>
  server_time: string
}

/** 统一错误体 */
export interface ApiError {
  code: string
  message: string
  request_id?: string
  detail?: unknown
}
