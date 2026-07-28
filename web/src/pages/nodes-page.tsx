import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Activity, ArrowUpDown, Ban, ChevronDown, ChevronRight, CircleDot, CirclePlay, Filter, Gauge, LoaderCircle, Network, Play, Radio, RefreshCw, Search, Settings2, Square } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api, type NodeQualitySettings } from "@/lib/api"
import type { NodeRecord, Subscription, TrafficSummary } from "@/lib/types"
import { cn, compactId, formatBytes, formatDate } from "@/lib/utils"

interface NodesPageProps {
  onNotice: (message: string, tone?: "success" | "error") => void
}

const states = ["", "candidate", "healthy", "degraded", "quarantined", "disabled", "retired"] as const
type SortKey = "latency" | "name" | "state" | "protocol" | "checked"
type SortDirection = "asc" | "desc"
const stateMeta: Record<NodeRecord["lifecycle_state"], { label: string; variant: "default" | "success" | "warning" | "destructive" | "secondary" }> = {
  candidate: { label: "未检测", variant: "default" },
  healthy: { label: "健康", variant: "success" },
  degraded: { label: "降级", variant: "warning" },
  quarantined: { label: "隔离", variant: "destructive" },
  disabled: { label: "已禁用", variant: "secondary" },
  retired: { label: "已退役", variant: "secondary" },
}

