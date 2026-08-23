import { useEffect, useRef, useState } from "react"
import { Container, LoaderCircle, RefreshCw } from "lucide-react"

import { api, type DockerContainer } from "@/lib/api"
import { cn } from "@/lib/utils"

const POLL_INTERVAL_MS = 5000

// DockerPage is the "Docker" sub-tab of the terminal page: a read-only list of
// containers (name, image, state, ports) merged with live `docker stats` where
// available. No start/stop/restart/remove operations are exposed.
export function DockerPage({ onNotice }: { onNotice: (message: string, tone?: "success" | "error") => void }) {
  const [containers, setContainers] = useState<DockerContainer[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const aliveRef = useRef(true)

  useEffect(() => {
    aliveRef.current = true
    let timer: number | null = null

    const load = async () => {
      try {
        const result = await api.dockerContainers()
        if (!aliveRef.current) return
        setContainers(result.containers)
        setError(null)
      } catch (cause) {
        if (!aliveRef.current) return
        setError(cause instanceof Error ? cause.message : "容器列表获取失败")
      } finally {
        if (aliveRef.current) setLoading(false)
      }
    }

    void load()
    timer = window.setInterval(() => void load(), POLL_INTERVAL_MS)
    return () => {
      aliveRef.current = false
      if (timer !== null) window.clearInterval(timer)
    }
  }, [onNotice])

  if (loading && containers.length === 0) {
    return (
      <div className="flex h-full min-h-40 items-center justify-center gap-2 text-xs text-muted-foreground">
        <LoaderCircle className="size-4 animate-spin" />正在读取容器列表…
      </div>
    )
  }

  const running = containers.filter((container) => container.state === "running").length

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-1.5 text-sm font-semibold">
          <Container className="size-4 text-muted-foreground" />Docker 容器
          <span className="rounded-full border bg-muted/60 px-2 py-0.5 text-[11px] text-muted-foreground">
            {running} 运行 / {containers.length} 总计
          </span>
        </div>
        <div className="flex items-center gap-2">
          {error && <span className="text-xs text-destructive">{error}</span>}
          <button
            type="button"
            onClick={() => {
              setLoading(true)
              void api.dockerContainers()
                .then((result) => setContainers(result.containers))
                .catch((cause) => setError(cause instanceof Error ? cause.message : "刷新失败"))
                .finally(() => setLoading(false))
            }}
            className="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs hover:bg-muted"
          >
            <RefreshCw className={cn("size-3.5", loading && "animate-spin")} />刷新
          </button>
        </div>
      </div>

      {containers.length === 0 ? (
        <div className="rounded-md border bg-card px-4 py-8 text-center text-xs text-muted-foreground">没有可显示的容器</div>
      ) : (
        <div className="overflow-x-auto rounded-md border bg-card">
          <table className="w-full min-w-[720px] text-left text-xs">
            <thead>
              <tr className="border-b bg-muted/60 text-muted-foreground">
                <th className="px-3 py-2 font-medium">名称</th>
                <th className="px-3 py-2 font-medium">镜像</th>
                <th className="px-3 py-2 font-medium">状态</th>
                <th className="px-3 py-2 font-medium">端口</th>
                <th className="px-3 py-2 font-medium">CPU</th>
                <th className="px-3 py-2 font-medium">内存</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {containers.map((container) => (
                <tr key={container.id} className="hover:bg-muted/30">
                  <td className="max-w-[180px] truncate px-3 py-2 font-medium" title={container.name}>{container.name}</td>
                  <td className="max-w-[200px] truncate px-3 py-2 text-muted-foreground" title={container.image}>{container.image}</td>
                  <td className="px-3 py-2">
                    <span
                      className={cn(
                        "inline-flex rounded-full border px-2 py-0.5 text-[10px]",
                        container.state === "running"
                          ? "border-success-border bg-success-muted text-success-foreground"
                          : "border-border bg-muted/60 text-muted-foreground",
                      )}
                    >
                      {container.state}
                    </span>
                  </td>
                  <td className="max-w-[200px] truncate px-3 py-2 font-mono text-[11px] text-muted-foreground" title={container.ports}>{container.ports || "—"}</td>
                  <td className="px-3 py-2 font-mono tabular-nums">{container.cpu_perc ?? "—"}</td>
                  <td className="px-3 py-2">
                    {container.mem_usage ? (
                      <span className="font-mono tabular-nums">{container.mem_usage}{container.mem_perc ? <span className="text-muted-foreground"> ({container.mem_perc})</span> : null}</span>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <p className="text-[11px] leading-5 text-muted-foreground">
        当前为只读视图：仅展示容器、端口与运行占用，不提供启动/停止/删除操作。数据每 5 秒自动刷新。
      </p>
    </div>
  )
}
