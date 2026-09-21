// 实时告警 WebSocket 客户端：断线自动重连（指数退避，上限 30s）。
//
// 说明：后端 WS 端点由 W7 告警工作流交付；本模块先固定契约与重连策略，
// 未连通时调用方应回退到轮询 GET /api/v1/alerts。

export type AlertEvent = {
  event: 'alert.firing' | 'alert.resolved' | 'alert.suppressed' | 'agent.offline' | 'agent.online' | 'change.detected'
  severity?: 'critical' | 'major' | 'info'
  alert?: Record<string, unknown>
  host?: Record<string, unknown>
  ts?: string
}

type Handler = (evt: AlertEvent) => void

export class AlertSocket {
  private ws: WebSocket | null = null
  private closedByUser = false
  private retry = 0
  private timer: number | null = null

  constructor(private readonly handlers: Handler[] = []) {}

  connect(): void {
    this.closedByUser = false
    const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
    const path = import.meta.env.VITE_WS_PATH ?? '/api/v1/ws/alerts'
    const url = `${scheme}://${location.host}${path}`

    try {
      this.ws = new WebSocket(url)
    } catch {
      this.scheduleReconnect()
      return
    }

    this.ws.onopen = () => {
      this.retry = 0
    }
    this.ws.onmessage = (ev) => {
      let parsed: AlertEvent
      try {
        parsed = JSON.parse(ev.data as string) as AlertEvent
      } catch {
        return
      }
      this.handlers.forEach((h) => h(parsed))
    }
    this.ws.onclose = () => {
      if (!this.closedByUser) this.scheduleReconnect()
    }
    this.ws.onerror = () => {
      this.ws?.close()
    }
  }

  private scheduleReconnect(): void {
    const delay = Math.min(30_000, 1000 * 2 ** this.retry)
    this.retry += 1
    this.timer = window.setTimeout(() => this.connect(), delay)
  }

  close(): void {
    this.closedByUser = true
    if (this.timer !== null) window.clearTimeout(this.timer)
    this.ws?.close()
    this.ws = null
  }
}
