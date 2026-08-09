// Helpers for keeping the file panel and the shell cwd in sync.
//
// The panel drives the shell by sending `cd` lines and asks the shell for its
// directory with a `pwd` probe; the typed `cd` command is recognized from the
// INPUT stream in the terminal page (the PTY echo is not scanned because fancy
// prompts interleave escape sequences and re-echoes with the command). These
// helpers are deliberately conservative: anything ambiguous returns null so
// the panel never jumps to a wrong directory.

/** Parent of an absolute path, mirroring the server's filepath.Dir semantics.
 * Returns null at the root so callers can disable the "go up" action. */
export function parentPath(path: string): string | null {
  const trimmed = path.replace(/\/+$/, "")
  if (trimmed === "" || trimmed === "/") return null
  const index = trimmed.lastIndexOf("/")
  if (index <= 0) return "/"
  return trimmed.slice(0, index)
}

/** Collapse duplicate slashes and resolve `.` / `..` lexically. Never returns empty. */
export function normalizePath(path: string): string {
  const absolute = path.startsWith("/")
  const segments = path.split("/")
  const resolved: string[] = []
  for (const segment of segments) {
    if (segment === "" || segment === ".") continue
    if (segment === "..") {
      if (resolved.length > 0) resolved.pop()
      continue
    }
    resolved.push(segment)
  }
  return absolute ? `/${resolved.join("/")}` : resolved.join("/") || "."
}

/**
 * Resolve a literal `cd` argument against the current absolute cwd. The
 * argument must already be unquoted (see parseFirstWord); surrounding quotes
 * are *data* here, not shell syntax, so they are never stripped again.
 * Returns null when the target cannot be resolved confidently: no argument,
 * `~` / `$VAR` prefixes (home expansion), `-` (previous dir).
 */
export function resolveCdTarget(current: string, argument: string): string | null {
  const arg = argument.trim()
  if (arg === "" || arg === "-" || arg.startsWith("~") || arg.startsWith("$")) return null
  if (arg.startsWith("/")) return normalizePath(arg)
  return normalizePath(`${current}/${arg}`)
}

const pwdLinePattern = /(?:^|[\r\n])(\/[^\r\n]*)(?=[\r\n])/g

/**
 * Extract the absolute path printed by the `pwd` command. The shell echoes the
 * command line itself (`...$ pwd`) before printing the result; with a prompt
 * that ends in the cwd that echoed line can *look* like an absolute path, so
 * lines containing a `pwd` word are skipped. The line must be terminated by a
 * line ending, so a path still being transmitted (split across frames) is not
 * accepted prematurely. Callers accumulate decoded frames while awaiting the
 * result so split paths still match once complete.
 */
export function detectPwdOutput(chunk: string): string | null {
  for (const match of chunk.matchAll(pwdLinePattern)) {
    const line = match[1]!.trimEnd()
    if (line === "" || line.includes(" pwd") || /(?:^|\s)\$?\s*pwd\s*$/.test(line)) continue
    return line
  }
  return null
}

