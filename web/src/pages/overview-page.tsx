import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Activity,
  ArrowDownToLine,
  ArrowUpFromLine,
  Cable,
  CircleAlert,
  CircleCheck,
  LoaderCircle,
  RefreshCw,
  Route,
  ShieldBan,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import type { DataPlaneStatus, ListenerRecord, OverviewSample, ProxyGroup, RoutingRuleSet } from "@/lib/types"
import { cn } from "@/lib/utils"

interface OverviewPageProps {
  onNotice: (message: string, tone?: "success" | "error") => void
}

type OverviewData = {
  status: DataPlaneStatus
  groups: ProxyGroup[]
  listeners: ListenerRecord[]
  ruleSets: RoutingRuleSet[]
}

const windows = [30, 60, 120] as const

export function OverviewPage({ onNotice }: OverviewPageProps) {
  const [data, setData] = useState<OverviewData | null>(null)
  const [samples, setSamples] = useState<OverviewSample[]>([])
  const [windowSeconds, setWindowSeconds] = useState<(typeof windows)[number]>(60)
  const [loading, setLoading] = useState(true)
  const [streaming, setStreaming] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [status, groups, listeners, rules] = await Promise.all([
        api.dataPlaneStatus(),
        api.listProxyGroups(),
        api.listListeners(),
        api.routingRules(),
      ])
      setData({ status, groups: groups.items, listeners: listeners.items, ruleSets: rules.rule_sets })
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "加载总览失败", "error")
    } finally {
      setLoading(false)
    }
  }, [onNotice])

  useEffect(() => { void load() }, [load])

  useEffect(() => {
    const source = new EventSource(api.overviewStreamURL())
    source.addEventListener("sample", (event) => {
      const sample = JSON.parse((event as MessageEvent<string>).data) as OverviewSample
      setSamples((current) => [...current, sample].slice(-120))
      setStreaming(true)
    })
    source.onerror = () => setStreaming(false)
    return () => source.close()
  }, [])

  const visibleSamples = samples.slice(-windowSeconds)
  const latest = samples.at(-1)
  const routes = useMemo(() => (data?.listeners ?? []).map((listener) => ({
    listener,
    group: data?.groups.find((group) => group.id === listener.proxy_group_id),
    ruleSets: (data?.ruleSets ?? []).filter((set) => set.enabled && setAppliesToGroup(set, listener.proxy_group_id)),
  })), [data])

  return <div className="space-y-5">
    <header className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
      <div><h1 className="text-xl font-semibold">总览</h1><p className="mt-1 text-sm text-muted-foreground">流量、入口与分流状态</p></div>
      <Button variant="outline" onClick={() => void load()} disabled={loading}>{loading ? <LoaderCircle className="animate-spin" /> : <RefreshCw />}刷新</Button>
    </header>

    <StatusBand status={data?.status ?? null} streaming={streaming} loading={loading} />

    <section className="grid border-y bg-card sm:grid-cols-2 xl:grid-cols-4">
      <Metric icon={ArrowDownToLine} label="当前下载" value={formatRate(latest?.download_bytes_per_second ?? 0)} color="text-info" />
      <Metric icon={ArrowUpFromLine} label="当前上传" value={formatRate(latest?.upload_bytes_per_second ?? 0)} color="text-success" />
      <Metric icon={Activity} label="活动连接" value={String(latest?.active_connections ?? 0)} color="text-primary" />
      <Metric icon={Route} label="有效入口" value={String(routes.filter((item) => item.listener.enabled).length)} color="text-warning" />
    </section>

    <section className="border-y bg-card py-4">
      <div className="flex flex-col gap-3 px-4 sm:flex-row sm:items-center sm:justify-between">
        <div><h2 className="text-sm font-semibold">实时流量</h2><p className="mt-0.5 text-xs text-muted-foreground">{visibleSamples.length} 个秒级采样点</p></div>
        <div className="inline-flex w-fit rounded-md border bg-muted p-0.5" aria-label="图表时间窗口">
          {windows.map((seconds) => <button key={seconds} type="button" onClick={() => setWindowSeconds(seconds)} className={cn("h-7 rounded px-2.5 text-xs font-medium text-muted-foreground", windowSeconds === seconds && "bg-card text-foreground shadow-sm")}>{seconds} 秒</button>)}
        </div>
      </div>
      <TrafficChart samples={visibleSamples} />
    </section>

    <section>
      <div className="mb-3 flex items-end justify-between gap-3"><div><h2 className="text-sm font-semibold">路由拓扑</h2><p className="mt-0.5 text-xs text-muted-foreground">{routes.length} 个 Listener 入口</p></div><Badge variant="outline">{data?.ruleSets.filter((item) => item.enabled).length ?? 0} 个规则集</Badge></div>
      {loading ? <div className="flex min-h-36 items-center justify-center text-sm text-muted-foreground"><LoaderCircle className="mr-2 size-4 animate-spin" />正在加载路由</div> : routes.length === 0 ? <div className="border-y bg-card px-4 py-12 text-center text-sm text-muted-foreground">暂无 Listener 路由</div> : <div className="grid gap-2 lg:grid-cols-2">{routes.map(({ listener, group, ruleSets }) => <RouteRow key={listener.id} listener={listener} group={group} ruleSets={ruleSets} />)}</div>}
    </section>
  </div>
}

