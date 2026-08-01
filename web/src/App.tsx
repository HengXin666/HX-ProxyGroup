import { useCallback, useEffect, useState } from "react"
import {
  Archive,
  BellRing,
  CheckCircle2,
  CircleX,
  Globe,
  Info,
  LayoutDashboard,
  LogOut,
  ListFilter,
  Network,
  PanelLeftClose,
  PanelLeftOpen,
  Radio,
  Route,
  Settings2,
  TerminalSquare,
  X,
} from "lucide-react"

import { ThemeToggle } from "@/components/theme-toggle"
import { api, setCsrfToken, setUnauthenticatedHandler } from "@/lib/api"
import { cn } from "@/lib/utils"
import { AlertsPage } from "@/pages/alerts-page"
import { AboutPage } from "@/pages/about-page"
import { ArtifactsPage } from "@/pages/artifacts-page"
import { AuthPage } from "@/pages/auth-page"
import { InventoryPage } from "@/pages/inventory-page"
import { OverviewPage } from "@/pages/overview-page"
import { RoutingPage } from "@/pages/routing-page"
import { ResidentialPage } from "@/pages/residential-page"
import { RulesPage } from "@/pages/rules-page"
import { SettingsPage } from "@/pages/settings-page"
import { TerminalPage } from "@/pages/terminal-page"

type Page = "overview" | "subscriptions" | "routing" | "residential" | "rules" | "settings" | "alerts" | "artifacts" | "terminal" | "about"
type Notice = { id: number; message: string; tone: "success" | "error" }
const sidebarStorageKey = "hx-proxygroup.sidebar-collapsed"

const pages: Array<{
  id: Page
  label: string
  description: string
  icon: typeof Radio
}> = [
  { id: "overview", label: "总览", description: "流量与路由", icon: LayoutDashboard },
  { id: "subscriptions", label: "订阅与节点", description: "来源、刷新与质量", icon: Radio },
  { id: "routing", label: "代理服务", description: "代理组与 Listener", icon: Route },
  { id: "residential", label: "住宅代理", description: "动态住宅 IP 渠道", icon: Globe },
  { id: "rules", label: "站点别名", description: "可复用网页组", icon: ListFilter },
  { id: "settings", label: "全局配置", description: "测速、DNS 与性能", icon: Settings2 },
  { id: "alerts", label: "告警", description: "状态与邮件通知", icon: BellRing },
  { id: "artifacts", label: "备份", description: "Backup 与 Export", icon: Archive },
  { id: "terminal", label: "终端", description: "v2 · 服务器 Shell", icon: TerminalSquare },
  { id: "about", label: "关于", description: "版本、GitHub 与更新", icon: Info },
]

