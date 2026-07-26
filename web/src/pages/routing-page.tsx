import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react"
import { Cable, CheckCircle2, CircleAlert, Gauge, LoaderCircle, Network, Plus, RefreshCw, Server, Trash2 } from "lucide-react"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api } from "@/lib/api"
import type { DataPlaneStatus, ListenerKind, ListenerRecord, NodeRecord, ProxyGroup, ProxyGroupStrategy, Subscription } from "@/lib/types"
import { cn, formatDate } from "@/lib/utils"

interface RoutingPageProps {
  onNotice: (message: string, tone?: "success" | "error") => void
}

const strategies: Array<{ value: ProxyGroupStrategy; label: string }> = [
  { value: "url-test", label: "自动测速" },
  { value: "fallback", label: "故障切换" },
  { value: "round-robin", label: "轮询" },
  { value: "consistent-hashing", label: "一致性哈希" },
  { value: "sticky-sessions", label: "会话粘滞" },
  { value: "manual", label: "手动选择" },
]

const regions = [
  { value: "jp", label: "日本" }, { value: "hk", label: "香港" }, { value: "tw", label: "台湾" },
  { value: "sg", label: "新加坡" }, { value: "us", label: "美国" }, { value: "kr", label: "韩国" },
]

const kinds: Array<{ value: ListenerKind; label: string }> = [
  { value: "mixed", label: "Mixed" }, { value: "http", label: "HTTP" }, { value: "socks", label: "SOCKS5" },
]

export function RoutingPage({ onNotice }: RoutingPageProps) {
  const [nodes, setNodes] = useState<NodeRecord[]>([])
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [groups, setGroups] = useState<ProxyGroup[]>([])
  const [listeners, setListeners] = useState<ListenerRecord[]>([])
  const [status, setStatus] = useState<DataPlaneStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<{ listener: ListenerRecord; group?: ProxyGroup } | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [nodeResult, subscriptionResult, groupResult, listenerResult, statusResult] = await Promise.all([
        api.listNodes(), api.listSubscriptions(), api.listProxyGroups(), api.listListeners(), api.dataPlaneStatus(),
      ])
      setNodes(nodeResult.items.filter((item) => !["disabled", "retired"].includes(item.lifecycle_state)))
      setSubscriptions(subscriptionResult.items.filter((item) => item.enabled))
      setGroups(groupResult.items)
      setListeners(listenerResult.items)
      setStatus(statusResult)
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "加载代理服务失败", "error")
    } finally {
      setLoading(false)
    }
  }, [onNotice])

  useEffect(() => { void load() }, [load])

  async function apply() {
    setBusy(true)
    try {
      setStatus(await api.applyDataPlane())
      onNotice("Mihomo 配置已重新编译并应用")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "应用数据面失败", "error")
      setStatus(await api.dataPlaneStatus().catch(() => null))
    } finally { setBusy(false) }
  }

  async function removeService() {
    if (!deleteTarget) return
    setBusy(true)
    try {
      await api.deleteListener(deleteTarget.listener.id, deleteTarget.listener.version)
      if (deleteTarget.group) await api.deleteProxyGroup(deleteTarget.group.id, deleteTarget.group.version)
      setDeleteTarget(null)
      await load()
      onNotice("代理服务及其入口已删除")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "删除代理服务失败", "error")
    } finally { setBusy(false) }
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <h1 className="text-xl font-semibold">代理服务</h1>
          <p className="mt-1 max-w-3xl text-sm leading-5 text-muted-foreground">每项服务同时定义入口 IP、端口、登录账号，以及它可以使用的跨订阅节点规则。</p>
        </div>
        <div className="grid w-full grid-cols-2 gap-2 sm:flex sm:w-auto sm:shrink-0">
          <Button variant="outline" onClick={() => void load()} disabled={loading || busy}><RefreshCw className={loading ? "animate-spin" : ""} />刷新</Button>
          <Button onClick={() => void apply()} disabled={busy}>{busy ? <LoaderCircle className="animate-spin" /> : <Server />}重新应用</Button>
        </div>
      </div>

      <DataPlaneBand status={status} loading={loading} />

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1.2fr)_minmax(390px,0.8fr)]">
        <section className="overflow-hidden rounded-lg border bg-white">
          <PanelHeader title="服务列表" description="入口与节点策略保持一一对应" count={listeners.length} />
          {listeners.length === 0 ? <EmptyState /> : <div className="divide-y">{listeners.map((listener) => {
            const group = groups.find((item) => item.id === listener.proxy_group_id)
            return <ServiceRow key={listener.id} listener={listener} group={group} onDelete={() => setDeleteTarget({ listener, group })} />
          })}</div>}
        </section>
        <CreateProxyServiceForm nodes={nodes} subscriptions={subscriptions} onCreated={load} onNotice={onNotice} />
      </div>

      <ConfirmDialog open={deleteTarget !== null} title="删除代理服务" description={`将关闭 ${deleteTarget?.listener.bind_address ?? ""}:${deleteTarget?.listener.port ?? ""}，并删除其专属节点策略。`} busy={busy} onCancel={() => setDeleteTarget(null)} onConfirm={() => void removeService()} />
    </div>
  )
}