export function NodesPage({ onNotice }: NodesPageProps) {
  const [items, setItems] = useState<NodeRecord[]>([])
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [traffic, setTraffic] = useState<Map<string, TrafficSummary>>(new Map())
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(true)
  const [checking, setChecking] = useState<Record<string, boolean>>({})
  const [search, setSearch] = useState("")
  const [protocol, setProtocol] = useState("")
  const [state, setState] = useState("")
  const [settings, setSettings] = useState<NodeQualitySettings | null>(null)
  const [sortKey, setSortKey] = useState<SortKey>("latency")
  const [sortDirection, setSortDirection] = useState<SortDirection>("asc")
  const [batchProgress, setBatchProgress] = useState<{ completed: number; total: number } | null>(null)
  const batchController = useRef<AbortController | null>(null)

  useEffect(() => {
    api.nodeQualitySettings().then(setSettings).catch(() => setSettings(null))
  }, [])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const result = await api.listNodes({ search: search.trim(), protocol, state })
      setItems(result.items.map(normalizeNode))
      const [subscriptionResult, trafficResult] = await Promise.allSettled([
        api.listSubscriptions(),
        api.trafficSummaries("node"),
      ])
      if (subscriptionResult.status === "fulfilled") {
        setSubscriptions(subscriptionResult.value.items)
        setExpanded((current) => current.size ? current : new Set(subscriptionResult.value.items.map((item) => item.id)))
      }
      if (trafficResult.status === "fulfilled") {
        setTraffic(new Map(trafficResult.value.items.map((item) => [item.resource_id, item])))
      }
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "加载节点失败", "error")
    } finally {
      setLoading(false)
    }
  }, [onNotice, protocol, search, state])

  useEffect(() => {
    const timer = window.setTimeout(() => void load(), 180)
    return () => window.clearTimeout(timer)
  }, [load])

  async function checkNode(item: NodeRecord) {
    setChecking((current) => ({ ...current, [item.id]: true }))
    try {
      const result = await api.checkNode(item.id)
      setItems((current) => current.map((candidate) => candidate.id === item.id ? mergeCheckedNode(candidate, result.node) : candidate))
      onNotice(
        result.success
          ? `检测成功：${result.latency_ms ?? "—"} ms`
          : `检测失败：${errorCodeLabel(result.error_code)}${result.error ? `（${result.error}）` : ""}`,
        result.success ? "success" : "error",
      )
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "节点检测失败", "error")
    } finally {
      setChecking((current) => ({ ...current, [item.id]: false }))
    }
  }

  async function checkVisibleNodes() {
    const ids = items.filter((item) => item.lifecycle_state !== "disabled" && item.lifecycle_state !== "retired").map((item) => item.id)
    if (ids.length === 0) {
      onNotice("当前筛选结果中没有可测速节点", "error")
      return
    }
    const selected = new Set(ids)
    const controller = new AbortController()
    batchController.current = controller
    setBatchProgress({ completed: 0, total: ids.length })
    setChecking(Object.fromEntries(ids.map((id) => [id, true])))
    setItems((current) => current.map((item) => selected.has(item.id) ? clearDisplayedQuality(item) : item))
    let failed = 0
    try {
      await api.streamNodeChecks(ids, (progress) => {
        if (!progress.result.success) failed++
        setItems((current) => current.map((candidate) => candidate.id === progress.node_id
          ? mergeCheckedNode(candidate, progress.result.node)
          : candidate))
        setChecking((current) => ({ ...current, [progress.node_id]: false }))
        setBatchProgress({ completed: progress.completed, total: progress.total })
      }, controller.signal)
      onNotice(`批量测速完成：${ids.length - failed} 个成功，${failed} 个失败`, failed ? "error" : "success")
    } catch (error) {
      if (controller.signal.aborted) onNotice("已停止批量测速", "error")
      else onNotice(error instanceof Error ? error.message : "批量测速失败", "error")
    } finally {
      setChecking({})
      setBatchProgress(null)
      batchController.current = null
    }
  }

  async function toggleNode(item: NodeRecord) {
    const disabling = item.lifecycle_state !== "disabled"
    try {
      const updated = disabling ? await api.disableNode(item.id) : await api.enableNode(item.id)
      setItems((current) => current.map((candidate) => candidate.id === item.id ? updated : candidate))
      onNotice(disabling ? "节点已禁用，配置已重新下发" : "节点已恢复为未检测状态")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "节点状态修改失败", "error")
    }
  }

  const protocols = useMemo(() => Array.from(new Set(items.map((item) => item.protocol))).sort(), [items])
  const metrics = useMemo(() => ({
    healthy: items.filter((item) => item.lifecycle_state === "healthy").length,
    degraded: items.filter((item) => item.lifecycle_state === "degraded").length,
    quarantined: items.filter((item) => item.lifecycle_state === "quarantined").length,
    unchecked: items.filter((item) => item.lifecycle_state === "candidate").length,
  }), [items])
  const groups = useMemo(() => {
    const bySubscription = subscriptions.map((subscription) => ({
      subscription,
      nodes: sortNodes(items.filter((node) => node.sources.some((source) => source.subscription_id === subscription.id)), sortKey, sortDirection),
    })).filter((group) => group.nodes.length > 0 || (!search && !protocol && !state))
    const knownSubscriptions = new Set(subscriptions.map((subscription) => subscription.id))
    const unassigned = sortNodes(items.filter((node) => node.sources.length === 0 || node.sources.every((source) => !knownSubscriptions.has(source.subscription_id))), sortKey, sortDirection)
    return { bySubscription, unassigned }
  }, [items, protocol, search, sortDirection, sortKey, state, subscriptions])

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">节点</h1>
          <p className="mt-1 max-w-3xl text-sm leading-5 text-muted-foreground">
            节点按稳定指纹去重。质量检测由 Mihomo 经指定节点访问测试 URL，不以服务器端口可连接代替代理可用性。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button onClick={() => batchProgress ? batchController.current?.abort() : void checkVisibleNodes()} disabled={loading || items.length === 0}>
            {batchProgress ? <Square /> : <Play />}
            {batchProgress ? `${batchProgress.completed}/${batchProgress.total}` : "批量测速"}
          </Button>
          <Button variant="outline" onClick={() => { window.location.hash = "/settings" }}>
            <Settings2 />全局配置
          </Button>
          <Button variant="outline" onClick={() => void load()} disabled={loading}>
            <RefreshCw className={loading ? "animate-spin" : ""} />刷新
          </Button>
        </div>
      </div>

      <div className="grid overflow-hidden rounded-lg border bg-white sm:grid-cols-4">
        <Metric label="健康" value={metrics.healthy} helper="最近测试成功且延迟正常" />
        <Metric label="降级" value={metrics.degraded} helper="高延迟或近期检测失败" border />
        <Metric label="隔离" value={metrics.quarantined} helper="连续失败三次" border />
        <Metric label="未检测" value={metrics.unchecked} helper="等待自动或手工检测" border />
      </div>

      <section className="overflow-hidden rounded-lg border bg-white">
        <div className="flex flex-col gap-3 border-b bg-[#f6f8fa] px-3 py-3 lg:flex-row lg:items-center">
          <div className="relative w-full max-w-md">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-[#8c959f]" />
            <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索节点名称或指纹" className="pl-8" />
          </div>
          <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
            <Filter className="mr-1 size-3.5 shrink-0 text-muted-foreground" />
            <FilterChip active={protocol === ""} onClick={() => setProtocol("")}>全部协议</FilterChip>
            {protocols.map((value) => <FilterChip key={value} active={protocol === value} onClick={() => setProtocol(value)}>{value.toUpperCase()}</FilterChip>)}
          </div>
          <div className="flex min-w-0 items-center gap-1 overflow-x-auto">
            {states.map((value) => <FilterChip key={value || "all"} active={state === value} onClick={() => setState(value)}>{value ? stateMeta[value].label : "全部状态"}</FilterChip>)}
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            <select value={sortKey} onChange={(event) => setSortKey(event.target.value as SortKey)} className="h-8 w-[118px] rounded-md border bg-white px-2.5 text-xs" aria-label="节点排序字段">
              <option value="latency">按延迟</option>
              <option value="name">按名称</option>
              <option value="state">按状态</option>
              <option value="protocol">按协议</option>
              <option value="checked">按检测时间</option>
            </select>
            <Button variant="outline" size="icon" title={sortDirection === "asc" ? "切换为降序" : "切换为升序"} aria-label={sortDirection === "asc" ? "切换为降序" : "切换为升序"} onClick={() => setSortDirection((current) => current === "asc" ? "desc" : "asc")}>
              <ArrowUpDown />
            </Button>
          </div>
        </div>

        {loading ? <Loading /> : items.length === 0 ? <Empty /> : <div className="divide-y">
          {groups.bySubscription.map(({ subscription, nodes }) => <NodeSubscriptionGroup key={subscription.id} name={subscription.name} sourceType={subscription.source_type} nodes={nodes} traffic={traffic} open={expanded.has(subscription.id)} onToggle={() => setExpanded((current) => { const next = new Set(current); if (next.has(subscription.id)) next.delete(subscription.id); else next.add(subscription.id); return next })} checking={checking} onCheck={checkNode} onToggleNode={toggleNode} />)}
          {groups.unassigned.length > 0 && <NodeSubscriptionGroup name="自定义、未归组或来源暂不可用" sourceType="inline" nodes={groups.unassigned} traffic={traffic} open={expanded.has("unassigned")} onToggle={() => setExpanded((current) => { const next = new Set(current); if (next.has("unassigned")) next.delete("unassigned"); else next.add("unassigned"); return next })} checking={checking} onCheck={checkNode} onToggleNode={toggleNode} />}
        </div>}
      </section>

      <div className="rounded-md border border-[#b6d8ff] bg-[#ddf4ff] px-3 py-2 text-xs leading-5 text-[#0550ae]">
        自动检测每分钟扫描一次，到期节点复测间隔
        {settings ? `为 ${formatInterval(settings.check_interval_seconds)}（可在「检测设置」中调整）` : "默认 10 分钟"}
        ；成功延迟超过 1500 ms 标记为降级，连续失败三次进入隔离，后续成功会自动恢复。
      </div>
    </div>
  )
}

