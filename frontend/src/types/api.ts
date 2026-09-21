// 与后端 REST 契约一一对应的类型定义（docs/04 §3）。
// 所有响应错误体统一为 ApiError。

// ---------------------------------------------------------------------------
// 通用
// ---------------------------------------------------------------------------

/** 统一错误体：{ code, message, request_id } */
export interface ApiError {
  code: string
  message: string
  request_id?: string
  detail?: unknown
}

// ---------------------------------------------------------------------------
// 系统 / 存储
// ---------------------------------------------------------------------------

export interface SystemStorage {
  metadata_driver: string
  tsdb_driver: string
  schema_version: string
  [key: string]: unknown
}

export interface SystemStatus {
  version: string
  uptime_sec: number
  host_count: number
  storage: SystemStorage
  collect: Record<string, number>
  // 兼容旧字段（部分构建可能仍返回）
  goroutines?: number
  heap_bytes?: number
  sys_bytes?: number
  server_time?: string
}

export type BackendKind = 'metadata' | 'timeseries' | string
export type BackendDeployment = 'builtin' | 'external' | string

export interface StorageBackend {
  name: string
  kind: BackendKind
  deployment: BackendDeployment
  implemented: boolean
  note: string
  work_item?: string
}

export interface StorageBackendsResponse {
  current: { metadata_driver: string; tsdb_driver: string }
  backends: StorageBackend[]
}

// ---------------------------------------------------------------------------
// 大盘聚合
// ---------------------------------------------------------------------------

export interface OverviewSeriesPoint {
  ts: string
  online: number
  offline: number
  alert: number
}

export interface Overview {
  host_total: number
  host_online: number
  host_offline: number
  alert_active: number
  series: OverviewSeriesPoint[]
}

// ---------------------------------------------------------------------------
// 资产 / 主机
// ---------------------------------------------------------------------------

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

/** 分页响应（后端使用 limit/offset 游标） */
export interface Paginated<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

export interface HostQuery {
  q?: string
  state?: HostStatus | 'all'
  limit?: number
  offset?: number
}

/** 时序曲线点：[时间戳(ms), 数值] */
export type MetricPoint = [number, number]

export interface MetricSeries {
  metric: string
  unit?: string
  points: MetricPoint[]
}

export interface HostMetricsQuery {
  metric: string
  from?: string
  to?: string
  step?: string
}

export type ChangeCategory = 'cpu' | 'memory' | 'disk' | 'nic' | 'firmware' | 'bmc' | string

export interface ChangeRecord {
  id: number
  host_id: number
  detected_at: string
  category: ChangeCategory
  field: string
  old_value?: string
  new_value?: string
  source: string
}

// ---------------------------------------------------------------------------
// 采集
// ---------------------------------------------------------------------------

export type CollectTaskKind = 'agent' | 'ipmi' | 'discovery'
export type CollectTaskState = 'running' | 'idle' | 'error' | 'disabled'

export interface CollectTask {
  id: number
  name: string
  kind: CollectTaskKind
  target: string
  interval_sec: number
  enabled: boolean
  last_run_at?: string
  state: CollectTaskState
  error?: string
}

// ---------------------------------------------------------------------------
// 告警
// ---------------------------------------------------------------------------

export type AlertSeverity = 'critical' | 'major' | 'minor' | 'info'
export type AlertState = 'active' | 'acked' | 'resolved' | 'suppressed'

export interface Alert {
  id: number
  host_id: number
  host_name?: string
  severity: AlertSeverity
  rule: string
  state: AlertState
  fired_at: string
  value?: number
  message?: string
}

export interface AlertQuery {
  state?: AlertState | 'all'
  from?: string
  to?: string
}

export interface ThresholdTemplate {
  id: number
  name: string
  metric: string
  op: '>' | '>=' | '<' | '<=' | '=='
  threshold: number
  severity: AlertSeverity
  for_duration: string
  enabled: boolean
}

// ---------------------------------------------------------------------------
// BMC 管控
// ---------------------------------------------------------------------------

export interface BmcCapability {
  host_id: number
  fan_control: boolean
  power_control: boolean
  identify: boolean
  bmc_model?: string
  firmware_version?: string
}

export type BmcCommandType = 'fan' | 'power' | 'identify' | 'policy'
export type BmcPowerAction = 'on' | 'off' | 'reset' | 'cycle' | 'soft'

export interface BmcCommandInput {
  cmd_type: BmcCommandType
  target?: string
  speed_percent?: number
  auto_mode?: boolean
  power_action?: BmcPowerAction
  duration_sec?: number
}

export interface BmcCommandResult {
  ok: boolean
  message?: string
  request_id?: string
}

export interface BmcAuditEntry {
  id: number
  host_id: number
  host_name?: string
  operator: string
  cmd_type: BmcCommandType
  target?: string
  params?: string
  result: 'success' | 'failed'
  created_at: string
}

// ---------------------------------------------------------------------------
// Mesh 拓扑
// ---------------------------------------------------------------------------

export interface MeshNode {
  agent_id: string
  hostname: string
  ip: string
  online: boolean
}

export interface MeshLink {
  from: string
  to: string
  rtt_ms: number
  loss_rate: number
}

export interface MeshTopology {
  nodes: MeshNode[]
  links: MeshLink[]
}

export interface MeshRouteEntry {
  dst_agent: string
  next_hop: string
  hop_count: number
  cost: number
  path: string[]
}

export interface MeshRoutes {
  version: string
  computed_at: string
  max_hops: number
  entries: MeshRouteEntry[]
}

// ---------------------------------------------------------------------------
// GeoIP
// ---------------------------------------------------------------------------

export interface GeoIpCoverage {
  country: string
  count: number
}

export interface GeoIpStats {
  library_version: string
  build_date?: string
  total_records: number
  country_count: number
  coverage_by_country: GeoIpCoverage[]
  updated_at?: string
}

// ---------------------------------------------------------------------------
// 报表
// ---------------------------------------------------------------------------

export type ReportKind = 'inspection' | 'inventory'
export type ReportFormat = 'json' | 'xlsx' | 'html' | 'csv'

export interface CreateReportInput {
  kind: ReportKind
  format: ReportFormat
  from?: string
  to?: string
  host_ids?: number[]
}

export interface ReportJob {
  id: number
  kind: ReportKind
  format: ReportFormat
  status: 'pending' | 'running' | 'done' | 'failed'
  download_url?: string
  created_at: string
  finished_at?: string
}