function ServiceRow({ listener, group, onDelete }: { listener: ListenerRecord; group?: ProxyGroup; onDelete: () => void }) {
  const spec = group?.source_spec
  const sourceText = spec?.subscription_ids?.length
    ? `${spec.subscription_ids.length} 个订阅${spec.regions?.length ? ` · ${spec.regions.map(regionLabel).join("/")}` : ""} · Top ${spec.limit || "全部"}`
    : `${spec?.node_ids.length || 0} 个固定节点`
  return (
    <div className="grid gap-3 px-3 py-3 hover:bg-[#f6f8fa] sm:grid-cols-[minmax(0,1fr)_minmax(220px,0.8fr)_32px] sm:items-center">
      <div className="flex min-w-0 items-start gap-2.5">
        <div className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-white text-[#57606a]"><Cable className="size-4" /></div>
        <div className="min-w-0"><div className="flex flex-wrap items-center gap-1.5"><span className="font-medium">{group?.name || listener.name}</span><Badge variant="outline">{listener.kind.toUpperCase()}</Badge><Badge variant={listener.auth_configured ? "warning" : "secondary"}>{listener.auth_configured ? "账号认证" : "无认证"}</Badge></div><div className="mt-1 font-mono text-[11px] text-muted-foreground">{listener.bind_address}:{listener.port}</div></div>
      </div>
      <div className="min-w-0"><div className="flex items-center gap-1.5 text-xs font-medium"><Network className="size-3.5" />{group ? strategyLabel(group.strategy) : "组已缺失"}</div><div className="mt-1 truncate text-[11px] text-muted-foreground" title={sourceText}>{sourceText}</div></div>
      <Button variant="ghost" size="icon" onClick={onDelete} aria-label={`删除 ${listener.name}`}><Trash2 className="text-destructive" /></Button>
    </div>
  )
}

function CreateProxyServiceForm({ nodes, subscriptions, onCreated, onNotice }: { nodes: NodeRecord[]; subscriptions: Subscription[]; onCreated: () => Promise<void>; onNotice: RoutingPageProps["onNotice"] }) {
  const [name, setName] = useState("")
  const [strategy, setStrategy] = useState<ProxyGroupStrategy>("url-test")
  const [sourceMode, setSourceMode] = useState<"rule" | "manual">("rule")
  const [selectedSubscriptions, setSelectedSubscriptions] = useState<string[]>([])
  const [selectedNodes, setSelectedNodes] = useState<string[]>([])
  const [selectedRegions, setSelectedRegions] = useState<string[]>(["jp"])
  const [limit, setLimit] = useState(5)
  const [maxLatency, setMaxLatency] = useState(2000)
  const [kind, setKind] = useState<ListenerKind>("mixed")
  const [bindAddress, setBindAddress] = useState("127.0.0.1")
  const [port, setPort] = useState(7890)
  const [authEnabled, setAuthEnabled] = useState(false)
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [submitting, setSubmitting] = useState(false)

  const estimated = useMemo(() => sourceMode === "manual" ? selectedNodes.length : nodes.filter((node) => {
    if (!node.sources.some((source) => selectedSubscriptions.includes(source.subscription_id))) return false
    if (node.last_latency_ms == null || node.last_latency_ms > maxLatency) return false
    return selectedRegions.length === 0 || selectedRegions.some((region) => matchesRegion(node.display_name, region))
  }).slice(0, limit).length, [limit, maxLatency, nodes, selectedNodes.length, selectedRegions, selectedSubscriptions, sourceMode])

  async function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitting(true)
    try {
      await api.createProxyService({
        name: name.trim(), strategy,
        source_spec: sourceMode === "rule" ? {
          node_ids: [], subscription_ids: selectedSubscriptions, regions: selectedRegions, states: ["candidate", "healthy", "degraded"],
          max_latency_ms: maxLatency, sort_by: "latency", limit, include_direct: false,
        } : { node_ids: selectedNodes, include_direct: false },
        empty_behavior: "fail-closed",
        listener: { name: `${name.trim()} 入口`, kind, bind_address: bindAddress.trim(), port, auth: authEnabled ? { username: username.trim(), password } : undefined },
      })
      setName("")
      await onCreated()
      onNotice(`代理服务已启动：${bindAddress}:${port}`)
    } catch (error) { onNotice(error instanceof Error ? error.message : "创建代理服务失败", "error") }
    finally { setSubmitting(false) }
  }

  const validSource = sourceMode === "rule" ? selectedSubscriptions.length > 0 : selectedNodes.length > 0
  return (
    <form onSubmit={submit} className="overflow-hidden rounded-lg border bg-white">
      <PanelHeader title="新建代理服务" description="入口与聚合规则一次创建" />
      <div className="space-y-4 p-3">
        <Field label="服务名称"><Input value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：日本低延迟出口" required /></Field>
        <Field label="节点来源"><ChipSet values={[{ value: "rule", label: "跨订阅规则" }, { value: "manual", label: "固定节点" }]} current={sourceMode} onChange={setSourceMode} /></Field>
        {sourceMode === "rule" ? <>
          <Field label={`订阅（已选 ${selectedSubscriptions.length}）`}><CheckList items={subscriptions.map((item) => ({ id: item.id, label: item.name }))} selected={selectedSubscriptions} onChange={setSelectedSubscriptions} empty="暂无已启用订阅" /></Field>
          <Field label="地区"><MultiChipSet values={regions} current={selectedRegions} onChange={setSelectedRegions} /></Field>
          <div className="grid grid-cols-2 gap-2"><Field label="最多节点"><Input type="number" min={1} max={500} value={limit} onChange={(event) => setLimit(Number(event.target.value))} /></Field><Field label="最大延迟 ms"><Input type="number" min={1} max={60000} value={maxLatency} onChange={(event) => setMaxLatency(Number(event.target.value))} /></Field></div>
          <div className="flex items-center gap-2 rounded-md border bg-[#f6f8fa] px-3 py-2 text-xs"><Gauge className="size-4 text-[#0969da]" />当前指标预计命中 {estimated} 个节点</div>
        </> : <Field label={`固定节点（已选 ${selectedNodes.length}）`}><CheckList items={nodes.map((item) => ({ id: item.id, label: `${item.display_name} · ${item.last_latency_ms == null ? "未测试" : `${item.last_latency_ms}ms`}` }))} selected={selectedNodes} onChange={setSelectedNodes} empty="暂无活动节点" /></Field>}
        <Field label="出站策略"><ChipSet values={strategies} current={strategy} onChange={setStrategy} /></Field>
        <div className="border-t pt-3"><div className="mb-3 text-xs font-semibold">入口与登录</div><div className="space-y-3"><Field label="入口协议"><ChipSet values={kinds} current={kind} onChange={setKind} /></Field><div className="grid grid-cols-[1fr_110px] gap-2"><Field label="绑定 IP"><Input value={bindAddress} onChange={(event) => setBindAddress(event.target.value)} required /></Field><Field label="端口"><Input type="number" min={1} max={65535} value={port} onChange={(event) => setPort(Number(event.target.value))} required /></Field></div><label className="flex items-center gap-2 rounded-md border bg-[#f6f8fa] px-3 py-2 text-xs"><input type="checkbox" checked={authEnabled} onChange={(event) => setAuthEnabled(event.target.checked)} className="size-4 accent-[#0969da]" />启用用户名密码认证</label>{authEnabled && <div className="grid grid-cols-2 gap-2"><Field label="用户名"><Input value={username} onChange={(event) => setUsername(event.target.value)} required /></Field><Field label="密码"><Input type="password" value={password} onChange={(event) => setPassword(event.target.value)} required /></Field></div>}</div></div>
        <Button type="submit" disabled={submitting || !name.trim() || !validSource || (authEnabled && (!username.trim() || !password))} className="w-full">{submitting ? <LoaderCircle className="animate-spin" /> : <Plus />}创建并启动</Button>
      </div>
    </form>
  )
}