function NodeSubscriptionGroup({ name, sourceType, nodes, traffic, open, onToggle, checking, onCheck, onToggleNode }: { name: string; sourceType: Subscription["source_type"]; nodes: NodeRecord[]; traffic: Map<string, TrafficSummary>; open: boolean; onToggle: () => void; checking: Record<string, boolean>; onCheck: (node: NodeRecord) => Promise<void>; onToggleNode: (node: NodeRecord) => Promise<void> }) {
  const healthy = nodes.filter((node) => node.lifecycle_state === "healthy").length
  return <section>
    <Button variant="ghost" className="h-auto w-full justify-start rounded-none px-3 py-3 text-left" onClick={onToggle}>
      {open ? <ChevronDown /> : <ChevronRight />}<span className="flex size-8 items-center justify-center rounded-md border bg-white"><Radio className="size-4" /></span><span className="min-w-0 flex-1"><span className="block truncate text-sm font-semibold">{name}</span><span className="block text-[11px] font-normal text-muted-foreground">{sourceType === "remote" ? "远程订阅" : sourceType === "file" ? "服务器文件" : "内联 / 自定义"}</span></span><Badge variant="secondary">{nodes.length} 个节点</Badge><Badge variant={healthy ? "success" : "secondary"}>{healthy} 健康</Badge>
    </Button>
    {open && <div className="overflow-x-auto border-t"><table className="w-full min-w-[1220px] border-collapse text-left"><thead className="bg-muted text-xs text-muted-foreground"><tr><Th>节点</Th><Th>协议</Th><Th>状态</Th><Th>延迟</Th><Th>站点健康</Th><Th>累计流量</Th><Th>失败次数</Th><Th>最近检测</Th><Th>指纹</Th><Th align="right">操作</Th></tr></thead><tbody className="divide-y">{nodes.length ? nodes.map((item) => <NodeRow key={item.id} item={item} traffic={traffic.get(item.id)} checking={Boolean(checking[item.id])} onCheck={() => void onCheck(item)} onToggle={() => void onToggleNode(item)} />) : <tr><td colSpan={10} className="px-4 py-8 text-center text-xs text-muted-foreground">该订阅当前没有匹配节点</td></tr>}</tbody></table></div>}
  </section>
}

