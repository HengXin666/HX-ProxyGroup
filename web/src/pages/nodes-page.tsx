import { useCallback, useEffect, useMemo, useState } from "react"
import { Activity, Ban, CircleDot, CirclePlay, Filter, Gauge, LoaderCircle, Network, RefreshCw, Search, Settings2 } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api, type NodeQualitySettings } from "@/lib/api"
import type { NodeRecord } from "@/lib/types"
import { cn, compactId, formatDate } from "@/lib/utils"

interface NodesPageProps {
  onNotice: (message: string, tone?: "success" | "error") => void
}

const states = ["", "candidate", "healthy", "degraded", "quarantined", "disabled", "retired"] as const
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
  const [loading, setLoading] = useState(true)
  const [checking, setChecking] = useState<Record<string, boolean>>({})
  const [search, setSearch] = useState("")
  const [protocol, setProtocol] = useState("")
  const [state, setState] = useState("")
  const [settings, setSettings] = useState<NodeQualitySettings | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [savingSettings, setSavingSettings] = useState(false)

  useEffect(() => {
    api.nodeQualitySettings().then(setSettings).catch(() => setSettings(null))
  }, [])

  async function saveSettings(next: NodeQualitySettings) {
    setSavingSettings(true)
    try {
      const saved = await api.updateNodeQualitySettings(next)
      setSettings(saved)
      setSettingsOpen(false)
      onNotice("质量检测设置已保存，下一轮扫描立即生效")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "保存检测设置失败", "error")
    } finally {
      setSavingSettings(false)
    }
  }

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const result = await api.listNodes({ search: search.trim(), protocol, state })
      setItems(result.items)
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
      setItems((current) => current.map((candidate) => candidate.id === item.id ? result.node : candidate))
      onNotice(result.success ? `检测成功：${result.latency_ms ?? "—"} ms` : `检测失败：${result.error_code || "probe_failed"}`, result.success ? "success" : "error")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "节点检测失败", "error")
    } finally {
      setChecking((current) => ({ ...current, [item.id]: false }))
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
          <Button variant="outline" onClick={() => setSettingsOpen((open) => !open)}>
            <Settings2 />检测设置
          </Button>
          <Button variant="outline" onClick={() => void load()} disabled={loading}>
            <RefreshCw className={loading ? "animate-spin" : ""} />刷新
          </Button>
        </div>
      </div>

      {settingsOpen && (
        <QualitySettingsForm
          initial={settings}
          saving={savingSettings}
          onCancel={() => setSettingsOpen(false)}
          onSave={(next) => void saveSettings(next)}
        />
      )}

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
        </div>

        {loading ? <Loading /> : items.length === 0 ? <Empty /> : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[1120px] border-collapse text-left">
              <thead className="bg-[#f6f8fa] text-xs text-muted-foreground"><tr>
                <Th>节点</Th><Th>协议</Th><Th>状态</Th><Th>延迟</Th><Th>失败次数</Th><Th>最近检测</Th><Th>来源</Th><Th>指纹</Th><Th align="right">操作</Th>
              </tr></thead>
              <tbody className="divide-y">{items.map((item) => <NodeRow key={item.id} item={item} checking={Boolean(checking[item.id])} onCheck={() => void checkNode(item)} onToggle={() => void toggleNode(item)} />)}</tbody>
            </table>
          </div>
        )}
      </section>

      <div className="rounded-md border border-[#b6d8ff] bg-[#ddf4ff] px-3 py-2 text-xs leading-5 text-[#0550ae]">
        自动检测每分钟扫描一次，到期节点复测间隔
        {settings ? `为 ${formatInterval(settings.check_interval_seconds)}（可在「检测设置」中调整）` : "默认 10 分钟"}
        ；成功延迟超过 1500 ms 标记为降级，连续失败三次进入隔离，后续成功会自动恢复。
      </div>
    </div>
  )
}

