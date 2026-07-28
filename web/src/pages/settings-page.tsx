import { useCallback, useEffect, useState } from "react"
import { Gauge, Globe2, LoaderCircle, Plus, Save, Settings2, ShieldCheck, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { api } from "@/lib/api"
import type { GlobalSettings, HealthTarget } from "@/lib/types"

interface SettingsPageProps {
  onNotice: (message: string, tone?: "success" | "error") => void
}

export function SettingsPage({ onNotice }: SettingsPageProps) {
  const [settings, setSettings] = useState<GlobalSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setSettings(await api.globalSettings())
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "加载全局配置失败", "error")
    } finally {
      setLoading(false)
    }
  }, [onNotice])

  useEffect(() => { void load() }, [load])

  async function save() {
    if (!settings) return
    setSaving(true)
    try {
      setSettings(await api.updateGlobalSettings(settings))
      onNotice("全局配置已校验并应用")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "应用全局配置失败", "error")
    } finally {
      setSaving(false)
    }
  }

  if (loading) return <div className="flex min-h-72 items-center justify-center gap-2 text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />正在加载全局配置</div>
  if (!settings) return <div className="py-20 text-center text-sm text-muted-foreground">全局配置不可用</div>

  const updateTarget = (index: number, patch: Partial<HealthTarget>) => {
    const targets = [...settings.quality.health_targets]
    const current = targets[index]
    if (!current) return
    targets[index] = { ...current, ...patch }
    setSettings({ ...settings, quality: { ...settings.quality, health_targets: targets } })
  }

  return <div className="space-y-5">
    <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div><h1 className="text-xl font-semibold">全局配置</h1><p className="mt-1 text-sm text-muted-foreground">质量检测、DNS 与 Mihomo 运行参数</p></div>
      <Button onClick={() => void save()} disabled={saving}>{saving ? <LoaderCircle className="animate-spin" /> : <Save />}保存并应用</Button>
    </div>

    <SettingsSection icon={Gauge} title="节点测速">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-5">
        <Field label="主测试 URL"><Input value={settings.quality.test_url} onChange={(event) => setSettings({ ...settings, quality: { ...settings.quality, test_url: event.target.value } })} /></Field>
        <Field label="复测间隔（秒）"><Input type="number" min={60} max={86400} value={settings.quality.check_interval_seconds} onChange={(event) => setSettings({ ...settings, quality: { ...settings.quality, check_interval_seconds: Number(event.target.value) } })} /></Field>
        <Field label="单次超时（秒）"><Input type="number" min={1} max={30} value={settings.quality.timeout_seconds} onChange={(event) => setSettings({ ...settings, quality: { ...settings.quality, timeout_seconds: Number(event.target.value) } })} /></Field>
        <Field label="每轮节点数"><Input type="number" min={1} max={200} value={settings.quality.batch_size} onChange={(event) => setSettings({ ...settings, quality: { ...settings.quality, batch_size: Number(event.target.value) } })} /></Field>
        <Field label="节点并发数"><Input type="number" min={1} max={16} value={settings.quality.probe_concurrency} onChange={(event) => setSettings({ ...settings, quality: { ...settings.quality, probe_concurrency: Number(event.target.value) } })} /></Field>
      </div>
    </SettingsSection>

    <SettingsSection icon={ShieldCheck} title="站点健康度" action={<Button variant="outline" size="sm" onClick={() => setSettings({ ...settings, quality: { ...settings.quality, health_targets: [...settings.quality.health_targets, newTarget(settings.quality.health_targets)] } })}><Plus />添加站点</Button>}>
      <div className="divide-y rounded-md border">
        {settings.quality.health_targets.map((target, index) => <div key={`${target.id}-${index}`} className="grid gap-3 p-3 sm:grid-cols-[auto_120px_minmax(220px,1fr)_auto] sm:items-end">
          <label className="flex h-8 items-center gap-2 text-xs font-medium"><input type="checkbox" checked={target.enabled} onChange={(event) => updateTarget(index, { enabled: event.target.checked })} />启用</label>
          <Field label="标识"><Input value={target.id} maxLength={32} onChange={(event) => updateTarget(index, { id: event.target.value })} /></Field>
          <div className="grid gap-3 sm:grid-cols-[140px_minmax(220px,1fr)]"><Field label="名称"><Input value={target.name} maxLength={40} onChange={(event) => updateTarget(index, { name: event.target.value })} /></Field><Field label="测试 URL"><Input value={target.url} onChange={(event) => updateTarget(index, { url: event.target.value })} /></Field></div>
          <Button variant="ghost" size="icon" title="删除站点" aria-label={`删除 ${target.name}`} onClick={() => setSettings({ ...settings, quality: { ...settings.quality, health_targets: settings.quality.health_targets.filter((_, targetIndex) => targetIndex !== index) } })}><Trash2 /></Button>
        </div>)}
      </div>
    </SettingsSection>

    <SettingsSection icon={Globe2} title="DNS">
      <div className="mb-4 flex flex-wrap gap-5">
        <Toggle label="启用 Mihomo DNS" checked={settings.dns.enabled} onChange={(enabled) => setSettings({ ...settings, dns: { ...settings.dns, enabled } })} />
        <Toggle label="IPv6 解析" checked={settings.dns.ipv6} onChange={(ipv6) => setSettings({ ...settings, dns: { ...settings.dns, ipv6 } })} />
        <Field label="增强模式"><select className="h-8 rounded-md border bg-white px-2.5 text-xs" value={settings.dns.enhanced_mode} onChange={(event) => setSettings({ ...settings, dns: { ...settings.dns, enhanced_mode: event.target.value as GlobalSettings["dns"]["enhanced_mode"] } })}><option value="normal">Normal</option><option value="fake-ip">Fake IP</option><option value="redir-host">Redir Host</option></select></Field>
      </div>
      <div className="grid gap-4 lg:grid-cols-3">
        <ListField label="Bootstrap DNS" values={settings.dns.default_nameserver} onChange={(default_nameserver) => setSettings({ ...settings, dns: { ...settings.dns, default_nameserver } })} />
        <ListField label="主 DNS" values={settings.dns.nameserver} onChange={(nameserver) => setSettings({ ...settings, dns: { ...settings.dns, nameserver } })} />
        <ListField label="Fallback DNS" values={settings.dns.fallback} onChange={(fallback) => setSettings({ ...settings, dns: { ...settings.dns, fallback } })} />
      </div>
    </SettingsSection>

    <SettingsSection icon={Settings2} title="性能与运行">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Toggle label="TCP 并发建连" checked={settings.performance.tcp_concurrent} onChange={(tcp_concurrent) => setSettings({ ...settings, performance: { ...settings.performance, tcp_concurrent } })} />
        <Toggle label="统一延迟计算" checked={settings.performance.unified_delay} onChange={(unified_delay) => setSettings({ ...settings, performance: { ...settings.performance, unified_delay } })} />
        <Field label="Keepalive 空闲（秒）"><Input type="number" min={0} max={600} value={settings.performance.keep_alive_idle_seconds} onChange={(event) => setSettings({ ...settings, performance: { ...settings.performance, keep_alive_idle_seconds: Number(event.target.value) } })} /></Field>
        <Field label="Keepalive 间隔（秒）"><Input type="number" min={0} max={600} value={settings.performance.keep_alive_interval_seconds} onChange={(event) => setSettings({ ...settings, performance: { ...settings.performance, keep_alive_interval_seconds: Number(event.target.value) } })} /></Field>
        <Field label="进程识别"><select className="h-8 w-full rounded-md border bg-white px-2.5 text-xs" value={settings.performance.find_process_mode} onChange={(event) => setSettings({ ...settings, performance: { ...settings.performance, find_process_mode: event.target.value as GlobalSettings["performance"]["find_process_mode"] } })}><option value="off">关闭</option><option value="strict">严格</option><option value="always">始终</option></select></Field>
        <Field label="数据面日志"><select className="h-8 w-full rounded-md border bg-white px-2.5 text-xs" value={settings.performance.log_level} onChange={(event) => setSettings({ ...settings, performance: { ...settings.performance, log_level: event.target.value as GlobalSettings["performance"]["log_level"] } })}><option value="silent">Silent</option><option value="error">Error</option><option value="warning">Warning</option><option value="info">Info</option><option value="debug">Debug</option></select></Field>
      </div>
    </SettingsSection>
  </div>
}

