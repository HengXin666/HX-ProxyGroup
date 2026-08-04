import { useEffect, useState } from "react"
import { Check, Copy, Download, ExternalLink, GitFork, LoaderCircle, PackageCheck, RefreshCw, ServerCog } from "lucide-react"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import { api } from "@/lib/api"
import type { SystemInfo } from "@/lib/types"
import { cn } from "@/lib/utils"

export function AboutPage({ onNotice }: { onNotice: (message: string, tone?: "success" | "error") => void }) {
  const [info, setInfo] = useState<SystemInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [copied, setCopied] = useState(false)
  const [confirmingUpdate, setConfirmingUpdate] = useState(false)
  const [updating, setUpdating] = useState(false)

  async function load() {
    setLoading(true)
    try {
      setInfo(await api.systemInfo())
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "加载版本信息失败", "error")
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { void load() }, [])

  async function copyUpdateCommand() {
    if (!info) return
    try {
      await navigator.clipboard.writeText(info.update_command)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1800)
      onNotice("更新命令已复制")
    } catch {
      onNotice("浏览器未允许复制，请手动选择命令", "error")
    }
  }

  async function updateToLatest() {
    setUpdating(true)
    try {
      await api.triggerSystemUpdate()
      setConfirmingUpdate(false)
      onNotice("更新任务已启动，服务恢复后页面会自动刷新")
      let observedRestart = false
      for (let attempt = 0; attempt < 90; attempt += 1) {
        await new Promise((resolve) => window.setTimeout(resolve, 2000))
        const healthy = await api.health()
        if (!healthy) observedRestart = true
        if (healthy && observedRestart) {
          window.location.reload()
          return
        }
      }
      onNotice("更新状态等待超时，请查看 systemd 日志或手动刷新", "error")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "自动更新启动失败", "error")
    } finally {
      setUpdating(false)
    }
  }

  return <div className="space-y-4">
    <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">关于 HX-ProxyGroup</h1>
        <p className="mt-1 max-w-3xl text-sm leading-5 text-muted-foreground">控制面版本、Mihomo 数据面能力与发布更新信息。</p>
      </div>
      <div className="flex items-center gap-2">
        <Button variant="outline" onClick={() => void load()} disabled={loading}><RefreshCw className={loading ? "animate-spin" : ""} />刷新</Button>
        {info && <a className={cn(buttonVariants({ variant: "default" }))} href={info.repository_url} target="_blank" rel="noreferrer"><GitFork />GitHub<ExternalLink className="size-3.5" /></a>}
      </div>
    </div>

    {loading && !info ? <div className="flex min-h-48 items-center justify-center gap-2 rounded-lg border bg-card text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />正在读取系统信息</div> : info && <>
      <section className="overflow-hidden rounded-lg border bg-card">
        <div className="grid sm:grid-cols-2 lg:grid-cols-3">
          <VersionItem icon={PackageCheck} label="控制面" value={info.version} />
          <VersionItem icon={ServerCog} label="Mihomo 数据面" value={info.dataplane_version || "当前未连接"} border />
          <VersionItem icon={GitFork} label="项目仓库" value="HengXin666/HX-ProxyGroup" border />
        </div>
      </section>

      <section className="rounded-lg border bg-card">
        <div className="border-b px-4 py-3">
          <h2 className="text-sm font-semibold">更新</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">安装器解析固定 Release 版本、校验 SHA-256，并在双服务 readiness 失败时恢复上一版。</p>
        </div>
        <div className="flex min-w-0 flex-col gap-3 p-4 sm:flex-row sm:items-center">
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <code className="min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap rounded-md border bg-muted px-3 py-2 font-mono text-xs" title={info.update_command}>{info.update_command}</code>
            <Button variant="outline" size="icon" onClick={() => void copyUpdateCommand()} title="复制更新命令" aria-label="复制更新命令">{copied ? <Check className="text-success" /> : <Copy />}</Button>
          </div>
          {info.automatic_update && (
            <Button onClick={() => setConfirmingUpdate(true)} disabled={updating}>
              {updating ? <LoaderCircle className="animate-spin" /> : <Download />}
              {updating ? "更新中" : "更新至最新版"}
            </Button>
          )}
        </div>
      </section>

      <section className="rounded-lg border bg-card">
        <div className="border-b px-4 py-3">
          <h2 className="text-sm font-semibold">订阅解析兼容</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">以下为当前控制面可规范化并交给 Mihomo 校验的出站类型，最终可用性取决于页面上方显示的 Mihomo 构建。</p>
        </div>
        <div className="flex flex-wrap gap-1.5 p-4">{info.supported_protocols.map((protocol) => <Badge key={protocol} variant="outline">{protocol.toUpperCase()}</Badge>)}</div>
      </section>
    </>}
    <ConfirmDialog
      open={confirmingUpdate}
      title="更新 HX-ProxyGroup"
      description="将下载最新 GitHub Release、校验 SHA-256，并重启控制面、数据面和终端 helper；readiness 失败会自动回滚。"
      confirmLabel="开始更新"
      busy={updating}
      onCancel={() => setConfirmingUpdate(false)}
      onConfirm={() => void updateToLatest()}
    />
  </div>
}

function VersionItem({ icon: Icon, label, value, border = false }: { icon: typeof PackageCheck; label: string; value: string; border?: boolean }) {
  return <div className={cn("flex min-w-0 items-center gap-3 px-4 py-4", border && "border-t sm:border-l sm:border-t-0", border && "lg:border-t-0")}>
    <div className="flex size-9 shrink-0 items-center justify-center rounded-md border bg-muted text-muted-foreground"><Icon className="size-4" /></div>
    <div className="min-w-0"><div className="text-xs text-muted-foreground">{label}</div><div className="mt-0.5 truncate font-medium" title={value}>{value}</div></div>
  </div>
}
