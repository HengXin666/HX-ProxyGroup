import { useCallback, useEffect, useRef, useState } from "react"
import { AlertTriangle, LoaderCircle, Play, ShieldCheck, Square, TerminalSquare } from "lucide-react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"

import { ApiError, api, type TerminalStatus } from "@/lib/api"
import { Input } from "@/components/ui/input"
import { PredictiveEcho, type TerminalMode } from "@/lib/terminal-echo"
import { subscribeTheme } from "@/lib/theme"

type ConnectionState = "idle" | "connecting" | "connected" | "closed"

export function TerminalPage({
  onNotice,
}: {
  onNotice: (message: string, tone?: "success" | "error") => void
}) {
  const containerRef = useRef<HTMLDivElement>(null)
  const terminalRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const socketRef = useRef<WebSocket | null>(null)
  const echoRef = useRef(new PredictiveEcho())
  const [status, setStatus] = useState<TerminalStatus | null>(null)
  const [connection, setConnection] = useState<ConnectionState>("idle")
  const [twoFactorCode, setTwoFactorCode] = useState("")
  const [unlocking, setUnlocking] = useState(false)

  useEffect(() => {
    let cancelled = false
    api
      .terminalStatus()
      .then((result) => {
        if (!cancelled) setStatus(result)
      })
      .catch((cause) => {
        if (!cancelled && cause instanceof ApiError) onNotice(cause.message, "error")
      })
    return () => {
      cancelled = true
    }
  }, [onNotice])

  const disconnect = useCallback(() => {
    socketRef.current?.close(1000, "user disconnect")
    socketRef.current = null
    setConnection("closed")
  }, [])

  async function unlockTerminal() {
    if (!/^\d{6}$/.test(twoFactorCode.trim())) {
      onNotice("请输入 6 位 2FA 验证码", "error")
      return
    }
    setUnlocking(true)
    try {
      await api.verifyTwoFactor(twoFactorCode.trim())
      setTwoFactorCode("")
      setStatus(await api.terminalStatus())
      onNotice("终端已解锁，15 分钟内可建立终端会话")
    } catch (cause) {
      onNotice(cause instanceof Error ? cause.message : "2FA 验证失败", "error")
    } finally {
      setUnlocking(false)
    }
  }

  const connect = useCallback(() => {
    if (!containerRef.current || socketRef.current || !status?.two_factor_verified) return
    setConnection("connecting")

    if (!terminalRef.current) {
      const terminal = new Terminal({
        cursorBlink: true,
        fontSize: 13,
        fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
        theme: terminalTheme(),
      })
      const fit = new FitAddon()
      terminal.loadAddon(fit)
      terminal.open(containerRef.current)
      terminalRef.current = terminal
      fitRef.current = fit
    }
    const terminal = terminalRef.current
    const fit = fitRef.current
    const predictiveEcho = echoRef.current
    predictiveEcho.reset()
    terminal.reset()
    fit?.fit()

    const socket = new WebSocket(api.terminalSocketURL())
    socket.binaryType = "arraybuffer"
    socketRef.current = socket

    const sendResize = () => {
      if (socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: "resize", cols: terminal.cols, rows: terminal.rows }))
      }
    }

    socket.onopen = () => {
      setConnection("connected")
      fit?.fit()
      sendResize()
      terminal.focus()
    }
    socket.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) {
        const output = predictiveEcho.consume(new Uint8Array(event.data))
        if (output.byteLength > 0) terminal.write(output)
        return
      }
      if (typeof event.data === "string") {
        try {
          const message = JSON.parse(event.data) as { type?: string } & Partial<TerminalMode>
          if (message.type === "mode" && typeof message.echo === "boolean" && typeof message.canonical === "boolean") {
            predictiveEcho.setMode({ echo: message.echo, canonical: message.canonical })
          }
        } catch {
          predictiveEcho.reset()
        }
      }
    }
    socket.onclose = (event) => {
      socketRef.current = null
      predictiveEcho.reset()
      setConnection("closed")
      terminal.write(`\r\n\x1b[33m[会话已结束${event.reason ? `：${event.reason}` : ""}]\x1b[0m\r\n`)
    }
    socket.onerror = () => {
      onNotice("终端连接失败，请确认已登录并完成 2FA 解锁", "error")
    }

    const inputDisposable = terminal.onData((data) => {
      if (socket.readyState === WebSocket.OPEN) {
        const predicted = predictiveEcho.predict(data)
        if (predicted != null) terminal.write(predicted)
        socket.send(JSON.stringify({ type: "input", data }))
      }
    })
    const resizeDisposable = terminal.onResize(() => sendResize())
    const observer = new ResizeObserver(() => fit?.fit())
    observer.observe(containerRef.current)

    socket.addEventListener("close", () => {
      inputDisposable.dispose()
      resizeDisposable.dispose()
      observer.disconnect()
      predictiveEcho.reset()
    })
  }, [onNotice, status])

  useEffect(() => () => disconnect(), [disconnect])
  useEffect(() => subscribeTheme(() => {
    if (terminalRef.current) terminalRef.current.options.theme = terminalTheme()
  }), [])

  if (status && !status.enabled) {
    return (
      <div className="space-y-4">
        <PageHeader />
        <div className="rounded-md border border-warning-border bg-warning-muted px-4 py-3 text-sm text-warning-foreground">
          <div className="font-medium">终端功能未启用</div>
          <p className="mt-1 text-xs leading-5">
            当前服务端已显式关闭终端。终端默认开启；如需恢复，请移除环境变量{" "}
            <code className="rounded bg-card/60 px-1">HX_PROXYGROUP_TERMINAL=0</code>{" "}
            后重启控制面。
          </p>
        </div>
      </div>
    )
  }

  if (status && !status.two_factor_enabled) {
    return (
      <div className="space-y-4">
        <PageHeader />
        <div className="rounded-md border border-warning-border bg-warning-muted px-4 py-3 text-sm text-warning-foreground">
          <div className="font-medium">需要先启用 2FA</div>
          <p className="mt-1 text-xs leading-5">请进入“全局配置 → 账号安全”，生成并启用 TOTP 2FA。终端默认开启，但没有 2FA 时不会接受 Shell 连接。</p>
        </div>
      </div>
    )
  }

  if (status && !status.two_factor_verified) {
    return (
      <div className="space-y-4">
        <PageHeader />
        <section className="max-w-md rounded-md border bg-card p-4">
          <div className="flex items-center gap-2 text-sm font-medium"><ShieldCheck className="size-4 text-primary" />验证 2FA 后解锁终端</div>
          <p className="mt-2 text-xs leading-5 text-muted-foreground">输入验证器当前显示的 6 位验证码。验证成功后，当前登录会话可在 {Math.round(status.two_factor_verification_ttl_seconds / 60)} 分钟内建立终端连接。</p>
          <div className="mt-4 flex items-end gap-2"><label className="block min-w-0 flex-1 text-xs font-medium">一次性验证码<Input aria-label="终端 2FA 验证码" inputMode="numeric" maxLength={6} value={twoFactorCode} onChange={(event) => setTwoFactorCode(event.target.value.replace(/\D/g, "").slice(0, 6))} className="mt-1 font-mono tracking-[0.25em]" /></label><button type="button" onClick={() => void unlockTerminal()} disabled={unlocking} className="inline-flex h-9 items-center gap-1.5 rounded-md bg-primary px-3 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-60">{unlocking ? <LoaderCircle className="size-3.5 animate-spin" /> : <ShieldCheck className="size-3.5" />}解锁</button></div>
        </section>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <PageHeader />

      <div className="flex items-start gap-2.5 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-xs leading-5 text-destructive">
        <AlertTriangle className="mt-0.5 size-4 shrink-0" />
        <div>
          <span className="font-medium">高风险操作提示：</span>
          此终端以控制面进程用户身份在服务器上执行真实 Shell 命令。删除文件、修改系统配置、停止服务等操作立即生效且不可撤销。
          会话空闲 {status ? Math.round(status.idle_timeout_seconds / 60) : 10} 分钟后自动断开，全部会话都会写入审计日志。
        </div>
      </div>

      <section className="overflow-hidden rounded-md border bg-card">
        <header className="flex items-center justify-between gap-2 border-b bg-muted/60 px-3 py-2">
          <div className="flex items-center gap-2 text-sm font-medium">
            <TerminalSquare className="size-4 text-muted-foreground" />
            服务器终端
            <ConnectionBadge state={connection} />
          </div>
          <div className="flex items-center gap-2">
            {connection === "connected" ? (
              <button
                type="button"
                onClick={disconnect}
                className="inline-flex items-center gap-1.5 rounded-md border px-3 py-1 text-xs hover:bg-card"
              >
                <Square className="size-3.5" />
                断开
              </button>
            ) : (
              <button
                type="button"
                onClick={connect}
                disabled={connection === "connecting" || !status?.two_factor_verified}
                className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-60"
              >
                <Play className="size-3.5" />
                {connection === "connecting" ? "连接中…" : "连接"}
              </button>
            )}
          </div>
        </header>
        <div ref={containerRef} className="h-[480px] w-full bg-background p-2" />
      </section>
    </div>
  )
}

