import { useEffect, useState } from "react"
import { ArrowDown, ArrowUp, Copy, Link2, LoaderCircle, Plus, RefreshCw, Trash2, X } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api } from "@/lib/api"
import type { ListenerKind, ListenerRecord, ProxyGroup, ProxyGroupStrategy } from "@/lib/types"
import { cn } from "@/lib/utils"
import { strategyMeta } from "./nodes"

export type Notice = (message: string, tone?: "success" | "error") => void

const strategyOrder: ProxyGroupStrategy[] = [
  "url-test",
  "fallback",
  "round-robin",
  "consistent-hashing",
  "sticky-sessions",
  "manual",
]

async function copyText(value: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(value)
    return true
  } catch {
    return false
  }
}

export function ListenerInspector({ listener, groups, onChanged, onNotice, onClose }: {
  listener: ListenerRecord
  groups: ProxyGroup[]
  onChanged: () => Promise<void>
  onNotice: Notice
  onClose: () => void
}) {
  const [busy, setBusy] = useState(false)

  async function run(action: () => Promise<void>) {
    setBusy(true)
    try {
      await action()
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "操作失败", "error")
    } finally {
      setBusy(false)
    }
  }

  const groupName = groups.find((item) => item.id === listener.proxy_group_id)?.name ?? "未绑定"
  return (
    <InspectorShell title="入口（根节点）" subtitle={listener.name} onClose={onClose}>
      <dl className="space-y-2 text-xs">
        <InfoRow label="监听地址"><span className="font-mono">{listener.bind_address}:{listener.port}</span></InfoRow>
        <InfoRow label="入口协议"><Badge variant="outline">{listener.kind.toUpperCase()}</Badge></InfoRow>
        <InfoRow label="认证">
          <Badge variant={listener.auth_configured ? "warning" : "secondary"}>
            {listener.auth_configured ? "用户名密码" : "无认证"}
          </Badge>
        </InfoRow>
        <InfoRow label="出口代理组">{groupName}</InfoRow>
      </dl>
      <div className="space-y-2 border-t pt-3">
        <Button
          className="w-full"
          variant="outline"
          disabled={busy || !listener.share_path}
          onClick={() => void run(async () => {
            const ok = await copyText(api.listenerShareURL(listener.share_path ?? ""))
            onNotice(ok ? "订阅链接已复制，可直接导入 Clash / Mihomo / Shadowrocket 等客户端" : "复制失败，请手动复制", ok ? "success" : "error")
          })}
        >
          <Link2 />复制订阅链接
        </Button>
        <Button
          className="w-full"
          variant="outline"
          disabled={busy}
          onClick={() => void run(async () => {
            const ok = await copyText(`${listener.bind_address}:${listener.port}`)
            onNotice(ok ? "入口地址已复制，可直接填入系统代理" : "复制失败，请手动复制", ok ? "success" : "error")
          })}
        >
          <Copy />复制 IP:端口
        </Button>
        <Button
          className="w-full"
          variant="outline"
          disabled={busy}
          onClick={() => void run(async () => {
            await api.rotateListenerShare(listener.id)
            await onChanged()
            onNotice("订阅链接已重置，旧链接立即失效")
          })}
        >
          <RefreshCw />重置订阅链接
        </Button>
        <Button
          className="w-full"
          variant="outline"
          disabled={busy}
          onClick={() => void run(async () => {
            await api.updateListener(listener.id, {
              version: listener.version,
              name: listener.name,
              kind: listener.kind,
              bind_address: listener.bind_address,
              port: listener.port,
              proxy_group_id: listener.proxy_group_id,
              enabled: !listener.enabled,
            })
            await onChanged()
            onNotice(listener.enabled ? "入口已停用" : "入口已启用")
          })}
        >
          {listener.enabled ? "停用入口" : "启用入口"}
        </Button>
        <Button
          className="w-full"
          variant="destructive"
          disabled={busy}
          onClick={() => void run(async () => {
            await api.deleteListener(listener.id, listener.version)
            await onChanged()
            onClose()
            onNotice("入口已删除")
          })}
        >
          <Trash2 />删除入口
        </Button>
      </div>
      <p className="border-t pt-3 text-[11px] leading-4 text-muted-foreground">
        订阅链接无需登录即可访问（链接里的随机 token 即凭证），请勿公开分享；泄露后点「重置订阅链接」。
      </p>
    </InspectorShell>
  )
}