function DataPlaneBand({ status, loading }: { status: DataPlaneStatus | null; loading: boolean }) {
  const running = status?.running === true
  return <section className="grid overflow-hidden rounded-lg border bg-white md:grid-cols-[220px_1fr]"><div className={cn("flex items-center gap-3 px-4 py-4", running ? "bg-[#dafbe1]" : status?.state === "failed" ? "bg-[#ffebe9]" : "bg-[#f6f8fa]")}>{loading ? <LoaderCircle className="size-5 animate-spin" /> : running ? <CheckCircle2 className="size-5 text-[#1a7f37]" /> : <CircleAlert className="size-5 text-[#bf8700]" />}<div><div className="font-semibold">{loading ? "检测中" : running ? "Mihomo 运行中" : status?.available ? "Mihomo 空闲" : "Mihomo 不可用"}</div><div className="mt-0.5 text-[11px] text-muted-foreground">{status?.listener_count ?? 0} 个活动入口</div></div></div><div className="grid gap-2 px-4 py-3 text-xs sm:grid-cols-3"><Info label="进程" value={status?.pid ? `PID ${status.pid}` : "—"} /><Info label="最近应用" value={formatDate(status?.last_apply_at)} /><Info label="版本" value={status?.version || "—"} /></div></section>
}

function CheckList({ items, selected, onChange, empty }: { items: Array<{ id: string; label: string }>; selected: string[]; onChange: (ids: string[]) => void; empty: string }) {
  return <div className="max-h-40 overflow-y-auto rounded-md border">{items.length === 0 ? <div className="px-3 py-4 text-center text-xs text-muted-foreground">{empty}</div> : items.map((item) => <label key={item.id} className="flex cursor-pointer items-center gap-2 border-b px-3 py-2 text-xs last:border-b-0 hover:bg-[#f6f8fa]"><input type="checkbox" checked={selected.includes(item.id)} onChange={(event) => onChange(event.target.checked ? [...selected, item.id] : selected.filter((id) => id !== item.id))} className="size-4 accent-[#0969da]" /><span className="min-w-0 flex-1 truncate">{item.label}</span></label>)}</div>
}