function PageHeader() {
  return (
    <div>
      <h1 className="text-lg font-semibold">终端（v2）</h1>
      <p className="mt-0.5 text-sm text-muted-foreground">
        基于 xterm.js 与服务端 PTY 的浏览器内终端。独立开关、管理员认证、空闲超时与审计缺一不可。
      </p>
    </div>
  )
}

function ConnectionBadge({ state }: { state: ConnectionState }) {
  const meta: Record<ConnectionState, { label: string; className: string }> = {
    idle: { label: "未连接", className: "border-border bg-card text-muted-foreground" },
    connecting: { label: "连接中", className: "border-warning-border bg-warning-muted text-warning-foreground" },
    connected: { label: "已连接", className: "border-success-border bg-success-muted text-success-foreground" },
    closed: { label: "已断开", className: "border-border bg-muted/60 text-muted-foreground" },
  }
  return (
    <span className={`inline-flex rounded-full border px-2 py-0.5 text-[11px] ${meta[state].className}`}>
      {meta[state].label}
    </span>
  )
}

function terminalTheme() {
  const styles = getComputedStyle(document.documentElement)
  const color = (name: string) => styles.getPropertyValue(name).trim()
  return { background: color("--background"), foreground: color("--foreground"), cursor: color("--foreground"), selectionBackground: color("--accent") }
}
