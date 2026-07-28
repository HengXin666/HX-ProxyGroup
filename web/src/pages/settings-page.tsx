import { useCallback, useEffect, useState } from "react"
import { Gauge, Globe2, KeyRound, LoaderCircle, LogOut, Play, Plus, Save, Settings2, ShieldCheck, Trash2, UserRound } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { SiteIcon } from "@/components/site-icon"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { api } from "@/lib/api"
import type { GlobalSettings, HealthTarget } from "@/lib/types"

interface SettingsPageProps {
  onNotice: (message: string, tone?: "success" | "error") => void
  onSignedOut: () => void
  username?: string
}

type SiteResult = { success: number; tested: number }

export function SettingsPage({ onNotice, onSignedOut, username }: SettingsPageProps) {
  const [settings, setSettings] = useState<GlobalSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testingSites, setTestingSites] = useState(false)
  const [siteResults, setSiteResults] = useState<Record<string, SiteResult>>({})
  const [currentPassword, setCurrentPassword] = useState("")
  const [newPassword, setNewPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [changingPassword, setChangingPassword] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try { setSettings(await api.globalSettings()) }
    catch (error) { onNotice(error instanceof Error ? error.message : "加载全局配置失败", "error") }
    finally { setLoading(false) }
  }, [onNotice])

  useEffect(() => { void load() }, [load])

  async function save() {
    if (!settings) return
    setSaving(true)
    try { setSettings(await api.updateGlobalSettings(settings)); onNotice("全局配置已校验并应用") }
    catch (error) { onNotice(error instanceof Error ? error.message : "应用全局配置失败", "error") }
    finally { setSaving(false) }
  }

  async function testHealthTargets() {
    if (!settings) return
    const enabledTargets = settings.quality.health_targets.filter((target) => target.enabled)
    if (!enabledTargets.length) { onNotice("请先启用至少一个站点", "error"); return }
    setTestingSites(true)
    try {
      const saved = await api.updateGlobalSettings(settings)
      setSettings(saved)
      const result = await api.checkNodes([])
      const summary: Record<string, SiteResult> = Object.fromEntries(enabledTargets.map((target) => [target.id, { success: 0, tested: 0 }]))
      for (const item of result.items) for (const check of item.node.health_checks) {
        const target = summary[check.target_id]
        if (!target) continue
        target.tested++
        if (check.success) target.success++
      }
      setSiteResults(summary)
      onNotice(`站点测试完成：已通过 ${result.items.length} 个节点执行 URL Test`)
    } catch (error) { onNotice(error instanceof Error ? error.message : "站点测试失败", "error") }
    finally { setTestingSites(false) }
  }

  async function changePassword() {
    if (newPassword !== confirmPassword) { onNotice("两次输入的新密码不一致", "error"); return }
    if (newPassword.length < 10) { onNotice("新密码至少需要 10 个字符", "error"); return }
    setChangingPassword(true)
    try { await api.changePassword(currentPassword, newPassword); onNotice("管理员密码已修改，请重新登录"); onSignedOut() }
    catch (error) { onNotice(error instanceof Error ? error.message : "修改密码失败", "error") }
    finally { setChangingPassword(false) }
  }

  async function logoutAll() {
    try { await api.logoutAll(); onSignedOut() }
    catch (error) { onNotice(error instanceof Error ? error.message : "注销会话失败", "error") }
  }

  if (loading) return <div className="flex min-h-72 items-center justify-center gap-2 text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />正在加载全局配置</div>
  if (!settings) return <div className="py-20 text-center text-sm text-muted-foreground">全局配置不可用</div>

  const updateTarget = (index: number, patch: Partial<HealthTarget>) => {
    const targets = [...settings.quality.health_targets]
    if (!targets[index]) return
    targets[index] = { ...targets[index], ...patch }
    setSettings({ ...settings, quality: { ...settings.quality, health_targets: targets } })
  }

  return <div className="space-y-5">
    <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between"><div><h1 className="text-xl font-semibold">全局配置</h1><p className="mt-1 text-sm text-muted-foreground">URL 延迟、站点健康、DNS、数据面运行与管理员安全</p></div><Button onClick={() => void save()} disabled={saving}>{saving ? <LoaderCircle className="animate-spin" /> : <Save />}保存并应用</Button></div>

    <SettingsSection icon={Gauge} title="节点 URL 延迟">
      <div className="mb-3 rounded-md border bg-muted/50 px-3 py-2 text-xs leading-5 text-muted-foreground">与 Clash Verge 一致调用 Mihomo delay API。默认目标为 Cloudflare 204、超时 10 秒；该结果包含代理握手和 HTTP 往返，不是 ICMP ping。</div>
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-5">
        <Field label="主测试 URL"><Input value={settings.quality.test_url} onChange={(event) => setSettings({ ...settings, quality: { ...settings.quality, test_url: event.target.value } })} /></Field>
        <Field label="复测间隔（秒）"><Input type="number" min={60} max={86400} value={settings.quality.check_interval_seconds} onChange={(event) => setSettings({ ...settings, quality: { ...settings.quality, check_interval_seconds: Number(event.target.value) } })} /></Field>
        <Field label="单次超时（秒）"><Input type="number" min={1} max={30} value={settings.quality.timeout_seconds} onChange={(event) => setSettings({ ...settings, quality: { ...settings.quality, timeout_seconds: Number(event.target.value) } })} /></Field>
        <Field label="每轮节点数"><Input type="number" min={1} max={200} value={settings.quality.batch_size} onChange={(event) => setSettings({ ...settings, quality: { ...settings.quality, batch_size: Number(event.target.value) } })} /></Field>
        <Field label="节点并发数"><Input type="number" min={1} max={16} value={settings.quality.probe_concurrency} onChange={(event) => setSettings({ ...settings, quality: { ...settings.quality, probe_concurrency: Number(event.target.value) } })} /></Field>
      </div>
    </SettingsSection>

    <SettingsSection icon={ShieldCheck} title="站点健康度" action={<div className="flex gap-2"><Button variant="outline" size="sm" onClick={() => void testHealthTargets()} disabled={testingSites}>{testingSites ? <LoaderCircle className="animate-spin" /> : <Play />}测试已启用站点</Button><Button variant="outline" size="sm" onClick={() => setSettings({ ...settings, quality: { ...settings.quality, health_targets: [...settings.quality.health_targets, newTarget(settings.quality.health_targets)] } })}><Plus />添加站点</Button></div>}>
      <div className="divide-y rounded-md border">{settings.quality.health_targets.map((target, index) => <div key={`${target.id}-${index}`} className="grid gap-3 p-3 lg:grid-cols-[auto_44px_120px_minmax(220px,1fr)_110px_auto] lg:items-end">
        <label className="flex h-8 items-center gap-2 text-xs font-medium"><Checkbox aria-label={`启用 ${target.name}`} checked={target.enabled} onCheckedChange={(value) => updateTarget(index, { enabled: value === true })} />启用</label>
        <div className="flex size-8 items-center justify-center overflow-hidden rounded-md border bg-card"><SiteIcon url={target.url} /></div>
        <Field label="标识"><Input value={target.id} maxLength={32} onChange={(event) => updateTarget(index, { id: event.target.value })} /></Field>
        <div className="grid gap-3 sm:grid-cols-[140px_minmax(220px,1fr)]"><Field label="名称"><Input value={target.name} maxLength={40} onChange={(event) => updateTarget(index, { name: event.target.value })} /></Field><Field label="测试 URL"><Input value={target.url} onChange={(event) => updateTarget(index, { url: event.target.value })} /></Field></div>
        <div className="flex h-8 items-center"><SiteResultBadge result={siteResults[target.id]} /></div>
        <Button variant="ghost" size="icon" title="删除站点" aria-label={`删除 ${target.name}`} onClick={() => setSettings({ ...settings, quality: { ...settings.quality, health_targets: settings.quality.health_targets.filter((_, targetIndex) => targetIndex !== index) } })}><Trash2 /></Button>
      </div>)}</div>
    </SettingsSection>

    <SettingsSection icon={Globe2} title="DNS">
      <div className="mb-4 flex flex-wrap gap-5"><Toggle label="启用 Mihomo DNS" checked={settings.dns.enabled} onChange={(enabled) => setSettings({ ...settings, dns: { ...settings.dns, enabled } })} /><Toggle label="IPv6 解析" checked={settings.dns.ipv6} onChange={(ipv6) => setSettings({ ...settings, dns: { ...settings.dns, ipv6 } })} /><Field label="增强模式"><Select value={settings.dns.enhanced_mode} onValueChange={(value) => setSettings({ ...settings, dns: { ...settings.dns, enhanced_mode: value as GlobalSettings["dns"]["enhanced_mode"] } })}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="normal">Normal</SelectItem><SelectItem value="fake-ip">Fake IP</SelectItem><SelectItem value="redir-host">Redir Host</SelectItem></SelectContent></Select></Field></div>
      <div className="grid gap-4 lg:grid-cols-3"><ListField label="Bootstrap DNS" values={settings.dns.default_nameserver} onChange={(default_nameserver) => setSettings({ ...settings, dns: { ...settings.dns, default_nameserver } })} /><ListField label="主 DNS" values={settings.dns.nameserver} onChange={(nameserver) => setSettings({ ...settings, dns: { ...settings.dns, nameserver } })} /><ListField label="Fallback DNS" values={settings.dns.fallback} onChange={(fallback) => setSettings({ ...settings, dns: { ...settings.dns, fallback } })} /></div>
    </SettingsSection>

    <SettingsSection icon={Settings2} title="性能与运行"><div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4"><Toggle label="TCP 并发建连" checked={settings.performance.tcp_concurrent} onChange={(tcp_concurrent) => setSettings({ ...settings, performance: { ...settings.performance, tcp_concurrent } })} /><Toggle label="统一延迟计算" checked={settings.performance.unified_delay} onChange={(unified_delay) => setSettings({ ...settings, performance: { ...settings.performance, unified_delay } })} /><Field label="Keepalive 空闲（秒）"><Input type="number" min={0} max={600} value={settings.performance.keep_alive_idle_seconds} onChange={(event) => setSettings({ ...settings, performance: { ...settings.performance, keep_alive_idle_seconds: Number(event.target.value) } })} /></Field><Field label="Keepalive 间隔（秒）"><Input type="number" min={0} max={600} value={settings.performance.keep_alive_interval_seconds} onChange={(event) => setSettings({ ...settings, performance: { ...settings.performance, keep_alive_interval_seconds: Number(event.target.value) } })} /></Field><Field label="进程识别"><Select value={settings.performance.find_process_mode} onValueChange={(value) => setSettings({ ...settings, performance: { ...settings.performance, find_process_mode: value as GlobalSettings["performance"]["find_process_mode"] } })}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="off">关闭</SelectItem><SelectItem value="strict">严格</SelectItem><SelectItem value="always">始终</SelectItem></SelectContent></Select></Field><Field label="数据面日志"><Select value={settings.performance.log_level} onValueChange={(value) => setSettings({ ...settings, performance: { ...settings.performance, log_level: value as GlobalSettings["performance"]["log_level"] } })}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent>{["silent", "error", "warning", "info", "debug"].map((value) => <SelectItem key={value} value={value}>{value}</SelectItem>)}</SelectContent></Select></Field></div></SettingsSection>

    <SettingsSection icon={UserRound} title="管理员与会话">
      <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(320px,0.8fr)]"><div><div className="flex items-center gap-3 rounded-md border bg-muted/50 px-3 py-3"><div className="flex size-9 items-center justify-center rounded-md bg-primary text-primary-foreground"><UserRound className="size-4" /></div><div><div className="text-sm font-medium">{username || "管理员"}</div><div className="text-xs text-muted-foreground">单管理员控制面账户</div></div></div><Button variant="outline" className="mt-3" onClick={() => void logoutAll()}><LogOut />注销所有会话</Button></div><div className="space-y-3"><div className="flex items-center gap-2 text-sm font-semibold"><KeyRound className="size-4" />修改密码</div><Field label="当前密码"><Input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} /></Field><div className="grid gap-3 sm:grid-cols-2"><Field label="新密码"><Input type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} /></Field><Field label="确认新密码"><Input type="password" autoComplete="new-password" value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} /></Field></div><Button onClick={() => void changePassword()} disabled={changingPassword || !currentPassword || !newPassword || !confirmPassword}>{changingPassword ? <LoaderCircle className="animate-spin" /> : <KeyRound />}修改并重新登录</Button></div></div>
    </SettingsSection>
  </div>
}