const errorCodeLabels: Record<string, string> = {
  node_unreachable: "节点不可用",
  timeout: "测速超时",
  connect_failed: "连接被拒绝",
  dataplane_down: "数据面未运行",
  proxy_not_found: "配置缺少该节点",
  http_failed: "HTTP 响应异常",
  probe_failed: "检测失败",
}

function errorCodeLabel(code?: string): string {
  if (!code) return "检测失败"
  return errorCodeLabels[code] ?? code
}

function normalizeNode(node: NodeRecord): NodeRecord {
  return {
    ...node,
    sources: Array.isArray(node.sources) ? node.sources : [],
    health_checks: Array.isArray(node.health_checks) ? node.health_checks : [],
  }
}

function mergeCheckedNode(previous: NodeRecord, checked: NodeRecord): NodeRecord {
  const next = normalizeNode(checked)
  return { ...next, sources: next.sources.length ? next.sources : previous.sources, source_count: next.source_count || previous.source_count }
}

function clearDisplayedQuality(node: NodeRecord): NodeRecord {
  return {
    ...node,
    lifecycle_state: node.lifecycle_state === "disabled" || node.lifecycle_state === "retired" ? node.lifecycle_state : "candidate",
    last_checked_at: undefined,
    last_latency_ms: undefined,
    last_error_code: undefined,
    last_error_message: undefined,
    consecutive_probe_failures: 0,
  }
}

const stateOrder: Record<NodeRecord["lifecycle_state"], number> = {
  healthy: 0, candidate: 1, degraded: 2, quarantined: 3, disabled: 4, retired: 5,
}

function sortNodes(nodes: NodeRecord[], key: SortKey, direction: SortDirection): NodeRecord[] {
  const multiplier = direction === "asc" ? 1 : -1
  return [...nodes].sort((left, right) => {
    let compared = 0
    if (key === "latency") {
      if (left.last_latency_ms == null || right.last_latency_ms == null) {
        if (left.last_latency_ms == null && right.last_latency_ms == null) compared = 0
        else return left.last_latency_ms == null ? 1 : -1
      } else compared = left.last_latency_ms - right.last_latency_ms
    }
    if (key === "name") compared = left.display_name.localeCompare(right.display_name, "zh-CN")
    if (key === "state") compared = stateOrder[left.lifecycle_state] - stateOrder[right.lifecycle_state]
    if (key === "protocol") compared = left.protocol.localeCompare(right.protocol)
    if (key === "checked") compared = (left.last_checked_at ? Date.parse(left.last_checked_at) : 0) - (right.last_checked_at ? Date.parse(right.last_checked_at) : 0)
    return compared === 0 ? left.display_name.localeCompare(right.display_name, "zh-CN") : compared * multiplier
  })
}

