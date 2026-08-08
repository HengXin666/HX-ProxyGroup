import { useCallback, useEffect, useRef, useState } from "react"
import { AlertTriangle, LoaderCircle, Maximize2, PanelLeft, Play, ShieldCheck, Square, TerminalSquare } from "lucide-react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"

import { ApiError, api, type TerminalStatus } from "@/lib/api"
import { FilePanel } from "@/components/terminal/file-panel"
import { HostMonitor } from "@/components/terminal/host-monitor"
import { Input } from "@/components/ui/input"
import { detectPwdOutput, parseFirstWord, quoteForShell, resolveCdTarget } from "@/lib/terminal-cwd"
import { subscribeTheme } from "@/lib/theme"
import { cn } from "@/lib/utils"

type ConnectionState = "idle" | "connecting" | "connected" | "closed"

// Recognize a complete `cd` command line typed at the shell prompt. Returns
// the raw argument (first word only semantics) or null when the line is not a
// plain `cd` (chains, redirects and other commands are ignored conservatively).
function parseTypedCd(line: string): { argument: string | null } | null {
  const match = line.match(/^\s*cd(?:\s+(.+?))?\s*$/)
  if (!match) return null
  const argument = match[1]
  if (argument === undefined) return { argument: null }
  // `cd /tmp && ls` or `cd /x; ls`: the shell may run more than one command.
  // Only a plain `cd <target>` is tracked; anything else is left alone.
  if (/[;&|<>]/.test(argument)) return null
  return { argument }
}