function newTarget(targets: HealthTarget[]): HealthTarget {
  let suffix = targets.length + 1
  while (targets.some((target) => target.id === `site-${suffix}`)) suffix++
  return { id: `site-${suffix}`, name: `站点 ${suffix}`, url: "https://example.com/", enabled: false }
}

function SettingsSection({ icon: Icon, title, action, children }: { icon: typeof Gauge; title: string; action?: React.ReactNode; children: React.ReactNode }) {
  return <section className="overflow-hidden rounded-lg border bg-white"><div className="flex h-11 items-center gap-2 border-b bg-muted px-4"><Icon className="size-4" /><h2 className="text-sm font-semibold">{title}</h2><div className="ml-auto">{action}</div></div><div className="p-4">{children}</div></section>
}

function Field({ label, children }: { label: string; children: React.ReactNode }) { return <label className="block text-xs"><span className="mb-1 block font-medium">{label}</span>{children}</label> }
function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (checked: boolean) => void }) { return <label className="flex h-8 items-center gap-2 text-xs font-medium"><input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} />{label}</label> }
function ListField({ label, values, onChange }: { label: string; values: string[]; onChange: (values: string[]) => void }) { return <Field label={label}><Textarea className="min-h-28 font-mono text-xs" value={values.join("\n")} onChange={(event) => onChange(event.target.value.split("\n").map((value) => value.trim()).filter(Boolean))} /></Field> }
