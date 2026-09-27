import { useSessionStore } from '../stores/session'

const API_BASE = import.meta.env.VITE_API_BASE || ''

export interface UsageSummary {
  requests_total: number
  requests_success: number
  requests_failed: number
  cache_hits: number
  results_total: number
  average_latency_ms: number
}

export interface UsageUnitSummary {
  provider_name: string
  unit: string
  quantity_total: number
  cost_usd_total: number
}

export interface BillingSummary {
  days: number
  units: UsageUnitSummary[]
}

export interface ProviderHealth {
  provider_name: string
  display_name: string
  enabled: boolean
  status: string
  available_keys: number
  total_keys: number
  exhausted_keys: number
  disabled_keys: number
  cooling_keys: number
  requests_total: number
  requests_failed: number
  success_rate: number
  last_error: string
  last_checked_at: string
  window_minutes: number
}

export interface GatewayMetrics {
  usage: UsageSummary
  provider_health: ProviderHealth[]
  billing: BillingSummary
}

export interface UsageSeriesPoint {
  date: string
  requests_total: number
  requests_success: number
  requests_failed: number
  cache_hits: number
  results_total: number
  average_latency_ms: number
}

export interface UsageSeries {
  range?: string
  granularity?: 'hour' | 'day' | string
  days: number
  points: UsageSeriesPoint[]
}

export type DashboardRangeKey = '24h' | 'today' | '7d' | '14d' | '30d'

export interface DashboardRangeMeta {
  range: DashboardRangeKey | string
  label: string
  granularity: 'hour' | 'day' | string
  segment_minutes: number
  segments: number
  billing_days: number
}

export interface ProviderUsagePoint {
  provider_name: string
  display_name: string
  requests_total: number
}

export interface HealthSegmentPoint {
  status: 'ok' | 'degraded' | 'down' | 'off' | string
  success: number
  failed: number
  total: number
}

export interface HealthSegmentSeries {
  provider_name: string
  display_name: string
  status: string
  available_keys: number
  total_keys: number
  success_rate: number
  uptime_percent: number
  segments: HealthSegmentPoint[]
  segment_minutes: number
}

export interface AuditLog {
  id: number
  request_id: string
  actor: string
  action: string
  resource_type: string
  resource_id: string
  ip_address: string
  metadata: Record<string, unknown>
  created_at: string
}

export interface ProviderConfig {
  id: number
  name: string
  display_name: string
  base_url: string
  enabled: boolean
  priority: number
  weight: number
  timeout_ms: number
  settings?: Record<string, unknown>
  available_keys?: number
  /**
   * 该渠道无密钥时能否正常调用，由后端按适配器能力下发（不是数据库字段）。
   * 仅用于管理台提示与创建匿名密钥时的确认框，不参与任何放行判断；
   * 缺省或后端未填充时视为 false，即「未声明支持匿名」。
   */
  supports_anonymous_key?: boolean
}

export interface ProviderKey {
  id: number
  provider_id: number
  provider_name: string
  alias: string
  key_hint: string
  key?: string
  /** key 级基础 URL 覆盖值；缺省或空串表示回退到渠道的 base_url */
  base_url?: string
  /** key 级代理模式，inherit（跟随渠道）/ direct（强制直连）/ custom（用 key 自己的地址）；缺省视为 inherit */
  proxy_mode?: string
  /** custom 模式下的代理地址；为空时回退渠道级代理 */
  proxy_url?: string
  exa_api_key_id?: string
  exa_service_key_hint?: string
  status: string
  weight: number
  rpm_limit: number
  daily_quota: number
  monthly_quota: number
  max_concurrency: number
  current_failures: number
  total_successes: number
  total_failures: number
  daily_used: number
  monthly_used: number
  official_quota_status: string
  official_quota_message: string
  official_quota_unit: string
  official_quota_balance?: number
  official_quota_balance_usd?: number
  official_quota_used_usd?: number
  official_quota_total_quantity?: number
  official_quota_account_id?: string
  official_quota_checked_at?: string
  cooldown_until?: string
  last_used_at?: string
  created_at: string
  updated_at: string
}

export interface OfficialQuotaResult {
  provider: string
  alias: string
  supported: boolean
  status: string
  message?: string
  unit?: string
  balance?: number
  balance_cents?: number
  balance_usd?: number
  total_cost_usd?: number
  total_quantity?: number
  api_key_id?: string
  api_key_name?: string
  account_id?: string
  period?: { start?: string; end?: string }
  breakdown?: Record<string, unknown>[]
  raw_text?: string
  fetched_at: string
}

export interface ApiToken {
  id: number
  name: string
  token_prefix: string
  token?: string
  /** 纯元数据：后端无任何按 scopes 的鉴权逻辑，它不限制可用接口或渠道。 */
  scopes: string[]
  allowed_providers: string[]
  status: string
  rate_limit_per_min: number
  daily_quota: number
  monthly_quota: number
  last_used_at?: string
  usage_count: number
  created_at: string
  updated_at: string
}

export interface AdminAPIKey {
  key?: string
  key_prefix: string
  created_at?: string
  updated_at?: string
}