function pageFromHash(): Page {
  const value = window.location.hash.replace(/^#\/?/, "")
  if (value === "nodes") return "subscriptions"
  return pages.some((item) => item.id === value) ? (value as Page) : "overview"
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
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => window.localStorage.getItem(sidebarStorageKey) === "1")

  useEffect(() => {
    window.localStorage.setItem(sidebarStorageKey, sidebarCollapsed ? "1" : "0")
  }, [sidebarCollapsed])

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

  const requireLogin = useCallback(() => {
    setCsrfToken("")
    setAuthGate({ phase: "required", configured: true })
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
    <div className={cn(
      "min-h-screen bg-background lg:grid lg:h-screen lg:overflow-hidden lg:transition-[grid-template-columns] lg:duration-200",
      sidebarCollapsed ? "lg:grid-cols-[68px_minmax(0,1fr)]" : "lg:grid-cols-[228px_minmax(0,1fr)]",
    )}>
      <aside data-sidebar data-collapsed={sidebarCollapsed} className="relative z-50 hidden min-h-screen border-r bg-card lg:sticky lg:top-0 lg:flex lg:h-screen lg:flex-col">
        <div className={cn("flex h-14 items-center border-b", sidebarCollapsed ? "justify-center px-2" : "gap-2.5 px-3")}>
          {!sidebarCollapsed && <>
            <div className="flex size-7 shrink-0 items-center justify-center rounded-md bg-[#24292f] text-white"><Network className="size-4" /></div>
            <div className="min-w-0 flex-1 truncate text-sm font-semibold">HX-ProxyGroup</div>
          </>}
          <div className="group relative flex">
            <button
              type="button"
              className="flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
              onClick={() => setSidebarCollapsed((current) => !current)}
              aria-label={sidebarCollapsed ? "展开侧栏" : "折叠侧栏"}
              aria-expanded={!sidebarCollapsed}
            >
              {sidebarCollapsed ? <PanelLeftOpen className="size-4" /> : <PanelLeftClose className="size-4" />}
            </button>
            {sidebarCollapsed && <SidebarTooltip>展开侧栏</SidebarTooltip>}
          </div>
        </div>

        <nav className="space-y-1 p-2" aria-label="主导航">
          {pages.map((item) => (
            <SidebarItem
              key={item.id}
              item={item}
              active={item.id === page}
              onClick={() => navigate(item.id)}
              collapsed={sidebarCollapsed}
            />
          ))}
        </nav>

        <div className={cn("mt-auto border-t", sidebarCollapsed ? "space-y-1 p-2" : "p-3")}>
          {sidebarCollapsed ? <>
            <SidebarIconSlot tooltip={`后端状态：${healthy === null ? "检测中" : healthy ? "就绪" : "离线"}`}><StatusDot healthy={healthy} compact /></SidebarIconSlot>
            <SidebarIconSlot tooltip="切换主题"><ThemeToggle /></SidebarIconSlot>
            {authGate.username && <SidebarIconSlot tooltip={`退出登录（${authGate.username}）`}>
              <button type="button" onClick={() => void handleLogout()} className="flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground" aria-label={`退出登录（${authGate.username}）`}><LogOut className="size-4" /></button>
            </SidebarIconSlot>}
          </> : <>
            <div className="rounded-md border bg-muted/60 px-3 py-2.5">
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs font-medium">后端状态</span>
              <StatusDot healthy={healthy} />
            </div>
            <div className="mt-1 text-[11px] leading-4 text-muted-foreground">
              Mihomo 数据面、节点检测、持久化流量统计、实时日志与告警均已接入。
            </div>
            </div>
            <div className="mt-2 flex h-8 items-center justify-between rounded-md px-2.5 text-xs text-muted-foreground"><span>界面主题</span><ThemeToggle /></div>
          {authGate.username && (
            <button
              type="button"
              onClick={() => void handleLogout()}
              className="mt-2 flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            >
              <LogOut className="size-3.5 shrink-0" />
              <span className="min-w-0 flex-1 truncate">退出登录（{authGate.username}）</span>
            </button>
          )}
          </>}
        </div>
      </aside>

      <div className={cn("min-w-0 lg:h-screen", page === "routing" ? "lg:overflow-hidden" : "lg:overflow-y-auto")}>
        <header className="sticky top-0 z-40 border-b bg-card lg:hidden">
          <div className="flex h-13 items-center gap-2 px-3">
            <div className="flex size-7 items-center justify-center rounded-md bg-[#24292f] text-white">
              <Network className="size-4" />
            </div>
            <span className="font-semibold">HX-ProxyGroup</span>
            <div className="ml-auto flex items-center gap-1"><ThemeToggle /><StatusDot healthy={healthy} /></div>
          </div>
          <nav className="scrollbar-none flex gap-1 overflow-x-auto border-t px-2 py-1.5">
            {pages.map((item) => {
              const Icon = item.icon
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => navigate(item.id)}
                  className={cn(
                    "inline-flex shrink-0 items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium text-muted-foreground",
                    page === item.id && "bg-accent text-accent-foreground",
                  )}
                >
                  <Icon className="size-3.5" />
                  {item.label}
                </button>
              )
            })}
          </nav>
        </header>

        <main key={page} className={cn("page-enter mx-auto w-full max-w-[1440px] px-4 py-5 sm:px-6 lg:px-8 lg:py-7", page === "routing" && "lg:h-full lg:max-w-none lg:overflow-hidden")}>
          {healthy === false && (
            <div className="mb-4 flex items-start gap-2.5 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-sm text-destructive">
              <CircleX className="mt-0.5 size-4 shrink-0" />
              <div>
                <div className="font-medium">无法连接 Go 控制面</div>
                <div className="mt-0.5 text-xs opacity-80">
                  请从项目根目录运行 bash run.sh，并确认 19090 端口未被占用。
                </div>
              </div>
            </div>
          )}

          {page === "overview" && <OverviewPage onNotice={showNotice} />}
          {page === "subscriptions" && <InventoryPage initialView={window.location.hash.includes("nodes") ? "nodes" : "subscriptions"} onNotice={showNotice} />}
          {page === "routing" && <RoutingPage onNotice={showNotice} />}
          {page === "residential" && <ResidentialPage onNotice={showNotice} />}
          {page === "rules" && <RulesPage onNotice={showNotice} />}
          {page === "settings" && <SettingsPage onNotice={showNotice} username={authGate.username} onSignedOut={requireLogin} />}
          {page === "alerts" && <AlertsPage onNotice={showNotice} />}
          {page === "artifacts" && <ArtifactsPage onNotice={showNotice} />}
          {page === "terminal" && <TerminalPage onNotice={showNotice} />}
          {page === "about" && <AboutPage onNotice={showNotice} />}
        </main>
      </div>

      {notice && (
        <div className="toast-enter fixed bottom-4 right-4 z-[90] flex max-w-sm items-start gap-2.5 rounded-md border bg-card px-3 py-2.5 shadow-[0_12px_32px_rgba(23,33,31,0.16)]">
          {notice.tone === "success" ? (
            <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success" />
          ) : (
            <CircleX className="mt-0.5 size-4 shrink-0 text-destructive" />
          )}
          <div className="min-w-0 text-sm leading-5">{notice.message}</div>
          <button
            type="button"
            onClick={() => setNotice(null)}
            className="ml-1 rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
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
  collapsed,
}: {
  item: (typeof pages)[number]
  active: boolean
  onClick: () => void
  collapsed: boolean
}) {
  const Icon = item.icon
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={collapsed ? item.label : undefined}
      className={cn(
        "group relative flex w-full items-center rounded-md text-left text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground",
        collapsed ? "h-10 justify-center px-0" : "gap-2.5 px-2.5 py-2",
        active && "bg-accent font-medium text-accent-foreground",
      )}
    >
      <Icon className="size-4 shrink-0" />
      {!collapsed && <span className="min-w-0 flex-1">
        <span className="block truncate">{item.label}</span>
        <span className={cn("block truncate text-[11px] font-normal", active ? "text-accent-foreground/80" : "text-muted-foreground")}>{item.description}</span>
      </span>}
      {collapsed && <SidebarTooltip><span className="font-medium">{item.label}</span><span className="ml-1.5 text-muted-foreground">{item.description}</span></SidebarTooltip>}
    </button>
  )
}

