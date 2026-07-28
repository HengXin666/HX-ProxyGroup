export type SourceType = "remote" | "inline" | "file"
export type ArtifactKind = "backup" | "export"

export interface Subscription {
  id: string
  name: string
  source_type: SourceType
  source_configured: boolean
  enabled: boolean
  refresh_interval_seconds: number
  refresh_cron?: string
  last_success_snapshot_id?: string
  consecutive_failures: number
  last_refresh_attempt_at?: string
  next_refresh_at?: string
  version: number
  created_at: string
  updated_at: string
}

export interface SubscriptionList {
  items: Subscription[]
  limit: number
  offset: number
}

export interface SourceConfig {
  url?: string
  headers?: Record<string, string>
  user_agent?: string
  inline?: string
  file_path?: string
  timeout_seconds?: number
  allow_private?: boolean
}

export interface CreateSubscriptionRequest {
  name: string
  source_type: SourceType
  source_config: SourceConfig
  enabled?: boolean
  refresh_interval_seconds?: number
  refresh_cron?: string
}

export interface UpdateSubscriptionRequest {
  version: number
  name: string
  source_type: SourceType
  source_config?: SourceConfig
  enabled: boolean
  refresh_interval_seconds: number
  refresh_cron?: string
}

export interface RefreshResult {
  subscription_id: string
  snapshot_id: string
  changed: boolean
  content_hash: string
  size: number
  detected_format: string
  estimated_nodes: number
  fetched_at: string
  fetch_metadata: {
    etag?: string
    last_modified?: string
    content_type?: string
    status_code?: number
    size: number
  }
}

export interface BatchRefreshList {
  items: Array<{
    subscription_id: string
    success: boolean
    result?: RefreshResult
    error?: string
  }>
}

export interface ArtifactRecord {
  schema_version: number
  id: string
  kind: ArtifactKind
  created_at: string
  filename: string
  content_type: string
  size: number
  sha256: string
  includes_secrets: boolean
  description?: string
}

export interface ArtifactList {
  items: ArtifactRecord[]
}

export interface VerifyResult {
  artifact_id: string
  kind: ArtifactKind
  valid: boolean
  files_checked: number
  artifact_sha256: string
  manifest_version: number
}

export interface NodeRecord {
  id: string
  fingerprint: string
  display_name: string
  protocol: string
  lifecycle_state: "candidate" | "healthy" | "degraded" | "quarantined" | "disabled" | "retired"
  first_seen_at: string
  last_seen_at: string
  retired_at?: string
  last_checked_at?: string
  last_latency_ms?: number
  last_error_code?: string
  last_error_message?: string
  consecutive_probe_failures: number
  version: number
  source_count: number
  sources: Array<{
    subscription_id: string
    subscription_name: string
    source_name: string
  }>
  health_checks: NodeHealthCheck[]
}

export interface NodeHealthCheck {
  target_id: string
  target_name: string
  url: string
  success: boolean
  latency_ms?: number
  checked_at?: string
  error_code?: string
}

export interface NodeCheckResult {
  node: NodeRecord
  success: boolean
  latency_ms?: number
  checked_at: string
  test_url: string
  error_code?: string
  error?: string
}

export interface NodeList {
  items: NodeRecord[]
  limit: number
  offset: number
}

export interface NodeCheckList {
  items: NodeCheckResult[]
}

export interface NodeCheckProgress {
  completed: number
  total: number
  node_id: string
  result: NodeCheckResult
}

export interface HealthTarget {
  id: string
  name: string
  url: string
  enabled: boolean
}

export interface GlobalSettings {
  quality: {
    check_interval_seconds: number
    timeout_seconds: number
    batch_size: number
    probe_concurrency: number
    test_url: string
    health_targets: HealthTarget[]
  }
  dns: {
    enabled: boolean
    ipv6: boolean
    enhanced_mode: "normal" | "fake-ip" | "redir-host"
    default_nameserver: string[]
    nameserver: string[]
    fallback: string[]
  }
  performance: {
    tcp_concurrent: boolean
    unified_delay: boolean
    keep_alive_idle_seconds: number
    keep_alive_interval_seconds: number
    find_process_mode: "off" | "strict" | "always"
    log_level: "silent" | "error" | "warning" | "info" | "debug"
  }
}

export interface RoutingRule {
  type: "domain" | "domain_suffix" | "domain_keyword" | "ip_cidr" | "geoip" | "geosite" | "process_name" | "network" | "dst_port"
  value: string
}

export interface RoutingRuleSet {
  id: string
  name: string
  enabled: boolean
  priority: number
  applied_group_ids: string[]
  action: { type: "reject" | "direct" | "proxy_group"; proxy_group_id?: string }
  rules: RoutingRule[]
}

export interface RoutingRulesConfig {
  rule_sets: RoutingRuleSet[]
}