export interface RuntimeSettings {
  default_mode: string
  default_providers: string[]
  default_limit: number
  default_dedupe: boolean
  request_timeout_ms: number
  cache_enabled: boolean
  cache_ttl_seconds: number
  cache_max_results: number
  compat_tavily_enabled: boolean
  compat_serper_enabled: boolean
  compat_openai_enabled: boolean
  api_auth_required: boolean
  provider_health_window_minutes: number
  provider_routing_strategy: string
  log_retention_days: number
  search_logs_limit: number
}

/** 网页抓取功能的全局配置，对应后端 settings 表的 key='fetch'。 */
export interface FetchSettings {
  /** 关闭时 /v1/fetch 返回 404，且 MCP 工具清单不再列出 fetch */
  enabled: boolean
  /** 代理地址，空串表示直连；容器部署时 127.0.0.1 / localhost 会被后端改写为 host.docker.internal */
  proxy_url: string
  /** 放行内网与环回目标；公网部署必须为 false，否则等于开放 SSRF 跳板 */
  allow_private: boolean
  /** 单次抓取超时（毫秒），上界 60000（受反向代理 65s 超时约束） */
  timeout_ms: number
  /** 兜底回退总开关；开启后内置抓取失败或内容过少时会改用 Tavily extract，并按量消耗第三方额度 */
  fallback_enabled: boolean
  /** 触发回退的可见文本长度阈值（按 Unicode 码点计，已剥离 Markdown 图片与链接目标）；默认 80 */
  fallback_min_chars: number
  /** 正常内容的缓存时长（秒），0 表示关闭缓存；窗口内返回的是上一次抓取的内容 */
  cache_ttl_seconds: number
  /** 上游返回 401/403/429 时的缓存时长（秒）；避免几分钟内反复撞同一道门禁 */
  cache_error_ttl_seconds: number
  /** 单条缓存上限（字节），超过则不缓存该条；默认 3145728（3MB） */
  cache_max_bytes: number
  /** 缓存目录总量上限（字节），超出时按文件修改时间从旧到新淘汰；默认 268435456（256MB） */
  cache_max_total_bytes: number
  /** 单进程同时在飞的抓取上限（含内置抓取与回退），超出的请求排队等待；默认 32 */
  max_concurrency: number
}

/** 单次抓取的结果，对应后端 fetch.Result。 */
export interface FetchResult {
  url: string
  method: string
  /**
   * 上游响应状态码：抓到 4xx/5xx 页面本身仍算成功抓取，状态码在此透出。
   * 内容来自回退通道（channel 为 tavily）时恒为 200。
   */
  status_code: number
  content_type: string
  /** 内容来自哪条通道：direct（内置抓取）或 tavily（兜底回退） */
  channel: string
  /** 归一化并按 max_length 截断后的内容（含续读提示） */
  content: string
  truncated: boolean
  /** 续读起点，仅在 truncated 为 true 时有意义 */
  next_start_index: number
  /** 归一化后完整内容的字符数 */
  total_length: number
}

export interface SearchLog {
  id: number
  request_id: string
  query: string
  mode: string
  compat_format: string
  providers: string[]
  cache_policy: string
  cache_hit: boolean
  result_count: number
  status: string
  error_message: string
  latency_ms: number
  request_json?: unknown
  response_json?: unknown
  created_at: string
}

export interface ProviderCallLog {
  provider_key_id: number
  provider_name: string
  key_alias: string
  attempt_index?: number
  will_retry?: boolean
  status: string
  error_type: string
  error_message: string
  latency_ms: number
  result_count: number
  cached: boolean
  usage?: Array<{ unit: string; quantity: number; cost_usd?: number; metadata?: Record<string, unknown> }>
}

export async function apiFetch<T>(path: string, options: RequestInit = {}): Promise<T> {
  const session = useSessionStore()
  const headers = new Headers(options.headers || {})
  headers.set('Content-Type', 'application/json')
  if (session.token) {
    headers.set('Authorization', `Bearer ${session.token}`)
  }
  const response = await fetch(`${API_BASE}${path}`, { ...options, headers })
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}))
    throw new Error(payload?.error?.message || response.statusText)
  }
  return response.json() as Promise<T>
}

