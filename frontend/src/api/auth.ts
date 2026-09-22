import http from '@/utils/request'

// W11 鉴权接口（契约见 docs/02-设计/02-接口契约 §三）

export type LoginPayload = { username: string; password: string }

export type LoginResult = {
  token: string
  expires_at: string
  user: { id: number; username: string; display_name: string; role: string; last_login_at?: string }
}

export type MeResult = {
  kind: 'user' | 'token'
  username: string
  role: string
  scopes: string[]
  can_write: boolean
  is_admin: boolean
  user?: { id: number; username: string; display_name: string; role: string }
}

export type ApiTokenItem = {
  id: number
  name: string
  scopes: string
  state: string
  expire_at?: string
  last_used_at?: string
  created_by: string
  created_at: string
}

export function login(payload: LoginPayload): Promise<LoginResult> {
  return http.post('/auth/login', payload).then((r) => r.data)
}

export function logout(): Promise<{ ok: boolean }> {
  return http.delete('/auth/logout').then((r) => r.data)
}

export function me(): Promise<MeResult> {
  return http.get('/auth/me').then((r) => r.data)
}

export function changePassword(oldPassword: string, newPassword: string): Promise<{ ok: boolean }> {
  return http.post('/auth/change-password', {
    old_password: oldPassword,
    new_password: newPassword
  }).then((r) => r.data)
}

// 开放接口令牌：明文只在创建响应里出现一次，列表接口永远拿不到
export function listApiTokens(): Promise<{ items: ApiTokenItem[]; total: number }> {
  return http.get('/api-tokens').then((r) => r.data)
}

export function createApiToken(name: string, scopes?: string, expireDays?: number):
  Promise<{ token: string; item: ApiTokenItem }> {
  return http.post('/api-tokens', { name, scopes, expire_days: expireDays ?? 0 }).then((r) => r.data)
}

export function revokeApiToken(id: number): Promise<{ ok: boolean }> {
  return http.delete(`/api-tokens/${id}`).then((r) => r.data)
}

export type AuditItem = {
  id: number
  user_id: number | null
  username: string
  action: string
  target_type: string
  target_id: string
  source_ip: string
  result: string
  detail: string
  created_at: string
}

export function listAuditLogs(params: {
  username?: string; action?: string; result?: string
  from?: string; to?: string; page?: number; page_size?: number
} = {}): Promise<{ items: AuditItem[]; total: number }> {
  return http.get('/audit-logs', { params }).then((r) => r.data)
}
