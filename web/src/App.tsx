import { useCallback, useEffect, useState } from "react"
import {
  Archive,
  BellRing,
  CheckCircle2,
  CircleX,
  LogOut,
  Network,
  Radio,
  Route,
  TerminalSquare,
  X,
} from "lucide-react"

import { api, setCsrfToken, setUnauthenticatedHandler } from "@/lib/api"
import { cn } from "@/lib/utils"
import { AlertsPage } from "@/pages/alerts-page"
import { ArtifactsPage } from "@/pages/artifacts-page"
import { AuthPage } from "@/pages/auth-page"
import { NodesPage } from "@/pages/nodes-page"
import { RoutingPage } from "@/pages/routing-page"
import { SubscriptionsPage } from "@/pages/subscriptions-page"
import { TerminalPage } from "@/pages/terminal-page"

type Page = "subscriptions" | "nodes" | "routing" | "alerts" | "artifacts" | "terminal"
type Notice = { id: number; message: string; tone: "success" | "error" }

const pages: Array<{
  id: Page
  label: string
  description: string
  icon: typeof Radio
}> = [
  { id: "subscriptions", label: "订阅", description: "来源与刷新", icon: Radio },
  { id: "nodes", label: "节点", description: "解析与生命周期", icon: Network },
  { id: "routing", label: "代理服务", description: "代理组与 Listener", icon: Route },
  { id: "alerts", label: "告警", description: "状态与邮件通知", icon: BellRing },
  { id: "artifacts", label: "备份", description: "Backup 与 Export", icon: Archive },
  { id: "terminal", label: "终端", description: "v2 · 服务器 Shell", icon: TerminalSquare },
]