function formatInterval(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`
  return `${seconds} 秒`
}

function NodeRow({ item, traffic, checking, onCheck, onToggle }: { item: NodeRecord; traffic?: TrafficSummary; checking: boolean; onCheck: () => void; onToggle: () => void }) {
  const state = stateMeta[item.lifecycle_state]
  const unavailable = item.lifecycle_state === "disabled" || item.lifecycle_state === "retired"
  const disabled = item.lifecycle_state === "disabled"
  const retired = item.lifecycle_state === "retired"
  return <tr className="hover:bg-[#f6f8fa]">
    <Td><div className="flex min-w-0 items-center gap-2.5"><div className="flex size-7 shrink-0 items-center justify-center rounded-md border bg-white text-[#57606a]"><CircleDot className="size-3.5" /></div><div className="min-w-0"><button type="button" className="block max-w-[300px] truncate font-medium text-foreground hover:text-primary hover:underline disabled:no-underline disabled:opacity-60" title={`${item.display_name} - 单击测速`} onClick={onCheck} disabled={checking || unavailable}>{item.display_name}</button><div className="mt-0.5 font-mono text-[11px] text-muted-foreground">{compactId(item.id)}</div></div></div></Td>
    <Td><Badge variant="outline">{item.protocol.toUpperCase()}</Badge></Td>
    <Td>
      <div className="flex items-center gap-1.5">
        <Badge variant={state.variant}>{state.label}</Badge>
        {item.last_error_code && (
          <span
            className="max-w-[160px] truncate text-[11px] text-[#a40e26]"
            title={item.last_error_message || item.last_error_code}
          >
            {errorCodeLabel(item.last_error_code)}
          </span>
        )}
      </div>
    </Td>
    <Td>{item.last_latency_ms == null ? "—" : <span className="inline-flex items-center gap-1 tabular-nums"><Gauge className="size-3.5" />{item.last_latency_ms} ms</span>}</Td>
    <Td><div className="flex max-w-[260px] flex-wrap gap-1">{item.health_checks.length === 0 ? <span className="text-muted-foreground">—</span> : item.health_checks.map((check) => <Badge key={check.target_id} variant={check.success ? "success" : "destructive"} title={check.latency_ms == null ? check.url : `${check.url} · ${check.latency_ms} ms`}>{check.target_name}{check.latency_ms == null ? "" : ` ${check.latency_ms}ms`}</Badge>)}</div></Td>
    <Td><span className="tabular-nums" title={`下载 ${formatBytes(traffic?.download_bytes ?? 0)} / 上传 ${formatBytes(traffic?.upload_bytes ?? 0)}`}>{formatBytes((traffic?.download_bytes ?? 0) + (traffic?.upload_bytes ?? 0))}</span></Td>
    <Td><span className={cn("tabular-nums", item.consecutive_probe_failures > 0 && "text-[#a40e26]")}>{item.consecutive_probe_failures}</span></Td>
    <Td>{formatDate(item.last_checked_at)}</Td>
    <Td><span className="font-mono text-[11px]" title={item.fingerprint}>{compactId(item.fingerprint)}</span></Td>
    <Td align="right"><div className="inline-flex items-center gap-1.5"><Button variant="outline" size="sm" onClick={onCheck} disabled={checking || unavailable}>{checking ? <LoaderCircle className="animate-spin" /> : <Activity />}复测</Button><Button variant="outline" size="sm" onClick={onToggle} disabled={retired}>{disabled ? <CirclePlay /> : <Ban />}{disabled ? "启用" : "禁用"}</Button></div></Td>
  </tr>
}

function Loading() { return <div className="flex min-h-48 items-center justify-center gap-2 text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />正在加载节点</div> }
function Empty() { return <div className="flex min-h-56 flex-col items-center justify-center px-6 text-center"><Network className="mb-3 size-8 text-[#8c959f]" /><div className="font-medium">没有匹配的节点</div><p className="mt-1 max-w-lg text-xs leading-5 text-muted-foreground">刷新一条可解析的订阅后，节点会进入库存并自动开始质量检测。</p></div> }
function Metric({ label, value, helper, border = false }: { label: string; value: number; helper: string; border?: boolean }) { return <div className={cn("px-4 py-3", border && "border-t sm:border-l sm:border-t-0")}><div className="text-xs text-muted-foreground">{label}</div><div className="mt-0.5 text-xl font-semibold tabular-nums">{value}</div><div className="mt-0.5 text-[11px] text-muted-foreground">{helper}</div></div> }
function FilterChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) { return <Button type="button" size="sm" variant={active ? "default" : "outline"} onClick={onClick} className="h-7 shrink-0 rounded-full px-2.5 text-[11px]">{children}</Button> }
function Th({ children, align = "left" }: { children: React.ReactNode; align?: "left" | "right" }) { return <th className={cn("whitespace-nowrap border-b px-3 py-2 font-medium", align === "right" && "text-right")}>{children}</th> }
function Td({ children, align = "left" }: { children: React.ReactNode; align?: "left" | "right" }) { return <td className={cn("whitespace-nowrap px-3 py-2.5 text-xs text-[#57606a]", align === "right" && "text-right")}>{children}</td> }
