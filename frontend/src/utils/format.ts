// 展示层格式化工具（与后端单位约定保持一致，见 docs/03 §3）。

export function formatBytes(v?: number | null, digits = 1): string {
  if (v === undefined || v === null || Number.isNaN(v)) return '-'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let n = v
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i += 1
  }
  return `${n.toFixed(i === 0 ? 0 : digits)} ${units[i]}`
}

export function formatPercent(v?: number | null, digits = 1): string {
  if (v === undefined || v === null || Number.isNaN(v)) return '-'
  return `${v.toFixed(digits)}%`
}

export function formatDuration(sec?: number | null): string {
  if (sec === undefined || sec === null) return '-'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}天${h}小时`
  if (h > 0) return `${h}小时${m}分`
  if (m > 0) return `${m}分`
  return `${Math.floor(sec)}秒`
}

export function formatTime(iso?: string | null): string {
  if (!iso) return '-'
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return '-'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())} ${pad(t.getHours())}:${pad(t.getMinutes())}:${pad(t.getSeconds())}`
}

/** 相对时间，用于「最近上报」这类字段 */
export function fromNow(iso?: string | null): string {
  if (!iso) return '从未'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '从未'
  const diff = Math.floor((Date.now() - t) / 1000)
  if (diff < 60) return `${diff} 秒前`
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`
  return `${Math.floor(diff / 86400)} 天前`
}

/** 状态色：正常/降级/异常（监控习惯：红=异常、绿=正常） */
export function statusTone(status?: string): 'ok' | 'warn' | 'crit' | 'muted' {
  switch (status) {
    case 'online':
    case 'OK':
    case 'Optimal':
      return 'ok'
    case 'unknown':
      return 'muted'
    case 'Degraded':
    case 'PreFail':
      return 'warn'
    case 'offline':
    case 'Fail':
    case 'Failed':
      return 'crit'
    default:
      return 'muted'
  }
}
