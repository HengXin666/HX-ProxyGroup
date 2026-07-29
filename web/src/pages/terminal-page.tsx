import { useCallback, useEffect, useRef, useState } from "react"
import { AlertTriangle, Play, Square, TerminalSquare } from "lucide-react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"

import { ApiError, api, type TerminalStatus } from "@/lib/api"
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

  const connect = useCallback(() => {
    if (!containerRef.current || socketRef.current) return
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
      onNotice("终端连接失败，请确认已登录且服务端已启用终端", "error")
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
  }, [onNotice])

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
            浏览器终端默认关闭。请在服务端以环境变量{" "}
            <code className="rounded bg-card/60 px-1">HX_PROXYGROUP_TERMINAL=1</code>{" "}
            启动控制面后刷新本页。启用后仍需管理员登录才能连接。
          </p>
        </div>
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
                disabled={connection === "connecting"}
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
