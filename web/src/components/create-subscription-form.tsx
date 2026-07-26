import { useState, type FormEvent, type ReactNode } from "react"
import { FileText, Globe2, LoaderCircle, Radio, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { api } from "@/lib/api"
import type { CreateSubscriptionRequest, SourceType, Subscription } from "@/lib/types"
import { cn } from "@/lib/utils"

interface CreateSubscriptionFormProps {
  onCreated: (subscription: Subscription) => void
  onCancel: () => void
  onError: (message: string) => void
}

const sourceOptions: Array<{
  type: SourceType
  label: string
  description: string
  icon: typeof Globe2
}> = [
  { type: "remote", label: "远程 URL", description: "HTTP / HTTPS", icon: Globe2 },
  { type: "inline", label: "粘贴内容", description: "URI / Base64 / YAML", icon: Radio },
  { type: "file", label: "本地文件", description: "服务器绝对路径", icon: FileText },
]

export function CreateSubscriptionForm({ onCreated, onCancel, onError }: CreateSubscriptionFormProps) {
  const [sourceType, setSourceType] = useState<SourceType>("remote")
  const [name, setName] = useState("")
  const [sourceValue, setSourceValue] = useState("")
  const [headersText, setHeadersText] = useState("")
  const [userAgent, setUserAgent] = useState("")
  const [allowPrivate, setAllowPrivate] = useState(false)
  const [timeout, setTimeout] = useState(30)
  const [interval, setInterval] = useState(3600)
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!name.trim() || !sourceValue.trim()) {
      onError("名称和来源内容不能为空")
      return
    }
    if (!Number.isInteger(interval) || interval < 60) {
      onError("自动刷新周期不能小于 60 秒")
      return
    }

    let headers: Record<string, string> | undefined
    if (sourceType === "remote" && headersText.trim()) {
      headers = {}
      for (const rawLine of headersText.split("\n")) {
        const line = rawLine.trim()
        if (!line) continue
        const separator = line.indexOf(":")
        if (separator <= 0) {
          onError(`Header 格式错误：${line}`)
          return
        }
        headers[line.slice(0, separator).trim()] = line.slice(separator + 1).trim()
      }
    }

    const payload: CreateSubscriptionRequest = {
      name: name.trim(),
      source_type: sourceType,
      refresh_interval_seconds: interval,
      source_config:
        sourceType === "remote"
          ? {
              url: sourceValue.trim(),
              headers,
              user_agent: userAgent.trim() || undefined,
              timeout_seconds: timeout,
              allow_private: allowPrivate,
            }
          : sourceType === "file"
            ? { file_path: sourceValue.trim() }
            : { inline: sourceValue },
    }

    setSubmitting(true)
    try {
      onCreated(await api.createSubscription(payload))
    } catch (error) {
      onError(error instanceof Error ? error.message : "创建订阅失败")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="fixed inset-0 z-[70] flex items-start justify-center overflow-y-auto bg-[#1f2328]/35 p-4 sm:items-center">
      <form
        onSubmit={handleSubmit}
        className="my-4 w-full max-w-2xl overflow-hidden rounded-lg border bg-white shadow-[0_16px_48px_rgba(31,35,40,0.18)]"
      >
        <div className="flex items-start justify-between border-b px-4 py-3">
          <div>
            <h2 className="font-semibold">新建订阅</h2>
            <p className="mt-0.5 text-xs text-muted-foreground">来源配置将使用主密钥加密保存，创建后不再回显。</p>
          </div>
          <Button type="button" variant="ghost" size="icon" onClick={onCancel} aria-label="关闭">
            <X />
          </Button>
        </div>

        <div className="space-y-4 px-4 py-4">
          <div className="grid grid-cols-3 overflow-hidden rounded-md border">
            {sourceOptions.map((option, index) => {
              const Icon = option.icon
              const selected = sourceType === option.type
              return (
                <button
                  key={option.type}
                  type="button"
                  onClick={() => setSourceType(option.type)}
                  className={cn(
                    "flex min-w-0 items-start gap-2 bg-white px-3 py-2.5 text-left hover:bg-[#f6f8fa]",
                    index > 0 && "border-l",
                    selected && "bg-[#ddf4ff] text-[#0550ae] hover:bg-[#ddf4ff]",
                  )}
                >
                  <Icon className="mt-0.5 size-4 shrink-0" />
                  <span className="min-w-0">
                    <span className="block truncate text-xs font-medium">{option.label}</span>
                    <span className={cn("mt-0.5 block truncate text-[10px]", selected ? "text-[#0969da]" : "text-muted-foreground")}>{option.description}</span>
                  </span>
                </button>
              )
            })}
          </div>

          <Field label="订阅名称" required>
            <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：机场 A" maxLength={128} autoFocus />
          </Field>

          <Field label={sourceType === "remote" ? "订阅 URL" : sourceType === "file" ? "文件绝对路径" : "订阅原始内容"} required>
            {sourceType === "inline" ? (
              <Textarea
                value={sourceValue}
                onChange={(event) => setSourceValue(event.target.value)}
                placeholder="粘贴 Clash/Mihomo YAML、分享 URI 列表或 Base64 包装内容"
                className="min-h-40 font-mono text-xs"
              />
            ) : (
              <Input
                value={sourceValue}
                onChange={(event) => setSourceValue(event.target.value)}
                placeholder={sourceType === "remote" ? "https://example.com/subscription" : "/srv/subscriptions/source.yaml"}
              />
            )}
          </Field>

          {sourceType === "remote" && (
            <div className="rounded-md border bg-[#f6f8fa] p-3">
              <div className="mb-3 text-xs font-semibold">远程请求选项</div>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="User-Agent" hint="可选">
                  <Input value={userAgent} onChange={(event) => setUserAgent(event.target.value)} placeholder="HX-ProxyGroup/1" />
                </Field>
                <Field label="请求超时（秒）">
                  <Input type="number" min={1} max={300} value={timeout} onChange={(event) => setTimeout(Number(event.target.value))} />
                </Field>
                <Field label="自定义 Header" hint="每行 Key: Value">
                  <Textarea
                    value={headersText}
                    onChange={(event) => setHeadersText(event.target.value)}
                    placeholder={"Authorization: Bearer ...\nX-Client: HX"}
                    className="min-h-24 font-mono text-xs"
                  />
                </Field>
                <label className="flex cursor-pointer items-start gap-2.5 rounded-md border bg-white px-3 py-2.5">
                  <input
                    type="checkbox"
                    checked={allowPrivate}
                    onChange={(event) => setAllowPrivate(event.target.checked)}
                    className="mt-0.5 size-4 accent-[#0969da]"
                  />
                  <span>
                    <span className="block text-xs font-medium">允许访问环回和私网地址</span>
                    <span className="mt-1 block text-[11px] leading-4 text-muted-foreground">
                      仅对可信内网源启用。默认 SSRF 防护会拒绝本机、私网和链路本地地址。
                    </span>
                  </span>
                </label>
              </div>
            </div>
          )}

          <Field label="自动刷新周期（秒）" hint="最低 60 秒">
            <Input
              type="number"
              min={60}
              value={interval}
              onChange={(event) => setInterval(Number(event.target.value))}
              className="max-w-48"
            />
          </Field>

          <div className="rounded-md border border-[#f2cc60] bg-[#fff8c5] px-3 py-2 text-[11px] leading-4 text-[#7d4e00]">
            当前支持 Clash/Mihomo proxies、VLESS、VMess、Trojan、Shadowsocks、HTTP 和 SOCKS 分享格式。至少解析出一个有效节点时新快照才会生效。
          </div>
        </div>

        <div className="flex justify-end gap-2 border-t bg-[#f6f8fa] px-4 py-3">
          <Button type="button" variant="outline" onClick={onCancel} disabled={submitting}>取消</Button>
          <Button type="submit" disabled={submitting}>
            {submitting && <LoaderCircle className="animate-spin" />}
            创建订阅
          </Button>
        </div>
      </form>
    </div>
  )
}

function Field({ label, hint, required = false, children }: { label: string; hint?: string; required?: boolean; children: ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="flex items-center gap-1.5 text-xs font-medium">
        {label}
        {required && <span className="text-destructive">*</span>}
        {hint && <span className="font-normal text-muted-foreground">{hint}</span>}
      </span>
      {children}
    </label>
  )
}