function formatInterval(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`
  return `${seconds} 秒`
}

const defaultSettings: NodeQualitySettings = {
  check_interval_seconds: 600,
  timeout_seconds: 8,
  batch_size: 20,
  test_url: "https://www.gstatic.com/generate_204",
}

function QualitySettingsForm({ initial, saving, onCancel, onSave }: {
  initial: NodeQualitySettings | null
  saving: boolean
  onCancel: () => void
  onSave: (next: NodeQualitySettings) => void
}) {
  const [form, setForm] = useState<NodeQualitySettings>(initial ?? defaultSettings)
  useEffect(() => {
    if (initial) setForm(initial)
  }, [initial])
  return (
    <section className="rounded-lg border bg-white">
      <div className="border-b bg-[#f6f8fa] px-4 py-2.5 text-sm font-medium">质量检测设置</div>
      <div className="grid gap-3 px-4 py-3 sm:grid-cols-2 lg:grid-cols-4">
        <SettingsField label="复测间隔（秒）" helper="节点上次检测超过该时长后自动复测，60–86400">
          <Input
            type="number"
            min={60}
            max={86400}
            value={form.check_interval_seconds}
            onChange={(event) => setForm({ ...form, check_interval_seconds: Number(event.target.value) })}
          />
        </SettingsField>
        <SettingsField label="单次超时（秒）" helper="经代理访问测试 URL 的最长等待时间，1–30">
          <Input
            type="number"
            min={1}
            max={30}
            value={form.timeout_seconds}
            onChange={(event) => setForm({ ...form, timeout_seconds: Number(event.target.value) })}
          />
        </SettingsField>
        <SettingsField label="每轮批量" helper="一次调度最多复测的节点数，1–200">
          <Input
            type="number"
            min={1}
            max={200}
            value={form.batch_size}
            onChange={(event) => setForm({ ...form, batch_size: Number(event.target.value) })}
          />
        </SettingsField>
        <SettingsField label="测试 URL" helper="建议返回 204 的地址，如 gstatic / cloudflare">
          <Input
            value={form.test_url}
            onChange={(event) => setForm({ ...form, test_url: event.target.value })}
            placeholder="https://www.gstatic.com/generate_204"
          />
        </SettingsField>
      </div>
      <div className="flex items-center justify-end gap-2 border-t px-4 py-2.5">
        <Button variant="ghost" onClick={onCancel} disabled={saving}>取消</Button>
        <Button onClick={() => onSave(form)} disabled={saving}>
          {saving && <LoaderCircle className="animate-spin" />}保存设置
        </Button>
      </div>
    </section>
  )
}

function SettingsField({ label, helper, children }: { label: string; helper: string; children: React.ReactNode }) {
  return (
    <label className="block text-xs">
      <span className="font-medium text-foreground">{label}</span>
      <div className="mt-1">{children}</div>
      <span className="mt-1 block text-[11px] leading-4 text-muted-foreground">{helper}</span>
    </label>
  )
}

function NodeRow({ item, checking, onCheck, onToggle }: { item: NodeRecord; checking: boolean; onCheck: () => void; onToggle: () => void }) {
  const state = stateMeta[item.lifecycle_state]
  const unavailable = item.lifecycle_state === "disabled" || item.lifecycle_state === "retired"
  const disabled = item.lifecycle_state === "disabled"
  const retired = item.lifecycle_state === "retired"
  return <tr className="hover:bg-[#f6f8fa]">
    <Td><div className="flex min-w-0 items-center gap-2.5"><div className="flex size-7 shrink-0 items-center justify-center rounded-md border bg-white text-[#57606a]"><CircleDot className="size-3.5" /></div><div className="min-w-0"><div className="max-w-[300px] truncate font-medium text-foreground" title={item.display_name}>{item.display_name}</div><div className="mt-0.5 font-mono text-[11px] text-muted-foreground">{compactId(item.id)}</div></div></div></Td>
    <Td><Badge variant="outline">{item.protocol.toUpperCase()}</Badge></Td>
    <Td><Badge variant={state.variant}>{state.label}</Badge></Td>
    <Td>{item.last_latency_ms == null ? "—" : <span className="inline-flex items-center gap-1 tabular-nums"><Gauge className="size-3.5" />{item.last_latency_ms} ms</span>}</Td>
    <Td><span className={cn("tabular-nums", item.consecutive_probe_failures > 0 && "text-[#a40e26]")}>{item.consecutive_probe_failures}</span></Td>
    <Td>{formatDate(item.last_checked_at)}</Td>
    <Td>{item.source_count}</Td>
    <Td><span className="font-mono text-[11px]" title={item.fingerprint}>{compactId(item.fingerprint)}</span></Td>
    <Td align="right"><div className="inline-flex items-center gap-1.5"><Button variant="outline" size="sm" onClick={onCheck} disabled={checking || unavailable}>{checking ? <LoaderCircle className="animate-spin" /> : <Activity />}复测</Button><Button variant="outline" size="sm" onClick={onToggle} disabled={retired}>{disabled ? <CirclePlay /> : <Ban />}{disabled ? "启用" : "禁用"}</Button></div></Td>
  </tr>
}

function Loading() { return <div className="flex min-h-48 items-center justify-center gap-2 text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />正在加载节点</div> }
function Empty() { return <div className="flex min-h-56 flex-col items-center justify-center px-6 text-center"><Network className="mb-3 size-8 text-[#8c959f]" /><div className="font-medium">没有匹配的节点</div><p className="mt-1 max-w-lg text-xs leading-5 text-muted-foreground">刷新一条可解析的订阅后，节点会进入库存并自动开始质量检测。</p></div> }
function Metric({ label, value, helper, border = false }: { label: string; value: number; helper: string; border?: boolean }) { return <div className={cn("px-4 py-3", border && "border-t sm:border-l sm:border-t-0")}><div className="text-xs text-muted-foreground">{label}</div><div className="mt-0.5 text-xl font-semibold tabular-nums">{value}</div><div className="mt-0.5 text-[11px] text-muted-foreground">{helper}</div></div> }
function FilterChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) { return <button type="button" onClick={onClick} className={cn("shrink-0 rounded-full border bg-white px-2.5 py-1 text-[11px] font-medium text-[#57606a] hover:bg-[#f3f4f6]", active && "border-[#54aeff] bg-[#ddf4ff] text-[#0550ae]")}>{children}</button> }
function Th({ children, align = "left" }: { children: React.ReactNode; align?: "left" | "right" }) { return <th className={cn("whitespace-nowrap border-b px-3 py-2 font-medium", align === "right" && "text-right")}>{children}</th> }
function Td({ children, align = "left" }: { children: React.ReactNode; align?: "left" | "right" }) { return <td className={cn("whitespace-nowrap px-3 py-2.5 text-xs text-[#57606a]", align === "right" && "text-right")}>{children}</td> }