export const api = {
  login: (username: string, password: string) => apiFetch<{ token: string; expires_at: string }>('/api/admin/login', { method: 'POST', body: JSON.stringify({ username, password }) }),
  logout: () => apiFetch('/api/admin/logout', { method: 'POST' }),
  dashboard: (range: DashboardRangeKey | string = '14d') => apiFetch<{
    range?: DashboardRangeMeta
    usage: UsageSummary
    providers: ProviderConfig[]
    provider_health?: ProviderHealth[]
    billing?: BillingSummary
    usage_series?: UsageSeries
    provider_series?: ProviderUsagePoint[]
    health_series?: HealthSegmentSeries[]
  }>(`/api/admin/dashboard?range=${encodeURIComponent(range)}`),
  providers: () => apiFetch<{ providers: ProviderConfig[] }>('/api/admin/providers'),
  updateProvider: (provider: ProviderConfig) => apiFetch('/api/admin/providers/' + provider.name, { method: 'PATCH', body: JSON.stringify(provider) }),
  keys: () => apiFetch<{ keys: ProviderKey[] }>('/api/admin/keys'),
  createKey: (payload: Record<string, unknown>) => apiFetch<ProviderKey>('/api/admin/keys', { method: 'POST', body: JSON.stringify(payload) }),
  revealKey: (id: number) => apiFetch<{ id: number; provider_name: string; alias: string; key: string; key_hint: string; exa_service_key?: string }>('/api/admin/keys/' + id + '/secret'),
  updateKey: (id: number, payload: Record<string, unknown>) => apiFetch<ProviderKey>('/api/admin/keys/' + id, { method: 'PATCH', body: JSON.stringify(payload) }),
  deleteKey: (id: number) => apiFetch('/api/admin/keys/' + id, { method: 'DELETE' }),
  testKey: (id: number, payload: Record<string, unknown>) => apiFetch('/api/admin/keys/' + id + '/test', { method: 'POST', body: JSON.stringify(payload) }),
  queryKeyQuota: (id: number, payload: Record<string, unknown> = {}) => apiFetch<OfficialQuotaResult>('/api/admin/keys/' + id + '/quota', { method: 'POST', body: JSON.stringify(payload) }),
  tokens: () => apiFetch<{ tokens: ApiToken[] }>('/api/admin/tokens'),
  createToken: (payload: Record<string, unknown>) => apiFetch<{ token: ApiToken; raw_token: string }>('/api/admin/tokens', { method: 'POST', body: JSON.stringify(payload) }),
  /**
   * 读取单条令牌的明文，用于管理台随时复制。
   * 明文在服务端加密留存，此处按需解密；失败原因（不存在 / 明文未留存）由后端以
   * 404 / 409 区分，错误信息可直接展示给用户。每次调用都会写入审计日志。
   */
  revealToken: (id: number) => apiFetch<{ id: number; name: string; token_prefix: string; token: string }>('/api/admin/tokens/' + id + '/secret'),
  updateToken: (id: number, payload: Record<string, unknown>) => apiFetch('/api/admin/tokens/' + id, { method: 'PATCH', body: JSON.stringify(payload) }),
  deleteToken: (id: number) => apiFetch('/api/admin/tokens/' + id, { method: 'DELETE' }),
  settings: () => apiFetch<RuntimeSettings>('/api/admin/settings'),
  updateSettings: (payload: RuntimeSettings) => apiFetch<RuntimeSettings>('/api/admin/settings', { method: 'PUT', body: JSON.stringify(payload) }),
  adminAPIKey: () => apiFetch<AdminAPIKey>('/api/admin/settings/admin-api-key'),
  /**
   * 读取管理员 API Key 的明文，用于管理台随时复制。
   * 未生成过 Key 时后端返回 404；每次调用都会写入审计日志（高敏动作）。
   */
  revealAdminAPIKey: () => apiFetch<{ key_prefix: string; key: string }>('/api/admin/settings/admin-api-key/secret'),
  rotateAdminAPIKey: () => apiFetch<AdminAPIKey>('/api/admin/settings/admin-api-key', { method: 'POST' }),
  /** 读取「网页抓取」功能配置；后端始终返回带默认值的完整结构 */
  fetchSettings: () => apiFetch<FetchSettings>('/api/admin/fetch/settings'),
  /** 保存「网页抓取」功能配置；回包为入库后的配置，可用作前端脏检查基线 */
  updateFetchSettings: (payload: FetchSettings) => apiFetch<FetchSettings>('/api/admin/fetch/settings', { method: 'PUT', body: JSON.stringify(payload) }),
  /** 管理台试抓；参数与 /v1/fetch 的 POST 形态一致，失败时抛出带原因的错误 */
  testFetch: (payload: Record<string, unknown>) => apiFetch<FetchResult>('/api/admin/fetch/test', { method: 'POST', body: JSON.stringify(payload) }),
  logs: (limit?: number) => apiFetch<{ logs: SearchLog[] }>(limit == null ? '/api/admin/logs' : `/api/admin/logs?limit=${Math.max(1, Math.min(limit, 1000))}`),
  logDetail: (id: number) => apiFetch<{ log: SearchLog; calls: ProviderCallLog[] }>('/api/admin/logs/' + id),
  usageSummary: () => apiFetch<UsageSummary>('/api/admin/usage/summary'),
  billingSummary: (days = 30) => apiFetch<BillingSummary>(`/api/admin/usage/billing?days=${days}`),
  providerHealth: () => apiFetch<{ providers: ProviderHealth[] }>('/api/admin/providers/health'),
  metrics: () => apiFetch<GatewayMetrics>('/api/admin/metrics'),
  auditLogs: () => apiFetch<{ logs: AuditLog[] }>('/api/admin/audit-logs?limit=100'),
  playgroundSearch: (payload: Record<string, unknown>) => apiFetch('/api/admin/playground/search', { method: 'POST', body: JSON.stringify(payload) })
}
