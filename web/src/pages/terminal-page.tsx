import { useCallback, useEffect, useRef, useState } from "react"
import { AlertTriangle, Play, Square, TerminalSquare } from "lucide-react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"

import { ApiError, api, type TerminalStatus } from "@/lib/api"

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
        theme: { background: "#ffffff", foreground: "#1f2328", cursor: "#1f2328" },
      })
      const fit = new FitAddon()
      terminal.loadAddon(fit)
      terminal.open(containerRef.current)
      terminalRef.current = terminal
      fitRef.current = fit
    }
    const terminal = terminalRef.current
    const fit = fitRef.current
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
        terminal.write(new Uint8Array(event.data))
      }
    }
    socket.onclose = (event) => {
      socketRef.current = null
      setConnection("closed")
      terminal.write(`\r\n\x1b[33m[会话已结束${event.reason ? `：${event.reason}` : ""}]\x1b[0m\r\n`)
    }
    socket.onerror = () => {
      onNotice("终端连接失败，请确认已登录且服务端已启用终端", "error")
    }

    const inputDisposable = terminal.onData((data) => {
      if (socket.readyState === WebSocket.OPEN) {
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
    })
  }, [onNotice])

  useEffect(() => () => disconnect(), [disconnect])

  if (status && !status.enabled) {
    return (
      <div className="space-y-4">
        <PageHeader />
        <div className="rounded-md border border-[#d4a72c66] bg-[#fff8c5] px-4 py-3 text-sm text-[#7d4e00]">
          <div className="font-medium">终端功能未启用</div>
          <p className="mt-1 text-xs leading-5">
            浏览器终端默认关闭。请在服务端以环境变量{" "}
            <code className="rounded bg-white/60 px-1">HX_PROXYGROUP_TERMINAL=1</code>{" "}
            启动控制面后刷新本页。启用后仍需管理员登录才能连接。
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <PageHeader />

      <div className="flex items-start gap-2.5 rounded-md border border-[#ff8182] bg-[#ffebe9] px-3 py-2.5 text-xs leading-5 text-[#a40e26]">
        <AlertTriangle className="mt-0.5 size-4 shrink-0" />
        <div>
          <span className="font-medium">高风险操作提示：</span>
          此终端以控制面进程用户身份在服务器上执行真实 Shell 命令。删除文件、修改系统配置、停止服务等操作立即生效且不可撤销。
          会话空闲 {status ? Math.round(status.idle_timeout_seconds / 60) : 10} 分钟后自动断开，全部会话都会写入审计日志。
        </div>
      </div>

      <section className="overflow-hidden rounded-md border bg-white">
        <header className="flex items-center justify-between gap-2 border-b bg-[#f6f8fa] px-3 py-2">
          <div className="flex items-center gap-2 text-sm font-medium">
            <TerminalSquare className="size-4 text-[#57606a]" />
            服务器终端
            <ConnectionBadge state={connection} />
          </div>
          <div className="flex items-center gap-2">
            {connection === "connected" ? (
              <button
                type="button"
                onClick={disconnect}
                className="inline-flex items-center gap-1.5 rounded-md border px-3 py-1 text-xs hover:bg-white"
              >
                <Square className="size-3.5" />
                断开
              </button>
            ) : (
              <button
                type="button"
                onClick={connect}
                disabled={connection === "connecting"}
                className="inline-flex items-center gap-1.5 rounded-md bg-[#1f883d] px-3 py-1 text-xs font-medium text-white hover:bg-[#1a7f37] disabled:opacity-60"
              >
                <Play className="size-3.5" />
                {connection === "connecting" ? "连接中…" : "连接"}
              </button>
            )}
          </div>
        </header>
        <div ref={containerRef} className="h-[480px] w-full bg-white p-2" />
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
    idle: { label: "未连接", className: "border-[#d0d7de] bg-white text-[#57606a]" },
    connecting: { label: "连接中", className: "border-[#d4a72c66] bg-[#fff8c5] text-[#7d4e00]" },
    connected: { label: "已连接", className: "border-[#aceebb] bg-[#dafbe1] text-[#116329]" },
    closed: { label: "已断开", className: "border-[#d0d7de] bg-[#f6f8fa] text-[#57606a]" },
  }
  return (
    <span className={`inline-flex rounded-full border px-2 py-0.5 text-[11px] ${meta[state].className}`}>
      {meta[state].label}
    </span>
  )
}
