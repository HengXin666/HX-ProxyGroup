import type {
  ApiErrorPayload,
  ArtifactKind,
  ArtifactList,
  ArtifactRecord,
  BatchRefreshList,
  CreateListenerRequest,
  CreateProxyGroupRequest,
  CreateProxyServiceRequest,
  CreateSubscriptionRequest,
  DataPlaneStatus,
  ListenerList,
  ListenerRecord,
  NodeCheckResult,
  NodeCheckList,
  NodeList,
  NodeRecord,
  ProxyGroup,
  ProxyGroupList,
  ProxyServiceRecord,
  RefreshResult,
  Subscription,
  SubscriptionList,
  VerifyResult,
} from "@/lib/types"

export class ApiError extends Error {
  readonly status: number
  readonly code?: string
  readonly requestId?: string

  constructor(message: string, status: number, code?: string, requestId?: string) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = code
    this.requestId = requestId
  }
}

let csrfToken = ""
let onUnauthenticated: (() => void) | null = null

export function setCsrfToken(token: string) {
  csrfToken = token
}

export function setUnauthenticatedHandler(handler: (() => void) | null) {
  onUnauthenticated = handler
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers)
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json")
  }
  const method = (init?.method ?? "GET").toUpperCase()
  if (csrfToken && method !== "GET" && method !== "HEAD") {
    headers.set("X-CSRF-Token", csrfToken)
  }

  const response = await fetch(path, {
    ...init,
    headers,
  })
  if (response.status === 401 && !path.startsWith("/api/v1/auth/")) {
    onUnauthenticated?.()
  }
  if (!response.ok) {
    let payload: ApiErrorPayload | undefined
    try {
      payload = (await response.json()) as ApiErrorPayload
    } catch {
      payload = undefined
    }
    throw new ApiError(
      payload?.error?.message || `请求失败：HTTP ${response.status}`,
      response.status,
      payload?.error?.code,
      payload?.error?.request_id,
    )
  }
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

export type AlertRecord = {
  id: string
  rule: string
  target_id: string
  target_name: string
  severity: "warning" | "critical"
  status: "firing" | "resolved"
  message: string
  fired_at: string
  resolved_at?: string
  last_notified_at?: string
  notify_count: number
  acknowledged: boolean
}

export type AlertList = { items: AlertRecord[] }

export type AlertSettings = {
  enabled: boolean
  configured: boolean
  host?: string
  port?: number
  security?: string
  username?: string
  has_password: boolean
  from?: string
  to?: string[]
}

export type UpdateAlertSettingsRequest = {
  enabled: boolean
  host: string
  port: number
  security: string
  username: string
  password: string
  from: string
  to: string[]
}

export type TerminalStatus = {
  enabled: boolean
  active_sessions: number
  max_sessions: number
  idle_timeout_seconds: number
}

export type AuthStatus = {
  configured: boolean
  authenticated: boolean
  username?: string
  csrf_token?: string
}

export type LoginResult = {
  username: string
  csrf_token: string
  expires_at: string
}

