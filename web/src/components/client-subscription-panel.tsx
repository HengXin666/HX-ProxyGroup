import { useEffect, useState } from "react"
import { Copy, KeyRound, LoaderCircle, Radio } from "lucide-react"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import type { ClientSubscriptionInfo } from "@/lib/types"

type ShareFormat = "clash" | "v2rayn" | "sing-box" | "uri"

const formats: Array<{ value: ShareFormat; label: string }> = [
  { value: "clash", label: "Clash / Mihomo" },
  { value: "v2rayn", label: "v2rayN" },
  { value: "sing-box", label: "sing-box" },
  { value: "uri", label: "URI" },
]

export function ClientSubscriptionPanel({
  onNotice,
}: {
  onNotice: (message: string, tone?: "success" | "error") => void
}) {
  const [info, setInfo] = useState<ClientSubscriptionInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [rotating, setRotating] = useState(false)
  const [confirming, setConfirming] = useState(false)

  useEffect(() => {
    api.clientSubscription()
      .then(setInfo)
      .catch((error) => onNotice(error instanceof Error ? error.message : "统一订阅加载失败", "error"))
      .finally(() => setLoading(false))
  }, [onNotice])

  async function copy(format: ShareFormat) {
    if (!info) return
    try {
      await navigator.clipboard.writeText(api.listenerShareURL(info.share_path, format))
      onNotice(`${formats.find((item) => item.value === format)?.label ?? format} 统一订阅已复制`)
    } catch {
      onNotice("复制失败，请手动复制", "error")
    }
  }

  async function rotate() {
    setRotating(true)
    try {
      setInfo(await api.rotateClientSubscription())
      setConfirming(false)
      onNotice("统一订阅 Token 已轮换，旧地址立即失效")
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "统一订阅 Token 轮换失败", "error")
    } finally {
      setRotating(false)
    }
  }

  return (
    <section className="overflow-hidden rounded-lg border bg-card">
      <div className="flex flex-col gap-3 border-b bg-muted/60 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-start gap-2.5">
          <Radio className="mt-0.5 size-4 shrink-0 text-primary" />
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-sm font-semibold">统一客户端订阅</h2>
              {info && <Badge variant="secondary">{info.node_count} 个入口节点</Badge>}
            </div>
            <p className="mt-0.5 text-xs text-muted-foreground">
              聚合普通代理服务与住宅节点；同一地址按客户端格式导入。
            </p>
          </div>
        </div>
        <Button variant="outline" size="sm" disabled={!info || rotating} onClick={() => setConfirming(true)}>
          <KeyRound />
          轮换 Token
        </Button>
      </div>
      <div className="p-4">
        {loading ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <LoaderCircle className="size-4 animate-spin" />正在生成统一订阅
          </div>
        ) : info ? (
          <div className="space-y-3">
            <code className="block overflow-x-auto rounded-md bg-muted px-3 py-2 text-xs">
              {api.listenerShareURL(info.share_path, "clash")}
            </code>
            <div className="flex flex-wrap gap-2">
              {formats.map((format) => (
                <Button key={format.value} variant="outline" size="sm" onClick={() => void copy(format.value)}>
                  <Copy />
                  {format.label}
                </Button>
              ))}
            </div>
          </div>
        ) : (
          <p className="text-sm text-destructive">统一订阅当前不可用</p>
        )}
      </div>

      <ConfirmDialog
        open={confirming}
        title="轮换统一订阅 Token"
        description="旧订阅地址会立即失效，所有客户端都需要更新为新地址。"
        busy={rotating}
        confirmLabel="确认轮换"
        onCancel={() => setConfirming(false)}
        onConfirm={() => void rotate()}
      />
    </section>
  )
}