function StatusBand({ status, streaming, loading }: { status: DataPlaneStatus | null; streaming: boolean; loading: boolean }) {
  const running = status?.running === true
  const Icon = loading ? LoaderCircle : running ? CircleCheck : CircleAlert
  return <section className="flex flex-col border-y bg-card sm:flex-row sm:items-center">
    <div className={cn("flex min-w-56 items-center gap-3 px-4 py-3", running ? "bg-success-muted" : "bg-warning-muted")}><Icon className={cn("size-5", loading && "animate-spin", running ? "text-success" : "text-warning")} /><div><div className="text-sm font-semibold">{loading ? "读取状态" : running ? "Mihomo 运行中" : "Mihomo 未运行"}</div><div className="mt-0.5 text-[11px] text-muted-foreground">{status?.listener_count ?? 0} 入口 · {status?.proxy_count ?? 0} 代理</div></div></div>
    <div className="flex flex-1 items-center justify-between gap-3 px-4 py-3 text-xs"><span className="text-muted-foreground">秒级数据流</span><span className="inline-flex items-center gap-1.5 font-medium"><span className={cn("size-2 rounded-full", streaming ? "bg-success" : "bg-destructive")} />{streaming ? "已连接" : "正在重连"}</span></div>
  </section>
}

function Metric({ icon: Icon, label, value, color }: { icon: typeof Activity; label: string; value: string; color: string }) {
  return <div className="flex min-h-24 items-center gap-3 border-b px-4 py-3 last:border-b-0 sm:border-b-0 sm:border-r sm:last:border-r-0"><Icon className={cn("size-5 shrink-0", color)} /><div className="min-w-0"><div className="text-xs text-muted-foreground">{label}</div><div className="mt-1 truncate font-mono text-lg font-semibold tabular-nums">{value}</div></div></div>
}