export type ProxyGroupStrategy = "manual" | "url-test" | "fallback" | "round-robin" | "consistent-hashing" | "sticky-sessions"

export interface ProxyGroupSourceSpec {
  node_ids: string[]
  group_ids?: string[]
  subscription_ids?: string[]
  name_keywords?: string[]
  regions?: string[]
  protocols?: string[]
  states?: Array<"candidate" | "healthy" | "degraded" | "quarantined">
  max_latency_ms?: number
  sort_by?: "latency" | "name"
  limit?: number
  include_direct: boolean
  test_url?: string
  interval_seconds?: number
}

export interface ProxyGroup {
  id: string
  name: string
  strategy: ProxyGroupStrategy
  source_spec: ProxyGroupSourceSpec
  enabled: boolean
  empty_behavior: "fail-closed" | "direct"
  fallback_target_id?: string
  version: number
  created_at: string
  updated_at: string
}

export interface ProxyGroupList {
  items: ProxyGroup[]
}

export interface CreateProxyGroupRequest {
  name: string
  strategy: ProxyGroupStrategy
  source_spec: ProxyGroupSourceSpec
  enabled?: boolean
  empty_behavior?: "fail-closed" | "direct"
}

export interface UpdateProxyGroupRequest {
  version: number
  name: string
  strategy: ProxyGroupStrategy
  source_spec: ProxyGroupSourceSpec
  enabled: boolean
  empty_behavior: "fail-closed" | "direct"
  fallback_target_id?: string
}

export type ListenerKind = "http" | "socks" | "mixed" | "vless" | "vmess" | "trojan"

export interface ListenerTransport {
  type: "ws" | ""
  ws_path?: string
}

export interface ListenerPublicEndpoint {
  host: string
  port: number
  tls: boolean
}

export interface ListenerRecord {
  id: string
  name: string
  kind: ListenerKind
  bind_address: string
  port: number
  proxy_group_id: string
  auth_configured: boolean
  transport: ListenerTransport
  public_endpoint: ListenerPublicEndpoint
  share_path?: string
  enabled: boolean
  version: number
  created_at: string
  updated_at: string
}

export interface ListenerList {
  items: ListenerRecord[]
}

export interface CreateListenerRequest {
  name: string
  kind: ListenerKind
  bind_address: string
  port: number
  proxy_group_id: string
  auth?: {
    username: string
    password: string
  }
  transport?: ListenerTransport
  public_endpoint?: ListenerPublicEndpoint
  enabled?: boolean
}

export interface UpdateListenerRequest {
  version: number
  name: string
  kind: ListenerKind
  bind_address: string
  port: number
  proxy_group_id: string
  auth?: {
    username: string
    password: string
  }
  transport?: ListenerTransport
  public_endpoint?: ListenerPublicEndpoint
  clear_auth?: boolean
  enabled: boolean
}

export interface CreateProxyServiceRequest {
  name: string
  strategy: ProxyGroupStrategy
  source_spec: ProxyGroupSourceSpec
  empty_behavior?: "fail-closed" | "direct"
  listener: Omit<CreateListenerRequest, "proxy_group_id">
}

export interface ProxyServiceRecord {
  group: ProxyGroup
  listener: ListenerRecord
}

export interface DataPlaneEndpoint {
  id: string
  name: string
  kind: ListenerKind
  bind_address: string
  port: number
}

export interface DataPlaneStatus {
  available: boolean
  running: boolean
  state: "idle" | "running" | "failed"
  pid?: number
  binary?: string
  version?: string
  active_config?: string
  last_apply_at?: string
  last_error?: string
  listener_count: number
  proxy_count: number
  active_listeners: DataPlaneEndpoint[]
  egress_interface?: string
  max_procs: number
  log_max_bytes: number
}

export interface OverviewSample {
  timestamp: string
  upload_bytes_per_second: number
  download_bytes_per_second: number
  active_connections: number
  running: boolean
  error_code?: string
}

export type TrafficResourceType = "listener" | "proxy_group" | "node"

export interface TrafficSummary {
  resource_type: TrafficResourceType
  resource_id: string
  upload_bytes: number
  download_bytes: number
  connection_count: number
  active_connections: number
  updated_at?: string
}

export interface TrafficPoint {
  time: string
  upload_bytes: number
  download_bytes: number
  connection_count: number
  peak_active_connections: number
}

export interface TrafficSeries {
  summary: TrafficSummary
  from: string
  to: string
  resolution_seconds: number
  points: TrafficPoint[]
}

export interface TrafficSummaryList {
  items: TrafficSummary[]
  limit: number
  offset: number
}

export interface ProxyLogEvent {
  timestamp: string
  level: string
  message: string
  proxy_group_id?: string
  proxy_group?: string
  node_id?: string
  node?: string
}

export interface ApiErrorPayload {
  error?: {
    code?: string
    message?: string
    request_id?: string
  }
}