export const api = {
  authStatus(): Promise<AuthStatus> {
    return request("/api/v1/auth/status")
  },

  authSetup(setupToken: string, username: string, password: string): Promise<void> {
    return request("/api/v1/auth/setup", {
      method: "POST",
      body: JSON.stringify({ setup_token: setupToken, username, password }),
    })
  },

  async login(username: string, password: string): Promise<LoginResult> {
    const result = await request<LoginResult>("/api/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    })
    setCsrfToken(result.csrf_token)
    return result
  },

  async logout(): Promise<void> {
    await request("/api/v1/auth/logout", { method: "POST" })
    setCsrfToken("")
  },

  changePassword(currentPassword: string, newPassword: string): Promise<void> {
    return request("/api/v1/auth/password", {
      method: "PUT",
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
    })
  },

  listAlerts(status?: "firing" | "resolved"): Promise<AlertList> {
    const query = new URLSearchParams({ limit: "200" })
    if (status) query.set("status", status)
    return request(`/api/v1/alerts?${query.toString()}`)
  },

  acknowledgeAlert(id: string): Promise<void> {
    return request(`/api/v1/alerts/${encodeURIComponent(id)}/ack`, { method: "POST" })
  },

  alertSettings(): Promise<AlertSettings> {
    return request("/api/v1/alerts/settings")
  },

  updateAlertSettings(payload: UpdateAlertSettingsRequest): Promise<AlertSettings> {
    return request("/api/v1/alerts/settings", {
      method: "PUT",
      body: JSON.stringify(payload),
    })
  },

  testAlertSettings(): Promise<void> {
    return request("/api/v1/alerts/settings/test", { method: "POST" })
  },

  terminalStatus(): Promise<TerminalStatus> {
    return request("/api/v1/terminal/status")
  },

  terminalSocketURL(): string {
    const scheme = window.location.protocol === "https:" ? "wss" : "ws"
    return `${scheme}://${window.location.host}/api/v1/terminal/ws`
  },

  async health(): Promise<boolean> {
    try {
      const result = await request<{ status: string }>("/health/ready")
      return result.status === "ready"
    } catch {
      return false
    }
  },

  listSubscriptions(): Promise<SubscriptionList> {
    return request("/api/v1/subscriptions?limit=500&offset=0")
  },

  createSubscription(payload: CreateSubscriptionRequest): Promise<Subscription> {
    return request("/api/v1/subscriptions", {
      method: "POST",
      body: JSON.stringify(payload),
    })
  },

  refreshSubscription(id: string): Promise<RefreshResult> {
    return request(`/api/v1/subscriptions/${encodeURIComponent(id)}/refresh`, {
      method: "POST",
    })
  },

  refreshSubscriptions(subscriptionIds: string[]): Promise<BatchRefreshList> {
    return request("/api/v1/subscriptions?action=refresh", {
      method: "POST",
      body: JSON.stringify({ subscription_ids: subscriptionIds }),
    })
  },

  listNodes(filters?: { search?: string; protocol?: string; state?: string }): Promise<NodeList> {
    const query = new URLSearchParams({ limit: "1000", offset: "0" })
    if (filters?.search) query.set("search", filters.search)
    if (filters?.protocol) query.set("protocol", filters.protocol)
    if (filters?.state) query.set("state", filters.state)
    return request(`/api/v1/nodes?${query.toString()}`)
  },

  checkNode(id: string): Promise<NodeCheckResult> {
    return request(`/api/v1/nodes/${encodeURIComponent(id)}/check`, { method: "POST" })
  },

  disableNode(id: string): Promise<NodeRecord> {
    return request(`/api/v1/nodes/${encodeURIComponent(id)}/disable`, { method: "POST" })
  },

  enableNode(id: string): Promise<NodeRecord> {
    return request(`/api/v1/nodes/${encodeURIComponent(id)}/enable`, { method: "POST" })
  },

  checkNodes(nodeIds: string[]): Promise<NodeCheckList> {
    return request("/api/v1/nodes", {
      method: "POST",
      body: JSON.stringify({ node_ids: nodeIds }),
    })
  },

  deleteSubscription(id: string, version: number): Promise<void> {
    return request(
      `/api/v1/subscriptions/${encodeURIComponent(id)}?version=${encodeURIComponent(version)}`,
      { method: "DELETE" },
    )
  },

  listProxyGroups(): Promise<ProxyGroupList> {
    return request("/api/v1/proxy-groups")
  },

  createProxyGroup(payload: CreateProxyGroupRequest): Promise<ProxyGroup> {
    return request("/api/v1/proxy-groups", {
      method: "POST",
      body: JSON.stringify(payload),
    })
  },

  deleteProxyGroup(id: string, version: number): Promise<void> {
    return request(`/api/v1/proxy-groups/${encodeURIComponent(id)}?version=${version}`, {
      method: "DELETE",
    })
  },

  listListeners(): Promise<ListenerList> {
    return request("/api/v1/listeners")
  },

  createListener(payload: CreateListenerRequest): Promise<ListenerRecord> {
    return request("/api/v1/listeners", {
      method: "POST",
      body: JSON.stringify(payload),
    })
  },

  createProxyService(payload: CreateProxyServiceRequest): Promise<ProxyServiceRecord> {
    return request("/api/v1/proxy-services", {
      method: "POST",
      body: JSON.stringify(payload),
    })
  },

  deleteListener(id: string, version: number): Promise<void> {
    return request(`/api/v1/listeners/${encodeURIComponent(id)}?version=${version}`, {
      method: "DELETE",
    })
  },

  dataPlaneStatus(): Promise<DataPlaneStatus> {
    return request("/api/v1/dataplane/status")
  },

  applyDataPlane(): Promise<DataPlaneStatus> {
    return request("/api/v1/dataplane/apply", { method: "POST" })
  },

  listArtifacts(kind: ArtifactKind): Promise<ArtifactList> {
    return request(`/api/v1/${kind === "backup" ? "backups" : "exports"}`)
  },

  createArtifact(kind: ArtifactKind, description: string): Promise<ArtifactRecord> {
    return request(`/api/v1/${kind === "backup" ? "backups" : "exports"}`, {
      method: "POST",
      body: JSON.stringify({ description, include_secrets: false }),
    })
  },

  verifyArtifact(kind: ArtifactKind, id: string): Promise<VerifyResult> {
    const collection = kind === "backup" ? "backups" : "exports"
    return request(`/api/v1/${collection}/${encodeURIComponent(id)}/verify`, {
      method: "POST",
    })
  },

  deleteArtifact(kind: ArtifactKind, id: string): Promise<void> {
    const collection = kind === "backup" ? "backups" : "exports"
    return request(`/api/v1/${collection}/${encodeURIComponent(id)}`, {
      method: "DELETE",
    })
  },

  artifactDownloadURL(kind: ArtifactKind, id: string): string {
    const collection = kind === "backup" ? "backups" : "exports"
    return `/api/v1/${collection}/${encodeURIComponent(id)}/download`
  },
}