function PanelHeader({ title, description, count }: { title: string; description: string; count?: number }) { return <div className="flex items-center justify-between border-b bg-[#f6f8fa] px-3 py-2.5"><div><div className="text-sm font-semibold">{title}</div><div className="mt-0.5 text-[11px] text-muted-foreground">{description}</div></div>{count != null && <Badge variant="secondary">{count}</Badge>}</div> }
function EmptyState() { return <div className="flex min-h-48 flex-col items-center justify-center px-4 text-center"><Cable className="mb-2 size-7 text-[#8c959f]" /><div className="text-sm font-medium">还没有代理服务</div><div className="mt-1 text-xs text-muted-foreground">创建入口并绑定跨订阅节点规则。</div></div> }
function Field({ label, children }: { label: string; children: React.ReactNode }) { return <label className="block space-y-1.5"><span className="text-xs font-medium">{label}</span>{children}</label> }
function Info({ label, value }: { label: string; value: string }) { return <div className="min-w-0"><div className="text-[11px] text-muted-foreground">{label}</div><div className="mt-0.5 block truncate" title={value}>{value}</div></div> }
function Choice({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) { return <button type="button" onClick={onClick} className={cn("rounded-md border bg-white px-2.5 py-1 text-[11px] font-medium text-[#57606a] hover:bg-[#f3f4f6]", active && "border-[#54aeff] bg-[#ddf4ff] text-[#0550ae]")}>{children}</button> }
function ChipSet<T extends string>({ values, current, onChange }: { values: Array<{ value: T; label: string }>; current: T; onChange: (value: T) => void }) { return <div className="flex flex-wrap gap-1.5">{values.map((item) => <Choice key={item.value} active={current === item.value} onClick={() => onChange(item.value)}>{item.label}</Choice>)}</div> }
function MultiChipSet<T extends string>({ values, current, onChange }: { values: Array<{ value: T; label: string }>; current: T[]; onChange: (value: T[]) => void }) { return <div className="flex flex-wrap gap-1.5">{values.map((item) => { const active = current.includes(item.value); return <Choice key={item.value} active={active} onClick={() => onChange(active ? current.filter((value) => value !== item.value) : [...current, item.value])}>{item.label}</Choice> })}</div> }
function strategyLabel(strategy: ProxyGroupStrategy): string { return strategies.find((item) => item.value === strategy)?.label || strategy }
function regionLabel(region: string): string { return regions.find((item) => item.value === region)?.label || region }
function matchesRegion(name: string, region: string): boolean { const lower = name.toLowerCase(); const aliases: Record<string, string[]> = { jp: ["jp", "japan", "日本", "东京", "大阪", "tokyo", "osaka"], hk: ["hk", "香港", "hong kong"], tw: ["tw", "台湾", "台灣", "taipei"], sg: ["sg", "新加坡", "singapore"], us: ["us", "美国", "usa", "los angeles"], kr: ["kr", "韩国", "korea", "seoul"] }; return (aliases[region] || [region]).some((value) => lower.includes(value)) }