function SidebarTooltip({ children }: { children: React.ReactNode }) {
  return <span data-sidebar-tooltip role="tooltip" className="pointer-events-none absolute left-[calc(100%+8px)] top-1/2 z-[70] -translate-y-1/2 whitespace-nowrap rounded-md border bg-popover px-2.5 py-1.5 text-xs text-popover-foreground opacity-0 shadow-lg transition-opacity group-hover:opacity-100">{children}</span>
}

function SidebarIconSlot({ children, tooltip }: { children: React.ReactNode; tooltip: string }) {
  return <div className="group relative flex justify-center">{children}<SidebarTooltip>{tooltip}</SidebarTooltip></div>
}

function StatusDot({ healthy, compact = false }: { healthy: boolean | null; compact?: boolean }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border bg-card px-2 py-0.5 text-[11px] text-muted-foreground",
        compact && "size-8 justify-center p-0",
        healthy === true && "border-success-border bg-success-muted text-success-foreground",
        healthy === false && "border-destructive/40 bg-destructive/10 text-destructive",
      )}
    >
      <span
        className={cn(
          "size-1.5 rounded-full bg-muted-foreground",
          healthy === true && "bg-success",
          healthy === false && "bg-destructive",
        )}
      />
      {!compact && (healthy === null ? "检测中" : healthy ? "就绪" : "离线")}
    </span>
  )
}