/** Wrap a path in single quotes, escaping embedded single quotes (POSIX-safe). */
export function quoteForShell(path: string): string {
  return `'${path.replace(/'/g, `'\\''`)}'`
}

/**
 * Parse the first shell word of a line into its literal value, mirroring how
 * the shell echoes commands back through the PTY. A single-quoted token may
 * contain the `'\''` idiom (close quote, escaped quote, reopen quote) for a
 * literal `'`; a double-quoted or bare token may contain `\x` escapes.
 * Returns null when a QUOTED word is not terminated yet (still being
 * transmitted). A bare word at the end of a complete line is returned as-is.
 */
export function parseFirstWord(line: string): string | null {
  let i = 0
  while (i < line.length && /\s/.test(line[i]!)) i += 1
  if (i >= line.length) return ""
  const quote = line[i]
  let out = ""
  if (quote === "'") {
    i += 1
    while (i < line.length) {
      const ch = line[i]!
      if (ch === "'") {
        // `'\''` continues the word with a literal quote (as emitted by
        // quoteForShell for a path containing an apostrophe).
        if (line[i + 1] === "\\" && line[i + 2] === "'" && line[i + 3] === "'") {
          out += "'"
          i += 4
          continue
        }
        return out
      }
      out += ch
      i += 1
    }
    return null // unterminated single-quoted word
  }
  if (quote === '"') {
    i += 1
    while (i < line.length) {
      const ch = line[i]!
      if (ch === "\\") {
        out += line[i + 1] ?? "\\"
        i += 2
        continue
      }
      if (ch === '"') return out
      out += ch
      i += 1
    }
    return null // unterminated double-quoted word
  }
  while (i < line.length) {
    const ch = line[i]!
    if (ch === "\\") {
      out += line[i + 1] ?? "\\"
      i += 2
      continue
    }
    if (/\s/.test(ch)) return out
    out += ch
    i += 1
  }
  return out
}

// Recognize a complete `cd` command line typed at the shell prompt. Returns
// the raw argument (first word only semantics) or null when the line is not a
// plain `cd` (chains, redirects and other commands are ignored conservatively).
export function parseTypedCd(line: string): { argument: string | null } | null {
  const match = line.match(/^\s*cd(?:\s+(.+?))?\s*$/)
  if (!match) return null
  const argument = match[1]
  if (argument === undefined) return { argument: null }
  // `cd /tmp && ls` or `cd /x; ls`: the shell may run more than one command.
  // Only a plain `cd <target>` is tracked; anything else is left alone.
  if (/[;&|<>]/.test(argument)) return null
  return { argument }
}

/** Action produced when the typed-cd tracker closes a command line. */
export type TypedCdAction =
  | { type: "cd"; target: string }
  | { type: "probe" }
  | { type: "none" }

// Commands that assume full-screen ownership of the terminal.  When the user
// presses Enter on such a command, the tracker sets an internal blocked flag:
// subsequent probes are suppressed until the app exits (detected via mode
// frames re-entering canonical/echo).  This prevents `pwd` probes from being
// typed into vim/less/htop/top etc.
const rawAppCommands = new Set([
  "vim", "vi", "nano", "emacs",
  "less", "more", "man", "info", "tldr",
  "top", "htop", "btop", "gotop", "glances",
  "python", "python3", "ipython", "ipython3", "node",
  "ssh", "sftp", "telnet", "nc", "nmap",
  "mysql", "psql", "sqlite3", "redis-cli", "mongosh",
  "screen", "tmux", "ranger", "mc", "midnight-commander",
  "lazygit", "lazydocker", "ctop", "k9s", "kubie", "stern",
  "fzf", "fzy", "skim", "peco",
  "mutt", "neomutt", "alpine",
  "htop", "iftop", "nethogs", "bmon",
  "git", "docker", "kubectl", "helm",
  "journalctl", "systemctl",
])

/** Options for createTypedCdTracker. */
export interface TypedCdTrackerOptions {
  /** Current absolute shell cwd, read lazily when a line is submitted. */
  currentCwd: () => string
}

// Commands that never change the shell's current directory. A fully typed
// line starting with one of these is the only case where the tracker skips
// the pwd probe, keeping the terminal output quiet for routine commands.
// Everything else (cd, pushd, popd, z, autojump, aliases, unknown tools) is
// probed so the file panel always follows the shell's real directory.
const safeCommands = new Set([
  "ls", "dir", "cat", "tac", "head", "tail", "less", "more", "grep", "rg",
  "sed", "awk", "cut", "sort", "uniq", "wc", "echo", "printf", "clear", "pwd",
  "whoami", "id", "uname", "date", "cal", "hostname", "uptime", "free", "df",
  "du", "ps", "top", "htop", "env", "history", "man", "info", "which",
  "whereis", "find", "tree", "stat", "file", "readlink", "xxd", "hexdump",
  "basename", "dirname", "realpath", "tar", "gzip", "gunzip", "zip", "unzip",
  "xz", "bzip2", "cp", "mv", "rm", "mkdir", "rmdir", "touch", "chmod", "chown",
  "chgrp", "ln", "mount", "umount", "systemctl", "journalctl", "service",
  "docker", "kubectl", "git", "make", "cmake", "go", "cargo", "npm", "pnpm",
  "yarn", "npx", "node", "python", "python3", "pip", "pip3", "ruby", "gem",
  "php", "composer", "java", "mvn", "gradle", "rustc", "gcc", "clang", "cc",
  "ssh", "scp", "sftp", "rsync", "wget", "curl", "ping", "traceroute",
  "nslookup", "dig", "nc", "nmap", "ip", "ifconfig", "ss", "netstat", "route",
  "iptables", "nft", "vim", "vi", "nano", "emacs", "code", "subl", "screen",
  "tmux", "sleep", "kill", "pkill", "killall", "nohup",
])

/**
 * Stateful tracker for the INPUT stream that keeps the file panel in sync
 * with the shell cwd. The PTY echo is not scanned (fancy prompts interleave
 * escape sequences and re-echoes with the command), so the tracker follows
 * what the user actually types and asks the shell with a `pwd` probe on Enter
 * whenever the directory may have changed.
 *
 * Enter handling:
 * - a plain `cd <target>` line resolves lexically against the current cwd and
 *   still asks for a confirming probe;
 * - bare `cd`, `~` / `$VAR` / `-` targets, cd chains and lines rewritten by
 *   readline (Tab completion, arrow-key history/autosuggestion) return probe;
 * - a fully typed line whose first word is a known non-dir-change command
 *   (see safeCommands) returns none;
 * - anything else (pushd/popd, z, aliases, unknown tools) returns probe so the
 *   shell's real directory always wins.
 */
export function createTypedCdTracker(options: TypedCdTrackerOptions) {
  let typedLine = ""
  let lineRewritten = false
  let escSeq: string | null = null
  // blocked prevents probes inside a raw-mode application (vim/less/htop).
  // It is set on Enter of a rawAppCommand and cleared by clearBlocked(),
  // which the page calls when a mode frame with canonical=true arrives.
  let blocked = false

  const finishLine = (line: string, rewritten: boolean): TypedCdAction => {
    // Detect a raw-mode app entering: suppress probes for this and subsequent
    // lines until a canonical mode frame clears the flag.
    const firstWord = parseFirstWord(line)
    if (firstWord !== null && rawAppCommands.has(firstWord)) {
      blocked = true
    }
    if (blocked) {
      // When blocked, only a plain resolvable `cd <target>` is accepted
      // (optimistic update).  Ambiguous targets and probes are suppressed
      // until clearBlocked() is called — the shell may still be inside a
      // raw-mode application.
      const cd = parseTypedCd(line)
      if (cd && cd.argument !== null) {
        const argument = parseFirstWord(cd.argument)
        const target = argument === null || argument === "" ? null : resolveCdTarget(options.currentCwd(), argument)
        if (target) return { type: "cd", target }
      }
      return { type: "none" }
    }
    const cd = parseTypedCd(line)
    if (cd) {
      if (cd.argument === null) return { type: "probe" }
      const argument = parseFirstWord(cd.argument)
      const target = argument === null || argument === "" ? null : resolveCdTarget(options.currentCwd(), argument)
      return target ? { type: "cd", target } : { type: "probe" }
    }
    if (rewritten) return { type: "probe" }
    if (firstWord === "") return { type: "none" } // empty line: nothing ran
    if (firstWord !== null && safeCommands.has(firstWord)) return { type: "none" }
    return { type: "probe" }
  }

  return {
    clearBlocked() { blocked = false },
    /**
     * Feed one raw xterm.js onData chunk. Returns the action for the command
     * line closed by an Enter inside this chunk (a multi-line paste only
     * reports its last line, which is the final panel destination).
     */
    feed(data: string): TypedCdAction {
      let action: TypedCdAction = { type: "none" }
      for (const ch of data) {
        if (ch === "\r" || ch === "\n") {
          const line = typedLine
          const rewritten = lineRewritten
          typedLine = ""
          lineRewritten = false
          escSeq = null
          action = finishLine(line, rewritten)
          continue
        }
        if (ch === "\x1b") {
          escSeq = "\x1b"
          continue
        }
        if (escSeq !== null) {
          escSeq += ch
          // A CSI sequence ends on the first byte in 0x40-0x7E after the
          // introducer: arrow keys, home/end and the bracketed-paste markers
          // `\x1b[200~` / `\x1b[201~`.
          if (escSeq.length > 2 && ch >= "@" && ch <= "~") {
            if (escSeq !== "\x1b[200~" && escSeq !== "\x1b[201~") {
              // readline navigation rewrote the visible line (history,
              // autosuggestion); the executed command is no longer what the
              // tracker accumulated, so Enter must probe.
              lineRewritten = true
              typedLine = ""
            }
            escSeq = null
          }
          continue
        }
        if (ch === "\x7f") {
          typedLine = typedLine.slice(0, -1)
          continue
        }
        if (/[\x00-\x1f]/.test(ch)) {
          // Tab completion, Ctrl+U/A/E, ...: readline rewrites the line, so
          // the executed command is no longer what the tracker accumulated.
          lineRewritten = true
          typedLine = ""
          continue
        }
        typedLine += ch
        if (typedLine.length > 8192) {
          // A single line far beyond any shell/path limit: stop tracking it.
          typedLine = ""
          continue
        }
      }
      return action
    },
  }
}