function newTarget(targets: HealthTarget[]): HealthTarget { let suffix = targets.length + 1; while (targets.some((target) => target.id === `site-${suffix}`)) suffix++; return { id: `site-${suffix}`, name: `站点 ${suffix}`, url: "https://example.com/", enabled: false } }
function SiteResultBadge({ result }: { result?: SiteResult }) { return result ? <Badge variant={result.success === result.tested && result.tested > 0 ? "success" : "warning"}>{result.success}/{result.tested} 可访问</Badge> : <span className="text-xs text-muted-foreground">尚未测试</span> }
function SettingsSection({ icon: Icon, title, action, children }: { icon: typeof Gauge; title: string; action?: React.ReactNode; children: React.ReactNode }) { return <section className="overflow-hidden rounded-lg border bg-card"><div className="flex min-h-11 flex-wrap items-center gap-2 border-b bg-muted/60 px-4 py-2"><Icon className="size-4" /><h2 className="text-sm font-semibold">{title}</h2><div className="ml-auto">{action}</div></div><div className="p-4">{children}</div></section> }
function Field({ label, children }: { label: string; children: React.ReactNode }) { return <label className="block text-xs"><span className="mb-1 block font-medium">{label}</span>{children}</label> }
function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (checked: boolean) => void }) { return <label className="flex h-8 items-center gap-2 text-xs font-medium"><Checkbox checked={checked} onCheckedChange={(value) => onChange(value === true)} />{label}</label> }
function ListField({ label, values, onChange }: { label: string; values: string[]; onChange: (values: string[]) => void }) { return <Field label={label}><Textarea className="min-h-28 font-mono text-xs" value={values.join("\n")} onChange={(event) => onChange(event.target.value.split("\n").map((value) => value.trim()).filter(Boolean))} /></Field> }
