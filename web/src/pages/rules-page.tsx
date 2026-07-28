import { useCallback, useEffect, useState } from "react"
import { Ban, LoaderCircle, Plus, Route, Save, Trash2 } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api } from "@/lib/api"
import type { ProxyGroup, RoutingRule, RoutingRulesConfig, RoutingRuleSet } from "@/lib/types"

interface RulesPageProps { onNotice: (message: string, tone?: "success" | "error") => void }

const ruleTypes: Array<{ value: RoutingRule["type"]; label: string }> = [
  { value: "domain_suffix", label: "域名后缀" }, { value: "domain", label: "完整域名" },
  { value: "domain_keyword", label: "域名关键词" }, { value: "ip_cidr", label: "IP CIDR" },
  { value: "geosite", label: "GeoSite" }, { value: "geoip", label: "GeoIP" },
  { value: "process_name", label: "进程名" }, { value: "network", label: "TCP / UDP" },
  { value: "dst_port", label: "目标端口" },
]

export function RulesPage({ onNotice }: RulesPageProps) {
  const [config, setConfig] = useState<RoutingRulesConfig>({ rule_sets: [] })
  const [groups, setGroups] = useState<ProxyGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [rules, groupList] = await Promise.all([api.routingRules(), api.listProxyGroups()])
      setConfig(rules)
      setGroups(groupList.items)
    } catch (error) { onNotice(error instanceof Error ? error.message : "加载路由规则失败", "error") }
    finally { setLoading(false) }
  }, [onNotice])
  useEffect(() => { void load() }, [load])

  const updateSet = (index: number, patch: Partial<RoutingRuleSet>) => {
    const rule_sets = [...config.rule_sets]
    const current = rule_sets[index]
    if (!current) return
    rule_sets[index] = { ...current, ...patch }
    setConfig({ rule_sets })
  }
  const updateRule = (setIndex: number, ruleIndex: number, patch: Partial<RoutingRule>) => {
    const set = config.rule_sets[setIndex]
    const current = set?.rules[ruleIndex]
    if (!set || !current) return
    const rules = [...set.rules]
    rules[ruleIndex] = { ...current, ...patch }
    updateSet(setIndex, { rules })
  }
  async function save() {
    setSaving(true)
    try { setConfig(await api.updateRoutingRules(config)); onNotice("路由规则已校验并应用") }
    catch (error) { onNotice(error instanceof Error ? error.message : "应用路由规则失败", "error") }
    finally { setSaving(false) }
  }
  if (loading) return <div className="flex min-h-72 items-center justify-center gap-2 text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />正在加载路由规则</div>

  return <div className="space-y-4">
    <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div><h1 className="text-xl font-semibold">路由规则</h1><p className="mt-1 text-sm text-muted-foreground">全局规则集与代理组入口策略</p></div>
      <div className="flex gap-2"><Button variant="outline" onClick={() => setConfig({ rule_sets: [...config.rule_sets, createRuleSet(config.rule_sets)] })}><Plus />新增规则集</Button><Button onClick={() => void save()} disabled={saving}>{saving ? <LoaderCircle className="animate-spin" /> : <Save />}保存并应用</Button></div>
    </div>
    {config.rule_sets.length === 0 ? <div className="flex min-h-64 flex-col items-center justify-center rounded-lg border bg-white text-center"><Route className="mb-3 size-8 text-muted-foreground" /><div className="font-medium">尚未配置路由规则</div></div> : config.rule_sets.map((set, setIndex) => <section key={`${set.id}-${setIndex}`} className="overflow-hidden rounded-lg border bg-white">
      <div className="flex flex-wrap items-center gap-3 border-b bg-muted px-4 py-3">
        <label className="flex items-center gap-2 text-xs font-medium"><input type="checkbox" checked={set.enabled} onChange={(event) => updateSet(setIndex, { enabled: event.target.checked })} />启用</label>
        <Input className="w-36" value={set.id} maxLength={40} onChange={(event) => updateSet(setIndex, { id: event.target.value })} aria-label="规则集标识" />
        <Input className="min-w-44 flex-1" value={set.name} maxLength={60} onChange={(event) => updateSet(setIndex, { name: event.target.value })} aria-label="规则集名称" />
        <label className="flex items-center gap-2 text-xs">优先级<Input className="w-24" type="number" min={0} max={10000} value={set.priority} onChange={(event) => updateSet(setIndex, { priority: Number(event.target.value) })} /></label>
        <Button variant="ghost" size="icon" title="删除规则集" aria-label={`删除 ${set.name}`} onClick={() => setConfig({ rule_sets: config.rule_sets.filter((_, index) => index !== setIndex) })}><Trash2 /></Button>
      </div>
      <div className="grid gap-5 p-4 lg:grid-cols-[minmax(0,1fr)_300px]">
        <div><div className="mb-2 flex items-center justify-between"><h2 className="text-xs font-semibold">匹配规则 <Badge variant="secondary">{set.rules.length}</Badge></h2><Button variant="outline" size="sm" onClick={() => updateSet(setIndex, { rules: [...set.rules, { type: "domain_suffix", value: "example.com" }] })}><Plus />添加</Button></div><div className="space-y-2">{set.rules.map((rule, ruleIndex) => <div key={ruleIndex} className="grid grid-cols-[132px_minmax(0,1fr)_32px] gap-2"><select className="h-8 rounded-md border bg-white px-2 text-xs" value={rule.type} onChange={(event) => updateRule(setIndex, ruleIndex, { type: event.target.value as RoutingRule["type"] })}>{ruleTypes.map((type) => <option key={type.value} value={type.value}>{type.label}</option>)}</select><Input value={rule.value} onChange={(event) => updateRule(setIndex, ruleIndex, { value: event.target.value })} /><Button variant="ghost" size="icon" title="删除规则" aria-label="删除规则" onClick={() => updateSet(setIndex, { rules: set.rules.filter((_, index) => index !== ruleIndex) })}><Trash2 /></Button></div>)}</div></div>
        <div className="space-y-4">
          <div><h2 className="mb-2 text-xs font-semibold">作用入口组</h2><div className="max-h-40 space-y-1 overflow-y-auto rounded-md border p-2">{groups.map((group) => <label key={group.id} className="flex items-center gap-2 px-1 py-1 text-xs"><input type="checkbox" checked={set.applied_group_ids.includes(group.id)} onChange={(event) => updateSet(setIndex, { applied_group_ids: event.target.checked ? [...set.applied_group_ids, group.id] : set.applied_group_ids.filter((id) => id !== group.id) })} />{group.name}</label>)}{groups.length === 0 && <span className="text-xs text-muted-foreground">暂无代理组</span>}</div><div className="mt-1 text-[11px] text-muted-foreground">未勾选时作用于全部入口</div></div>
          <div><h2 className="mb-2 text-xs font-semibold">匹配后动作</h2><select className="h-8 w-full rounded-md border bg-white px-2 text-xs" value={set.action.type} onChange={(event) => updateSet(setIndex, { action: { type: event.target.value as RoutingRuleSet["action"]["type"], proxy_group_id: event.target.value === "proxy_group" ? groups[0]?.id : undefined } })}><option value="reject">阻断（REJECT）</option><option value="direct">直连（DIRECT）</option><option value="proxy_group">路由到代理组</option></select>{set.action.type === "proxy_group" && <select className="mt-2 h-8 w-full rounded-md border bg-white px-2 text-xs" value={set.action.proxy_group_id ?? ""} onChange={(event) => updateSet(setIndex, { action: { ...set.action, proxy_group_id: event.target.value } })}>{groups.map((group) => <option key={group.id} value={group.id}>{group.name}</option>)}</select>}{set.action.type === "reject" && <div className="mt-2 flex items-center gap-1.5 text-xs text-[#a40e26]"><Ban className="size-3.5" />连接将由数据面直接拒绝</div>}</div>
        </div>
      </div>
    </section>)}
  </div>
}

function createRuleSet(existing: RoutingRuleSet[]): RoutingRuleSet {
  let suffix = existing.length + 1
  while (existing.some((set) => set.id === `rules-${suffix}`)) suffix++
  return { id: `rules-${suffix}`, name: `规则集 ${suffix}`, enabled: true, priority: suffix * 10, applied_group_ids: [], action: { type: "reject" }, rules: [{ type: "domain_suffix", value: "example.com" }] }
}
