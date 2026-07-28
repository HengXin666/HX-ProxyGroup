import type { ProxyGroupStrategy } from "@/lib/types"

export const strategyMeta: Record<ProxyGroupStrategy, { label: string; short: string; detail: string }> = {
  "url-test": { label: "自动测速", short: "始终走最快成员", detail: "定期测速并选择当前最快的可用节点。" },
  fallback: { label: "故障切换", short: "按顺序使用可用成员", detail: "主节点失效后切换到下一成员，恢复后自动切回。" },
  "round-robin": { label: "轮询", short: "请求依次分配", detail: "请求依次分给每个可用成员。" },
  "consistent-hashing": { label: "按站点哈希", short: "同一网站固定出口", detail: "按目标域名进行一致性哈希。" },
  "sticky-sessions": { label: "会话粘滞", short: "同一会话短期固定", detail: "相同来源与目标在一段时间内固定出口。" },
  manual: { label: "手动选择", short: "由客户端选择", detail: "保留成员列表供客户端手动切换。" },
}