function TrafficChart({ samples }: { samples: OverviewSample[] }) {
  const width = 960
  const height = 220
  const inset = 18
  const maximum = Math.max(1, ...samples.flatMap((sample) => [sample.upload_bytes_per_second, sample.download_bytes_per_second]))
  const upload = chartPath(samples, "upload_bytes_per_second", maximum, width, height, inset)
  const download = chartPath(samples, "download_bytes_per_second", maximum, width, height, inset)
  return <div className="mt-3 px-2 sm:px-4">
    <div className="relative h-56 w-full overflow-hidden">
      <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" className="size-full" role="img" aria-label="上传和下载速率滑动窗口图表">
        {[0.25, 0.5, 0.75, 1].map((ratio) => <line key={ratio} x1={inset} x2={width - inset} y1={height - inset - (height - inset * 2) * ratio} y2={height - inset - (height - inset * 2) * ratio} stroke="var(--border)" strokeWidth="1" vectorEffect="non-scaling-stroke" />)}
        <path d={download} fill="none" stroke="var(--info)" strokeWidth="2" vectorEffect="non-scaling-stroke" />
        <path d={upload} fill="none" stroke="var(--success)" strokeWidth="2" vectorEffect="non-scaling-stroke" />
      </svg>
      {samples.length === 0 && <div className="absolute inset-0 flex items-center justify-center text-xs text-muted-foreground">等待首个采样点</div>}
    </div>
    <div className="flex items-center justify-between border-t pt-2 text-[11px] text-muted-foreground"><div className="flex gap-4"><span className="inline-flex items-center gap-1.5"><span className="h-0.5 w-4 bg-info" />下载</span><span className="inline-flex items-center gap-1.5"><span className="h-0.5 w-4 bg-success" />上传</span></div><span>峰值 {formatRate(maximum)}</span></div>
  </div>
}

function RouteRow({ listener, group, ruleSets }: { listener: ListenerRecord; group?: ProxyGroup; ruleSets: RoutingRuleSet[] }) {
  return <article className="min-w-0 border bg-card p-3">
    <div className="flex items-start gap-2.5"><div className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted"><Cable className="size-4" /></div><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-1.5"><span className="truncate text-sm font-medium">{listener.name}</span><Badge variant={listener.enabled ? "success" : "secondary"}>{listener.enabled ? "启用" : "停用"}</Badge></div><div className="mt-1 truncate font-mono text-[11px] text-muted-foreground">{listener.bind_address}:{listener.port} · {listener.kind.toUpperCase()}</div></div></div>
    <div className="my-2 border-t" />
    <div className="grid gap-2 text-xs sm:grid-cols-2"><div><div className="text-[11px] text-muted-foreground">代理组</div><div className="mt-0.5 truncate font-medium">{group?.name ?? "代理组缺失"}</div></div><div><div className="text-[11px] text-muted-foreground">策略</div><div className="mt-0.5 truncate font-medium">{group?.strategy ?? "-"}</div></div></div>
    <div className="mt-2 flex min-h-6 flex-wrap gap-1">{ruleSets.length === 0 ? <span className="inline-flex items-center gap-1 text-[11px] text-muted-foreground"><ShieldBan className="size-3" />仅默认规则</span> : ruleSets.slice(0, 4).map((set) => <Badge key={set.id} variant="outline">{set.name} · {actionLabel(set, listener.proxy_group_id)}</Badge>)}{ruleSets.length > 4 && <Badge variant="secondary">+{ruleSets.length - 4}</Badge>}</div>
  </article>
}

function chartPath(samples: OverviewSample[], key: "upload_bytes_per_second" | "download_bytes_per_second", maximum: number, width: number, height: number, inset: number): string {
  if (samples.length === 0) return ""
  const span = Math.max(1, samples.length - 1)
  return samples.map((sample, index) => {
    const x = inset + (index / span) * (width - inset * 2)
    const y = height - inset - (sample[key] / maximum) * (height - inset * 2)
    return `${index === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`
  }).join(" ")
}

function setAppliesToGroup(set: RoutingRuleSet, groupID: string): boolean {
  if (set.routes?.length) return set.routes.some((route) => route.proxy_group_id === groupID)
  return Boolean(set.action?.type) && (!set.applied_group_ids?.length || set.applied_group_ids.includes(groupID))
}

function actionLabel(set: RoutingRuleSet, groupID: string): string {
  const action = set.routes?.find((route) => route.proxy_group_id === groupID)?.action ?? set.action
  if (action?.type === "reject") return "拒绝"
  if (action?.type === "direct") return "直连"
  return "代理"
}

function formatRate(bytes: number): string {
  if (bytes < 1024) return `${Math.round(bytes)} B/s`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB/s`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB/s`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GB/s`
}