export function GroupInspector({ group, groups, onChanged, onNotice, onClose }: {
  group: ProxyGroup
  groups: ProxyGroup[]
  onChanged: () => Promise<void>
  onNotice: Notice
  onClose: () => void
}) {
  const [strategy, setStrategy] = useState<ProxyGroupStrategy>(group.strategy)
  const [groupIDs, setGroupIDs] = useState<string[]>(group.source_spec.group_ids ?? [])
  const [limit, setLimit] = useState<number>(group.source_spec.limit ?? 0)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    setStrategy(group.strategy)
    setGroupIDs(group.source_spec.group_ids ?? [])
    setLimit(group.source_spec.limit ?? 0)
  }, [group])

  const nameOf = (id: string) => groups.find((item) => item.id === id)?.name ?? id
  const dirty =
    strategy !== group.strategy ||
    limit !== (group.source_spec.limit ?? 0) ||
    JSON.stringify(groupIDs) !== JSON.stringify(group.source_spec.group_ids ?? [])

  function move(index: number, offset: number) {
    const next = [...groupIDs]
    const target = index + offset
    if (target < 0 || target >= next.length) return
    const moved = next[index]
    const displaced = next[target]
    if (moved == null || displaced == null) return
    next[index] = displaced
    next[target] = moved
    setGroupIDs(next)
  }

  async function save() {
    setBusy(true)
    try {
      await api.updateProxyGroup(group.id, {
        version: group.version,
        name: group.name,
        strategy,
        source_spec: { ...group.source_spec, group_ids: groupIDs, limit: limit || undefined },
        enabled: group.enabled,
        empty_behavior: group.empty_behavior,
        fallback_target_id: group.fallback_target_id,
      })
      await onChanged()
      onNotice("代理组已保存并重新下发")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "保存代理组失败", "error")
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    setBusy(true)
    try {
      await api.deleteProxyGroup(group.id, group.version)
      await onChanged()
      onClose()
      onNotice("代理组已删除")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "删除代理组失败", "error")
    } finally {
      setBusy(false)
    }
  }

  return (
    <InspectorShell title="代理组" subtitle={group.name} onClose={onClose}>
      <div className="space-y-1.5">
        <div className="text-xs font-medium">出站策略</div>
        <div className="space-y-1.5">
          {strategyOrder.map((value) => {
            const meta = strategyMeta[value]
            const active = strategy === value
            return (
              <button
                key={value}
                type="button"
                onClick={() => setStrategy(value)}
                className={cn(
                  "w-full rounded-md border bg-white px-2.5 py-2 text-left hover:bg-[#f6f8fa]",
                  active && "border-[#54aeff] bg-[#ddf4ff]",
                )}
              >
                <div className={cn("text-xs font-medium", active && "text-[#0550ae]")}>{meta.label}</div>
                <div className="mt-0.5 text-[10px] leading-4 text-muted-foreground">{meta.detail}</div>
              </button>
            )
          })}
        </div>
      </div>

      <div className="space-y-1.5 border-t pt-3">
        <div className="text-xs font-medium">子代理组（按顺序）</div>
        <p className="text-[10px] leading-4 text-muted-foreground">
          在画布上从本组右侧拖到其他组即可添加；串联（故障切换）时顺序即优先级。
        </p>
        {groupIDs.length === 0 ? (
          <div className="rounded-md border border-dashed px-2.5 py-2 text-[11px] text-muted-foreground">暂无子代理组</div>
        ) : (
          <ul className="space-y-1">
            {groupIDs.map((id, index) => (
              <li key={id} className="flex items-center gap-1 rounded-md border bg-white px-2 py-1.5 text-xs">
                <span className="w-4 text-center text-[10px] text-muted-foreground">{index + 1}</span>
                <span className="min-w-0 flex-1 truncate" title={nameOf(id)}>{nameOf(id)}</span>
                <button type="button" className="rounded p-0.5 hover:bg-[#f3f4f6]" onClick={() => move(index, -1)} aria-label="上移"><ArrowUp className="size-3.5" /></button>
                <button type="button" className="rounded p-0.5 hover:bg-[#f3f4f6]" onClick={() => move(index, 1)} aria-label="下移"><ArrowDown className="size-3.5" /></button>
                <button type="button" className="rounded p-0.5 text-destructive hover:bg-[#ffebe9]" onClick={() => setGroupIDs(groupIDs.filter((value) => value !== id))} aria-label="移除"><X className="size-3.5" /></button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="space-y-1.5 border-t pt-3">
        <div className="text-xs font-medium">并联 Top-K（最多节点数）</div>
        <Input type="number" min={0} max={500} value={limit} onChange={(event) => setLimit(Number(event.target.value))} />
        <p className="text-[10px] leading-4 text-muted-foreground">
          0 表示不限制。按最近延迟排序后截取前 K 个节点进入本组，配合「自动测速」即并联取最优。
        </p>
      </div>

      <div className="space-y-2 border-t pt-3">
        <Button className="w-full" onClick={() => void save()} disabled={busy || !dirty}>
          {busy ? <LoaderCircle className="animate-spin" /> : null}保存修改
        </Button>
        <Button className="w-full" variant="destructive" onClick={() => void remove()} disabled={busy}>
          <Trash2 />删除代理组
        </Button>
      </div>
    </InspectorShell>
  )
}

export function CanvasToolbox({ groups, onChanged, onNotice }: {
  groups: ProxyGroup[]
  onChanged: () => Promise<void>
  onNotice: Notice
}) {
  const [mode, setMode] = useState<"none" | "group" | "listener">("none")
  return (
    <div className="space-y-3">
      <div className="text-sm font-semibold">画布工具</div>
      <p className="text-[11px] leading-4 text-muted-foreground">
        点击节点查看与编辑；从节点右侧圆点拖线到另一个节点即可建立「入口 → 组」或「组 → 子组」关系；选中连线按 Delete 可断开组间引用。
      </p>
      <div className="grid grid-cols-2 gap-2">
        <Button variant={mode === "group" ? "default" : "outline"} onClick={() => setMode(mode === "group" ? "none" : "group")}><Plus />代理组</Button>
        <Button variant={mode === "listener" ? "default" : "outline"} onClick={() => setMode(mode === "listener" ? "none" : "listener")}><Plus />入口</Button>
      </div>
      {mode === "group" && <QuickGroupForm onDone={async () => { setMode("none"); await onChanged() }} onNotice={onNotice} />}
      {mode === "listener" && <QuickListenerForm groups={groups} onDone={async () => { setMode("none"); await onChanged() }} onNotice={onNotice} />}
    </div>
  )
}

function QuickGroupForm({ onDone, onNotice }: { onDone: () => Promise<void>; onNotice: Notice }) {
  const [name, setName] = useState("")
  const [strategy, setStrategy] = useState<ProxyGroupStrategy>("url-test")
  const [busy, setBusy] = useState(false)
  async function submit() {
    setBusy(true)
    try {
      await api.createProxyGroup({
        name: name.trim(),
        strategy,
        // 新组先以 DIRECT 兜底占位，随后在画布上连接子组或来源。
        source_spec: { node_ids: [], include_direct: true },
        empty_behavior: "direct",
      })
      await onDone()
      onNotice(`代理组「${name.trim()}」已创建，可开始连线`)
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "创建代理组失败", "error")
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="space-y-2 rounded-md border bg-[#f6f8fa] p-2.5">
      <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="组名称，如：主备出口" />
      <select
        className="h-8 w-full rounded-md border bg-white px-2 text-xs"
        value={strategy}
        onChange={(event) => setStrategy(event.target.value as ProxyGroupStrategy)}
      >
        {strategyOrder.map((value) => <option key={value} value={value}>{strategyMeta[value].label}</option>)}
      </select>
      <Button className="w-full" size="sm" disabled={busy || !name.trim()} onClick={() => void submit()}>
        {busy ? <LoaderCircle className="animate-spin" /> : <Plus />}创建
      </Button>
    </div>
  )
}

function QuickListenerForm({ groups, onDone, onNotice }: { groups: ProxyGroup[]; onDone: () => Promise<void>; onNotice: Notice }) {
  const [name, setName] = useState("")
  const [kind, setKind] = useState<ListenerKind>("mixed")
  const [bindAddress, setBindAddress] = useState("127.0.0.1")
  const [port, setPort] = useState(7890)
  const [groupID, setGroupID] = useState(groups[0]?.id ?? "")
  const [authEnabled, setAuthEnabled] = useState(false)
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [busy, setBusy] = useState(false)
  const enabledGroups = groups.filter((item) => item.enabled)
  async function submit() {
    setBusy(true)
    try {
      await api.createListener({
        name: name.trim(),
        kind,
        bind_address: bindAddress.trim(),
        port,
        proxy_group_id: groupID,
        auth: authEnabled ? { username: username.trim(), password } : undefined,
      })
      await onDone()
      onNotice(`入口「${name.trim()}」已创建并生成订阅链接`)
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "创建入口失败", "error")
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="space-y-2 rounded-md border bg-[#f6f8fa] p-2.5">
      <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="入口名称" />
      <div className="grid grid-cols-[1fr_90px] gap-2">
        <Input value={bindAddress} onChange={(event) => setBindAddress(event.target.value)} placeholder="绑定 IP" />
        <Input type="number" min={1} max={65535} value={port} onChange={(event) => setPort(Number(event.target.value))} />
      </div>
      <div className="grid grid-cols-2 gap-2">
        <select className="h-8 rounded-md border bg-white px-2 text-xs" value={kind} onChange={(event) => setKind(event.target.value as ListenerKind)}>
          <option value="mixed">Mixed</option>
          <option value="http">HTTP</option>
          <option value="socks">SOCKS5</option>
        </select>
        <select className="h-8 rounded-md border bg-white px-2 text-xs" value={groupID} onChange={(event) => setGroupID(event.target.value)}>
          {enabledGroups.length === 0 && <option value="">先创建代理组</option>}
          {enabledGroups.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
        </select>
      </div>
      <label className="flex items-center gap-2 text-[11px]">
        <input type="checkbox" className="size-3.5 accent-[#0969da]" checked={authEnabled} onChange={(event) => setAuthEnabled(event.target.checked)} />
        用户名密码认证（非环回地址必须开启）
      </label>
      {authEnabled && (
        <div className="grid grid-cols-2 gap-2">
          <Input value={username} onChange={(event) => setUsername(event.target.value)} placeholder="用户名" />
          <Input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="密码" />
        </div>
      )}
      <Button
        className="w-full"
        size="sm"
        disabled={busy || !name.trim() || !groupID || (authEnabled && (!username.trim() || !password))}
        onClick={() => void submit()}
      >
        {busy ? <LoaderCircle className="animate-spin" /> : <Plus />}创建
      </Button>
    </div>
  )
}

function InspectorShell({ title, subtitle, onClose, children }: {
  title: string
  subtitle: string
  onClose: () => void
  children: React.ReactNode
}) {
  return (
    <div className="space-y-3">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="text-[11px] text-muted-foreground">{title}</div>
          <div className="truncate text-sm font-semibold" title={subtitle}>{subtitle}</div>
        </div>
        <button type="button" className="rounded p-1 text-muted-foreground hover:bg-[#f3f4f6]" onClick={onClose} aria-label="关闭面板">
          <X className="size-4" />
        </button>
      </div>
      {children}
    </div>
  )
}

function InfoRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-2">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 truncate text-right">{children}</dd>
    </div>
  )
}