export function TerminalPage({
  onNotice,
}: {
  onNotice: (message: string, tone?: "success" | "error") => void
}) {
  const containerRef = useRef<HTMLDivElement>(null)
  const terminalRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const socketRef = useRef<WebSocket | null>(null)
  const keepAliveRef = useRef<number | null>(null)
  const cwdRef = useRef("/")
  const pwdPendingRef = useRef(false)
  const pwdDeadlineRef = useRef(0)
  const pwdBufferRef = useRef("")
  const inputDisposableRef = useRef<{ dispose: () => void } | null>(null)
  const [status, setStatus] = useState<TerminalStatus | null>(null)
  const [connection, setConnection] = useState<ConnectionState>("idle")
  const [twoFactorCode, setTwoFactorCode] = useState("")
  const [unlocking, setUnlocking] = useState(false)
  const [panelHidden, setPanelHidden] = useState(false)
  const [cwd, setCwd] = useState("/")

  const updateCwd = useCallback((next: string) => {
    cwdRef.current = next
    setCwd(next)
  }, [])

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
    if (keepAliveRef.current !== null) {
      window.clearInterval(keepAliveRef.current)
      keepAliveRef.current = null
    }
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

  // Keep the session synced with server PTY clock even when the browser tab is
  // hidden: switching pages must NOT disconnect the user. A lightweight no-op
  // ping frame every 20s encourages NAT/proxy keepalive and helps the server
  // keep the session lastActive fresh without sending real input.
  const startKeepalive = useCallback((socket: WebSocket) => {
    if (keepAliveRef.current !== null) window.clearInterval(keepAliveRef.current)
    keepAliveRef.current = window.setInterval(() => {
      if (socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: "resize", cols: 0, rows: 0 }))
      }
    }, 20_000)
  }, [])

  // When returning to this tab, the terminal may have buffered output while we
  // were away. We do NOT reconnect — the socket is still open — but we
  // re-fit so the visible terminal matches the viewport again.
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === "visible") {
        fitRef.current?.fit()
        if (terminalRef.current) terminalRef.current.focus()
      }
    }
    document.addEventListener("visibilitychange", onVisible)
    return () => document.removeEventListener("visibilitychange", onVisible)
  }, [])

  // Debounced refit on container resize so the PTY window tracks layout changes
  // caused by expanding/collapsing the side panel.
  useEffect(() => {
    if (!containerRef.current) return
    const observer = new ResizeObserver(() => {
      fitRef.current?.fit()
      const term = terminalRef.current
      const socket = socketRef.current
      if (term && socket?.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }))
      }
    })
    observer.observe(containerRef.current)
    return () => observer.disconnect()
  }, [])

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
      subscribeTheme(() => { terminalRef.current && (terminalRef.current.options.theme = terminalTheme()) })
    }
    const terminal = terminalRef.current
    const fit = fitRef.current
    // Reconnecting must not stack onData listeners: each leaked listener
    // re-sends every keystroke, so a reconnect used to echo `d` as `dd`.
    inputDisposableRef.current?.dispose()
    inputDisposableRef.current = null
    let pendingInput = ""
    let inputTimer: number | null = null
    const flushInput = () => {
      if (inputTimer !== null) window.clearTimeout(inputTimer)
      inputTimer = null
      const socket = socketRef.current
      if (pendingInput && socket && socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: "input", data: pendingInput }))
      }
      pendingInput = ""
    }
    terminal.reset()
    fit?.fit()

    const socket = new WebSocket(api.terminalSocketURL())
    socket.binaryType = "arraybuffer"
    socketRef.current = socket

    // Streaming decoder lets us peek at PTY output for cwd tracking without
    // touching the raw bytes written to the terminal.
    const decoder = new TextDecoder()

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
      startKeepalive(socket)
      // Ask the shell where it started so the file panel matches immediately.
      // The probe is time-bounded, not frame-bounded: a slow shell banner may
      // split the `pwd` echo and result across many frames.
      pwdPendingRef.current = true
      pwdDeadlineRef.current = Date.now() + 4000
      pwdBufferRef.current = ""
      socket.send(JSON.stringify({ type: "input", data: "pwd\n" }))
    }
    // Ask the shell for its current directory again. Used when a `cd` target
    // cannot be resolved from what the user typed (bare `cd`, `~`, `$VAR`, `-`).
    const reprobePwd = () => {
      const socket = socketRef.current
      if (!socket || socket.readyState !== WebSocket.OPEN || pwdPendingRef.current) return
      pwdPendingRef.current = true
      pwdDeadlineRef.current = Date.now() + 4000
      pwdBufferRef.current = ""
      socket.send(JSON.stringify({ type: "input", data: "pwd\n" }))
    }
    socket.onmessage = (event) => {
      if (!(event.data instanceof ArrayBuffer)) return
      const bytes = new Uint8Array(event.data)
      terminal.write(bytes)
      const text = decoder.decode(bytes, { stream: true })
      if (!pwdPendingRef.current) return
      // The shell may split the `pwd` echo/result across many frames (a slow
      // banner, the prompt redraw, ...); accumulate until the path line appears
      // or the time budget runs out.
      pwdBufferRef.current += text
      const target = detectPwdOutput(pwdBufferRef.current)
      if (target) {
        pwdPendingRef.current = false
        updateCwd(target)
      } else if (Date.now() > pwdDeadlineRef.current) {
        pwdPendingRef.current = false
      }
    }
    socket.onclose = (event) => {
      if (keepAliveRef.current !== null) {
        window.clearInterval(keepAliveRef.current)
        keepAliveRef.current = null
      }
      socketRef.current = null
      pwdPendingRef.current = false
      pwdBufferRef.current = ""
      setConnection("closed")
      terminal.write(`\r\n\x1b[33m[会话已结束${event.reason ? `：${event.reason}` : ""}]\x1b[0m\r\n`)
      if (event.wasClean === false) {
        onNotice("终端连接被中断，请确认网络稳定后重连", "error")
      }
    }
    socket.onerror = () => {
      onNotice("终端连接失败，请确认已登录并完成 2FA 解锁", "error")
    }

    const inputDisposable = terminal.onData((data) => {
      if (!socketRef.current || socketRef.current.readyState !== WebSocket.OPEN) return
      // Collapse safe keystrokes into one frame (≤12ms) so weak round-trips do
      // not make typing feel laggy.
      pendingInput += data
      const isControl = /[\x00-\x1f]/.test(data) || data === "\x7f"
      if (isControl || pendingInput.length >= 1024) {
        flushInput()
      } else if (inputTimer === null) {
        inputTimer = window.setTimeout(flushInput, 12)
      }
      trackTypedCd(data)
    })

    // Keep the file panel in sync with what the user types. Detecting `cd`
    // from the OUTPUT is unreliable (fancy prompts interleave escape
    // sequences, backspaces and re-echoes with the command), so track the
    // INPUT stream instead: accumulate the current line, and when Enter is
    // pressed resolve a leading `cd` against the known cwd.
    let typedLine = ""
    const trackTypedCd = (data: string) => {
      for (const ch of data) {
        if (ch === "\r") {
          const line = typedLine
          typedLine = ""
          const cd = parseTypedCd(line)
          if (!cd) continue
          if (cd.argument === null) {
            reprobePwd() // bare `cd` -> the shell moved to $HOME
            continue
          }
          const argument = parseFirstWord(cd.argument)
          const target = argument === null || argument === "" ? null : resolveCdTarget(cwdRef.current, argument)
          if (target) updateCwd(target)
          else reprobePwd() // `cd ~/...`, `cd $VAR`, `cd -` ...
        } else if (ch === "\x7f") {
          typedLine = typedLine.slice(0, -1)
        } else if (ch === "\x1b" || /[\x00-\x1f]/.test(ch)) {
          typedLine = "" // navigation / search / completion: reset the guess
        } else {
          typedLine += ch
          // Drop lines that cannot possibly become a `cd` command (`ls`,
          // `QQQQcd /x`, …) so a later Enter cannot misread them.
          if (!/^c(?:d(?:\s.*)?)?$/.test(typedLine)) typedLine = ""
        }
      }
    }

    inputDisposableRef.current = inputDisposable
  }, [onNotice, startKeepalive, status?.two_factor_verified, updateCwd])

  // Navigate from the file panel: update the shared cwd and drive the shell.
  const handlePanelPath = useCallback((next: string) => {
    // Ignore duplicate navigations (e.g. the second click of a double-click);
    // the shell already is where the panel wants to go.
    if (next === cwdRef.current) return
    updateCwd(next)
    const socket = socketRef.current
    if (socket?.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ type: "input", data: `cd ${quoteForShell(next)}\n` }))
    }
  }, [updateCwd])

  useEffect(() => {
    return () => {
      if (keepAliveRef.current !== null) window.clearInterval(keepAliveRef.current)
      socketRef.current?.close(1000, "unmount")
      socketRef.current = null
      inputDisposableRef.current?.dispose()
      inputDisposableRef.current = null
      terminalRef.current?.dispose()
      terminalRef.current = null
    }
  }, [])

  if (status && !status.two_factor_verified) {
    return (
      <div className="space-y-4">
        <PageHeader />
        <section className="rounded-md border bg-card p-4">
          <div className="flex items-center gap-2 text-sm font-medium"><ShieldCheck className="size-4 text-primary" />验证 2FA 后解锁终端</div>
          <p className="mt-2 text-xs leading-5 text-muted-foreground">输入验证器当前显示的 6 位验证码。验证成功后，当前登录会话可在 {Math.round(status.two_factor_verification_ttl_seconds / 60)} 分钟内建立终端连接。</p>
          <div className="mt-4 flex items-end gap-2"><label className="block min-w-0 flex-1 text-xs font-medium">一次性验证码<Input aria-label="终端 2FA 验证码" inputMode="numeric" maxLength={6} value={twoFactorCode} onChange={(event) => setTwoFactorCode(event.target.value.replace(/\D/g, "").slice(0, 6))} className="mt-1 font-mono tracking-[0.25em]" /></label><button type="button" onClick={() => void unlockTerminal()} disabled={unlocking} className="inline-flex h-9 items-center gap-1.5 rounded-md bg-primary px-3 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-60">{unlocking ? <LoaderCircle className="size-3.5 animate-spin" /> : <ShieldCheck className="size-3.5" />}解锁</button></div>
        </section>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <PageHeader />
        <button
          type="button"
          onClick={() => setPanelHidden((v) => !v)}
          className="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs hover:bg-muted"
        >
          <PanelLeft className="size-3.5" />
          {panelHidden ? "展开侧栏" : "收起侧栏"}
        </button>
      </div>

      <div className="flex items-start gap-2.5 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-xs leading-5 text-destructive">
        <AlertTriangle className="mt-0.5 size-4 shrink-0" />
        <div>
          <span className="font-medium">高风险操作提示：</span>
          此终端以控制面进程用户身份在服务器上执行真实 Shell 命令。删除文件、修改系统配置、停止服务等操作立即生效且不可撤销。会话无空闲与寿命上限，全部会话都会写入审计日志。
        </div>
      </div>

      <div className={cn("grid min-h-0 flex-1 gap-3", panelHidden ? "grid-cols-1" : "grid-cols-1 lg:grid-cols-[1fr_320px]")}>
        <section className="flex min-h-0 flex-col overflow-hidden rounded-md border bg-card">
          <header className="flex items-center justify-between gap-2 border-b bg-muted/60 px-3 py-2">
            <div className="flex items-center gap-2 text-sm font-medium">
              <TerminalSquare className="size-4 text-muted-foreground" />
              服务器终端
              {status?.privileged && <span className="rounded-full border border-warning-border bg-warning-muted px-2 py-0.5 text-[11px] text-warning-foreground">root PTY</span>}
              <ConnectionBadge state={connection} />
            </div>
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => fitRef.current?.fit()}
                className="inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs hover:bg-card"
                title="重新适配窗口尺寸"
              >
                <Maximize2 className="size-3.5" />
              </button>
              {connection === "connected" ? (
                <button type="button" onClick={disconnect} className="inline-flex items-center gap-1.5 rounded-md border px-3 py-1 text-xs hover:bg-card">
                  <Square className="size-3.5" /> 断开
                </button>
              ) : (
                <button
                  type="button"
                  onClick={connect}
                  disabled={connection === "connecting" || !status?.two_factor_verified}
                  className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-60"
                >
                  <Play className="size-3.5" /> {connection === "connecting" ? "连接中…" : "连接"}
                </button>
              )}
            </div>
          </header>
          <div className="relative min-h-0 flex-1 bg-background">
            <div ref={containerRef} className="absolute inset-0 p-2" />
          </div>
        </section>

        {!panelHidden && (
          <aside className="hidden min-h-0 flex-col gap-3 rounded-md border bg-card lg:flex">
            {connection === "connected" ? (
              <>
                <div className="overflow-auto p-2">
                  <HostMonitor enabled />
                </div>
                <div className="mx-2 border-t" />
                <div className="min-h-0 flex-1">
                  <FilePanel path={cwd} connected onPathChange={handlePanelPath} onNotice={onNotice} />
                </div>
              </>
            ) : (
              <div className="flex min-h-40 flex-1 flex-col items-center justify-center gap-1.5 p-4 text-center text-xs text-muted-foreground">
                <TerminalSquare className="size-5 opacity-60" />
                <div className="font-medium">连接后查看服务器数据</div>
                <div className="text-[11px] opacity-70">系统监控与文件管理将在终端连接后显示</div>
              </div>
            )}
          </aside>
        )}
      </div>
    </div>
  )
}

function PageHeader() {
  return (
    <div>
      <h1 className="text-lg font-semibold">终端</h1>
      <p className="mt-0.5 text-sm text-muted-foreground">
        浏览器内服务器终端，切换页面不断连，集成系统监控与文件管理；文件目录与 Shell 实时同步。
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
  return {
    background: color("--background"),
    foreground: color("--foreground"),
    cursor: color("--foreground"),
    selectionBackground: color("--accent"),
  }
}
