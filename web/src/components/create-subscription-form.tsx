import { useRef, useState, type FormEvent, type ReactNode } from "react"
import { FileUp, Globe2, LoaderCircle, Radio, ShieldCheck } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { api } from "@/lib/api"
import type { SourceConfig, SourceType, Subscription } from "@/lib/types"

interface SubscriptionFormProps {
  subscription?: Subscription
  onSaved: (subscription: Subscription) => void
  onCancel: () => void
  onError: (message: string) => void
}

export function CreateSubscriptionForm({ subscription, onSaved, onCancel, onError }: SubscriptionFormProps) {
  const editing = Boolean(subscription)
  const [sourceType, setSourceType] = useState<SourceType>(subscription?.source_type ?? "remote")
  const [replaceSource, setReplaceSource] = useState(!editing)
  const [name, setName] = useState(subscription?.name ?? "")
  const [sourceValue, setSourceValue] = useState("")
  const [headersText, setHeadersText] = useState("")
  const [userAgent, setUserAgent] = useState("")
  const [allowPrivate, setAllowPrivate] = useState(false)
  const [timeout, setTimeout] = useState(30)
  const [interval, setInterval] = useState(subscription?.refresh_interval_seconds ?? 3600)
  const [cron, setCron] = useState(subscription?.refresh_cron ?? "")
  const [enabled, setEnabled] = useState(subscription?.enabled ?? true)
  const [submitting, setSubmitting] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  async function loadClashFile(file?: File) {
    if (!file) return
    if (file.size > 4 * 1024 * 1024) {
      onError("Clash 文件不能超过 4 MiB")
      return
    }
    setSourceType("inline")
    setReplaceSource(true)
    setSourceValue(await file.text())
    if (!name.trim()) setName(file.name.replace(/\.(ya?ml|txt)$/i, ""))
  }

  function buildSource(): SourceConfig | undefined {
    if (!replaceSource) return undefined
    if (sourceType === "inline") return { inline: sourceValue }
    if (sourceType === "file") return { file_path: sourceValue.trim() }
    const headers: Record<string, string> = {}
    for (const rawLine of headersText.split("\n")) {
      const line = rawLine.trim()
      if (!line) continue
      const separator = line.indexOf(":")
      if (separator <= 0) throw new Error(`Header 格式错误：${line}`)
      headers[line.slice(0, separator).trim()] = line.slice(separator + 1).trim()
    }
    return {
      url: sourceValue.trim(),
      headers: Object.keys(headers).length ? headers : undefined,
      user_agent: userAgent.trim() || undefined,
      timeout_seconds: timeout,
      allow_private: allowPrivate,
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!name.trim() || (replaceSource && !sourceValue.trim())) {
      onError(replaceSource ? "名称和来源内容不能为空" : "名称不能为空")
      return
    }
    if (!Number.isInteger(interval) || interval < 60) {
      onError("自动刷新周期不能小于 60 秒")
      return
    }
    setSubmitting(true)
    try {
      const source = buildSource()
      const saved = subscription
        ? await api.updateSubscription(subscription.id, {
            version: subscription.version,
            name: name.trim(),
            source_type: sourceType,
            source_config: source,
            enabled,
            refresh_interval_seconds: interval,
            refresh_cron: cron.trim() || undefined,
          })
        : await api.createSubscription({
            name: name.trim(),
            source_type: sourceType,
            source_config: source!,
            enabled,
            refresh_interval_seconds: interval,
            refresh_cron: cron.trim() || undefined,
          })
      onSaved(saved)
    } catch (error) {
      onError(error instanceof Error ? error.message : editing ? "更新订阅失败" : "创建订阅失败")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open && !submitting) onCancel() }}>
      <DialogContent>
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>{editing ? "查看与编辑订阅" : "新建订阅"}</DialogTitle>
            <DialogDescription>来源内容使用主密钥加密且不会回显；编辑其他字段时可安全保留原来源。</DialogDescription>
          </DialogHeader>
          <div className="space-y-4 px-5 py-4">
            <div className="grid gap-3 sm:grid-cols-[1fr_180px]">
              <Field label="订阅名称"><Input value={name} onChange={(event) => setName(event.target.value)} maxLength={128} autoFocus required /></Field>
              <Field label="运行状态">
                <label className="flex h-8 items-center gap-2 rounded-md border bg-white px-2.5 text-xs"><Checkbox checked={enabled} onCheckedChange={(value) => setEnabled(value === true)} />启用自动刷新</label>
              </Field>
            </div>

            {editing && (
              <label className="flex items-start gap-2 rounded-md border bg-muted px-3 py-2.5 text-xs">
                <Checkbox className="mt-0.5" checked={replaceSource} onCheckedChange={(value) => setReplaceSource(value === true)} />
                <span><span className="block font-medium">替换加密来源</span><span className="mt-0.5 block text-[11px] text-muted-foreground">关闭时只更新名称、状态和刷新计划，不触碰现有 URL、Header 或 Clash 内容。</span></span>
              </label>
            )}

            {replaceSource && (
              <div className="space-y-3 rounded-md border p-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <Tabs value={sourceType} onValueChange={(value) => setSourceType(value as SourceType)}>
                    <TabsList><TabsTrigger value="remote"><Globe2 className="mr-1 size-3.5" />远程 URL</TabsTrigger><TabsTrigger value="inline"><Radio className="mr-1 size-3.5" />粘贴内容</TabsTrigger><TabsTrigger value="file"><FileUp className="mr-1 size-3.5" />服务器文件</TabsTrigger></TabsList>
                  </Tabs>
                  <input ref={fileRef} type="file" accept=".yaml,.yml,.txt,text/yaml,application/yaml" className="hidden" onChange={(event) => void loadClashFile(event.target.files?.[0])} />
                  <Button type="button" variant="outline" size="sm" onClick={() => fileRef.current?.click()}><FileUp />导入 Clash 文件</Button>
                </div>
                <Field label={sourceType === "remote" ? "订阅 URL" : sourceType === "file" ? "服务器绝对路径" : "Clash / Mihomo YAML 或分享内容"}>
                  {sourceType === "inline" ? <Textarea className="min-h-44 font-mono text-xs" value={sourceValue} onChange={(event) => setSourceValue(event.target.value)} placeholder="粘贴含 proxies: 的 Clash YAML，或选择本地 YAML 文件" /> : <Input value={sourceValue} onChange={(event) => setSourceValue(event.target.value)} placeholder={sourceType === "remote" ? "https://example.com/subscription" : "/srv/subscriptions/source.yaml"} />}
                </Field>
                {sourceType === "remote" && <div className="grid gap-3 sm:grid-cols-2"><Field label="User-Agent"><Input value={userAgent} onChange={(event) => setUserAgent(event.target.value)} placeholder="可选" /></Field><Field label="超时（秒）"><Input type="number" min={1} max={120} value={timeout} onChange={(event) => setTimeout(Number(event.target.value))} /></Field><Field label="请求 Header（每行一个）"><Textarea className="min-h-20 font-mono text-xs" value={headersText} onChange={(event) => setHeadersText(event.target.value)} placeholder="Authorization: Bearer ..." /></Field><label className="flex items-center gap-2 self-end rounded-md border px-3 py-2 text-xs"><Checkbox checked={allowPrivate} onCheckedChange={(value) => setAllowPrivate(value === true)} />允许可信私网来源</label></div>}
              </div>
            )}

            <div className="grid gap-3 sm:grid-cols-2"><Field label="刷新周期（秒）"><Input type="number" min={60} value={interval} onChange={(event) => setInterval(Number(event.target.value))} /></Field><Field label="Cron（UTC，可选）"><Input value={cron} onChange={(event) => setCron(event.target.value)} placeholder="0 */6 * * *" /></Field></div>
            <div className="flex gap-2 rounded-md border border-primary/25 bg-accent px-3 py-2 text-[11px] leading-4 text-accent-foreground"><ShieldCheck className="mt-0.5 size-4 shrink-0" />支持 Clash/Mihomo `proxies`、常见分享 URI 和 Base64 列表。导入 YAML 后仍需刷新，解析成功才会切换活动快照。</div>
          </div>
          <DialogFooter><Button type="button" variant="outline" onClick={onCancel} disabled={submitting}>取消</Button><Button type="submit" disabled={submitting}>{submitting && <LoaderCircle className="animate-spin" />}{editing ? "保存修改" : "创建订阅"}</Button></DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block space-y-1.5"><span className="text-xs font-medium">{label}</span>{children}</label>
}