function pageFromHash(): Page {
  const value = window.location.hash.replace(/^#\/?/, "")
  return pages.some((item) => item.id === value) ? (value as Page) : "subscriptions"
}

type AuthGate =
  | { phase: "checking" }
  | { phase: "required"; configured: boolean }
  | { phase: "ready"; username?: string }

export default function App() {
  const [page, setPage] = useState<Page>(pageFromHash)
  const [healthy, setHealthy] = useState<boolean | null>(null)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [authGate, setAuthGate] = useState<AuthGate>({ phase: "checking" })

  const refreshAuth = useCallback(async () => {
    try {
      const status = await api.authStatus()
      if (!status.configured) {
        setAuthGate({ phase: "required", configured: false })
        return
      }
      if (!status.authenticated) {
        setCsrfToken("")
        setAuthGate({ phase: "required", configured: true })
        return
      }
      if (status.csrf_token) setCsrfToken(status.csrf_token)
      setAuthGate({ phase: "ready", username: status.username })
    } catch {
      // 后端不可达或未启用认证模块时不阻塞页面；健康横幅会提示离线。
      setAuthGate({ phase: "ready" })
    }
  }, [])

  useEffect(() => {
    setUnauthenticatedHandler(() => {
      setCsrfToken("")
      setAuthGate({ phase: "required", configured: true })
    })
    void refreshAuth()
    return () => setUnauthenticatedHandler(null)
  }, [refreshAuth])

  useEffect(() => {
    function onHashChange() {
      setPage(pageFromHash())
    }
    window.addEventListener("hashchange", onHashChange)
    return () => window.removeEventListener("hashchange", onHashChange)
  }, [])

  useEffect(() => {
    let cancelled = false
    async function check() {
      const ready = await api.health()
      if (!cancelled) setHealthy(ready)
    }
    void check()
    const timer = window.setInterval(() => void check(), 15_000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [])

  const navigate = useCallback((next: Page) => {
    window.location.hash = `/${next}`
    setPage(next)
  }, [])

  const showNotice = useCallback((message: string, tone: "success" | "error" = "success") => {
    const next = { id: Date.now(), message, tone }
    setNotice(next)
    window.setTimeout(() => {
      setNotice((current) => (current?.id === next.id ? null : current))
    }, 4_500)
  }, [])

  const handleLogout = useCallback(async () => {
    try {
      await api.logout()
    } finally {
      setCsrfToken("")
      setAuthGate({ phase: "required", configured: true })
    }
  }, [])

  if (authGate.phase === "checking") {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background text-sm text-muted-foreground">
        正在检查登录状态…
      </div>
    )
  }

  if (authGate.phase === "required") {
    return <AuthPage configured={authGate.configured} onAuthenticated={() => void refreshAuth()} />
  }

  return (
    <div className="min-h-screen bg-background lg:grid lg:grid-cols-[228px_minmax(0,1fr)]">
      <aside className="hidden min-h-screen border-r bg-white lg:sticky lg:top-0 lg:flex lg:h-screen lg:flex-col">
        <div className="flex h-14 items-center gap-2.5 border-b px-4">
          <div className="flex size-7 items-center justify-center rounded-md bg-[#24292f] text-white">
            <Network className="size-4" />
          </div>
          <div className="min-w-0">
            <div className="truncate text-sm font-semibold">HX-ProxyGroup</div>
            <div className="text-[11px] text-muted-foreground">Control Plane</div>
          </div>
        </div>

        <nav className="space-y-1 p-2">
          {pages.map((item) => (
            <SidebarItem
              key={item.id}
              item={item}
              active={item.id === page}
              onClick={() => navigate(item.id)}
            />
          ))}
        </nav>

        <div className="mt-auto border-t p-3">
          <div className="rounded-md border bg-[#f6f8fa] px-3 py-2.5">
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs font-medium">后端状态</span>
              <StatusDot healthy={healthy} />
            </div>
            <div className="mt-1 text-[11px] leading-4 text-muted-foreground">
              Mihomo 数据面、节点延迟检测与管理员认证已接入；流量统计与告警仍未完成。
            </div>
          </div>
          {authGate.username && (
            <button
              type="button"
              onClick={() => void handleLogout()}
              className="mt-2 flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-xs text-[#57606a] transition-colors hover:bg-[#f3f4f6] hover:text-foreground"
            >
              <LogOut className="size-3.5 shrink-0" />
              <span className="min-w-0 flex-1 truncate">退出登录（{authGate.username}）</span>
            </button>
          )}
        </div>
      </aside>

      <div className="min-w-0">
        <header className="sticky top-0 z-40 border-b bg-white lg:hidden">
          <div className="flex h-13 items-center gap-2 px-3">
            <div className="flex size-7 items-center justify-center rounded-md bg-[#24292f] text-white">
              <Network className="size-4" />
            </div>
            <span className="font-semibold">HX-ProxyGroup</span>
            <div className="ml-auto">
              <StatusDot healthy={healthy} />
            </div>
          </div>
          <nav className="flex gap-1 overflow-x-auto border-t px-2 py-1.5">
            {pages.map((item) => {
              const Icon = item.icon
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => navigate(item.id)}
                  className={cn(
                    "inline-flex shrink-0 items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium text-muted-foreground",
                    page === item.id && "bg-[#ddf4ff] text-[#0550ae]",
                  )}
                >
                  <Icon className="size-3.5" />
                  {item.label}
                </button>
              )
            })}
          </nav>
        </header>

        <main className="mx-auto w-full max-w-[1440px] px-4 py-5 sm:px-6 lg:px-8 lg:py-7">
          {healthy === false && (
            <div className="mb-4 flex items-start gap-2.5 rounded-md border border-[#ff8182] bg-[#ffebe9] px-3 py-2.5 text-sm text-[#a40e26]">
              <CircleX className="mt-0.5 size-4 shrink-0" />
              <div>
                <div className="font-medium">无法连接 Go 控制面</div>
                <div className="mt-0.5 text-xs opacity-80">
                  请从项目根目录运行 bash run.sh，并确认 19090 端口未被占用。
                </div>
              </div>
            </div>
          )}

          {page === "subscriptions" && <SubscriptionsPage onNotice={showNotice} />}
          {page === "nodes" && <NodesPage onNotice={showNotice} />}
          {page === "routing" && <RoutingPage onNotice={showNotice} />}
          {page === "alerts" && <AlertsPage onNotice={showNotice} />}
          {page === "artifacts" && <ArtifactsPage onNotice={showNotice} />}
          {page === "terminal" && <TerminalPage onNotice={showNotice} />}
        </main>
      </div>

      {notice && (
        <div className="fixed bottom-4 right-4 z-[90] flex max-w-sm items-start gap-2.5 rounded-md border bg-white px-3 py-2.5 shadow-[0_8px_24px_rgba(31,35,40,0.16)]">
          {notice.tone === "success" ? (
            <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-[#1a7f37]" />
          ) : (
            <CircleX className="mt-0.5 size-4 shrink-0 text-[#cf222e]" />
          )}
          <div className="min-w-0 text-sm leading-5">{notice.message}</div>
          <button
            type="button"
            onClick={() => setNotice(null)}
            className="ml-1 rounded p-0.5 text-muted-foreground hover:bg-[#f3f4f6] hover:text-foreground"
            aria-label="关闭提示"
          >
            <X className="size-3.5" />
          </button>
        </div>
      )}
    </div>
  )
}

function SidebarItem({
  item,
  active,
  onClick,
}: {
  item: (typeof pages)[number]
  active: boolean
  onClick: () => void
}) {
  const Icon = item.icon
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-sm text-[#57606a] transition-colors hover:bg-[#f3f4f6] hover:text-foreground",
        active && "bg-[#ddf4ff] font-medium text-[#0550ae]",
      )}
    >
      <Icon className="size-4 shrink-0" />
      <span className="min-w-0 flex-1">
        <span className="block truncate">{item.label}</span>
        <span className={cn("block truncate text-[11px] font-normal", active ? "text-[#0969da]" : "text-[#8c959f]")}>{item.description}</span>
      </span>
    </button>
  )
}

function StatusDot({ healthy }: { healthy: boolean | null }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border bg-white px-2 py-0.5 text-[11px] text-muted-foreground",
        healthy === true && "border-[#aceebb] bg-[#dafbe1] text-[#116329]",
        healthy === false && "border-[#ff8182] bg-[#ffebe9] text-[#a40e26]",
      )}
    >
      <span
        className={cn(
          "size-1.5 rounded-full bg-[#8c959f]",
          healthy === true && "bg-[#1a7f37]",
          healthy === false && "bg-[#cf222e]",
        )}
      />
      {healthy === null ? "检测中" : healthy ? "就绪" : "离线"}
    </span>
  )
}
