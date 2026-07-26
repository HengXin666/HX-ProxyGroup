import { Handle, Position, type NodeProps, type Node } from "@xyflow/react"
import { Cable, GitBranch, Layers, Lock, LockOpen, Radio } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import type { ListenerRecord, ProxyGroup, ProxyGroupStrategy } from "@/lib/types"
import { cn } from "@/lib/utils"

// 每个策略的中文名称与行为说明，节点与检查器共用，保证口径一致。
export const strategyMeta: Record<ProxyGroupStrategy, { label: string; short: string; detail: string }> = {
  "url-test": {
    label: "并联 · 自动测速",
    short: "始终走最快成员",
    detail: "定期对所有成员访问测试 URL 测速，始终选择当前最快的一个成员转发流量。适合把多个订阅的同地区节点并联，追求最低延迟。",
  },
  fallback: {
    label: "串联 · 故障切换",
    short: "责任链：前面可用就不走后面",
    detail: "按成员顺序组成责任链：只要第一个成员可用就一直使用它；它失效后自动切到下一个，前面的恢复后自动切回。适合主备出口。",
  },
  "round-robin": {
    label: "并联 · 轮询",
    short: "请求依次分给每个成员",
    detail: "每个请求依次轮流分配给各成员，实现简单的负载均衡。注意同一网站的不同请求可能来自不同出口 IP。",
  },
  "consistent-hashing": {
    label: "并联 · 按站点哈希",
    short: "同一网站固定同一出口",
    detail: "按目标域名做一致性哈希，同一网站始终从同一个成员出去，兼顾负载均衡与会话稳定（如登录态）。",
  },
  "sticky-sessions": {
    label: "并联 · 会话粘滞",
    short: "同来源同目标短期固定",
    detail: "相同来源地址与目标在一段时间内固定走同一成员，适合对连接一致性敏感的场景。",
  },
  manual: {
    label: "手动选择",
    short: "在客户端里手工切换",
    detail: "不做自动调度，成员列表原样暴露，由使用者在客户端界面里手工选择出口。",
  },
}

export type ListenerNodeData = {
  listener: ListenerRecord
  groupName?: string
}

export type GroupNodeData = {
  group: ProxyGroup
  memberSummary: string
  resolvedCount: number | null
}

export type SourceNodeData = {
  group: ProxyGroup
  summary: string[]
}

export type BlueprintNode =
  | Node<ListenerNodeData, "listener">
  | Node<GroupNodeData, "group">
  | Node<SourceNodeData, "source">

const cardBase =
  "rounded-lg border-2 bg-white shadow-[0_1px_3px_rgba(31,35,40,0.12)] text-left transition-shadow"

export function ListenerNode({ data, selected }: NodeProps<Node<ListenerNodeData, "listener">>) {
  const { listener } = data
  return (
    <div className={cn(cardBase, "w-[230px] border-[#0969da]/40", selected && "border-[#0969da] shadow-[0_0_0_3px_rgba(9,105,218,0.18)]", !listener.enabled && "opacity-60")}>
      <div className="flex items-center gap-2 rounded-t-md border-b bg-[#ddf4ff] px-3 py-2">
        <Cable className="size-4 shrink-0 text-[#0550ae]" />
        <span className="min-w-0 flex-1 truncate text-xs font-semibold text-[#0550ae]" title={listener.name}>{listener.name}</span>
        {listener.auth_configured
          ? <Lock className="size-3.5 shrink-0 text-[#9a6700]" aria-label="需要账号密码" />
          : <LockOpen className="size-3.5 shrink-0 text-[#8c959f]" aria-label="无认证" />}
      </div>
      <div className="space-y-1 px-3 py-2 text-[11px] text-[#57606a]">
        <div className="font-mono">{listener.bind_address}:{listener.port}</div>
        <div className="flex items-center gap-1.5">
          <Badge variant="outline">{listener.kind.toUpperCase()}</Badge>
          {!listener.enabled && <Badge variant="secondary">已停用</Badge>}
        </div>
        <div className="truncate text-[10px] text-[#8c959f]">出口：{data.groupName ?? "未绑定"}</div>
      </div>
      <Handle type="source" position={Position.Right} className="!size-2.5 !bg-[#0969da]" />
    </div>
  )
}

export function GroupNode({ data, selected }: NodeProps<Node<GroupNodeData, "group">>) {
  const { group } = data
  const meta = strategyMeta[group.strategy]
  const serial = group.strategy === "fallback"
  return (
    <div className={cn(cardBase, "w-[240px] border-[#1a7f37]/40", selected && "border-[#1a7f37] shadow-[0_0_0_3px_rgba(26,127,55,0.18)]", !group.enabled && "opacity-60")}>
      <Handle type="target" position={Position.Left} className="!size-2.5 !bg-[#1a7f37]" />
      <div className="flex items-center gap-2 rounded-t-md border-b bg-[#dafbe1] px-3 py-2">
        {serial ? <GitBranch className="size-4 shrink-0 text-[#116329]" /> : <Layers className="size-4 shrink-0 text-[#116329]" />}
        <span className="min-w-0 flex-1 truncate text-xs font-semibold text-[#116329]" title={group.name}>{group.name}</span>
      </div>
      <div className="space-y-1 px-3 py-2 text-[11px] text-[#57606a]">
        <div className="font-medium text-foreground" title={meta.detail}>{meta.label}</div>
        <div className="text-[10px] text-[#8c959f]">{meta.short}</div>
        <div className="truncate text-[10px]" title={data.memberSummary}>{data.memberSummary}</div>
        {!group.enabled && <Badge variant="secondary">已停用</Badge>}
      </div>
      <Handle type="source" position={Position.Right} className="!size-2.5 !bg-[#1a7f37]" />
    </div>
  )
}

export function SourceNode({ data, selected }: NodeProps<Node<SourceNodeData, "source">>) {
  return (
    <div className={cn(cardBase, "w-[220px] border-[#8250df]/40", selected && "border-[#8250df] shadow-[0_0_0_3px_rgba(130,80,223,0.18)]")}>
      <Handle type="target" position={Position.Left} className="!size-2.5 !bg-[#8250df]" />
      <div className="flex items-center gap-2 rounded-t-md border-b bg-[#fbefff] px-3 py-2">
        <Radio className="size-4 shrink-0 text-[#6639ba]" />
        <span className="text-xs font-semibold text-[#6639ba]">节点来源</span>
      </div>
      <ul className="space-y-0.5 px-3 py-2 text-[10px] leading-4 text-[#57606a]">
        {data.summary.map((line, index) => <li key={index} className="truncate" title={line}>{line}</li>)}
      </ul>
    </div>
  )
}

export const blueprintNodeTypes = {
  listener: ListenerNode,
  group: GroupNode,
  source: SourceNode,
}
