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

export interface TypedCdTrackerOptions {
  /** Current absolute shell cwd, read lazily when a line is submitted. */
  currentCwd: () => string
  /**
   * Whether the shell is currently at a line-editing prompt (canonical
   * terminal mode). `pwd` probes are only emitted at a prompt so they are
   * never typed into vim/less/htop or another raw-mode application.
   */
  atPrompt: () => boolean
}

// A line that can still become a plain `cd` command. The regex is anchored so
// `QQQQcd /x`, `ls cd /x`, ... can never match: the tracker keeps the raw line
// and only arms cd tracking when the whole line still starts with `c`/`cd`.
const cdLinePrefix = /^c(?:d(?:\s.*)?)?$/

/**
 * Stateful tracker for the INPUT stream that keeps the file panel in sync
 * with the shell cwd. The PTY echo is not scanned (fancy prompts interleave
 * escape sequences and re-echoes with the command), so the tracker follows
 * what the user actually types and asks the shell with a `pwd` probe whenever
 * it cannot resolve the final directory confidently.
 *
 * Enter handling:
 * - a plain `cd <target>` line resolves lexically against the current cwd;
 * - bare `cd`, `~` / `$VAR` / `-` targets and `cd` chains return `probe`;
 * - when the line was rewritten by readline (Tab completion, bracketed paste
 *   wrappers, arrow-key history/autosuggestion) after the user typed a `cd`
 *   prefix, the tracker returns `probe` so the shell's real directory wins.
 */
export function createTypedCdTracker(options: TypedCdTrackerOptions) {
  let typedLine = ""
  let sawCdPrefix = false
  let escSeq: string | null = null

  const probeIfAtPrompt = (): TypedCdAction => (options.atPrompt() ? { type: "probe" } : { type: "none" })

  const finishLine = (line: string, mayBeCd: boolean): TypedCdAction => {
    const cd = parseTypedCd(line)
    if (cd) {
      if (cd.argument === null) return probeIfAtPrompt()
      const argument = parseFirstWord(cd.argument)
      const target = argument === null || argument === "" ? null : resolveCdTarget(options.currentCwd(), argument)
      return target ? { type: "cd", target } : probeIfAtPrompt()
    }
    return mayBeCd ? probeIfAtPrompt() : { type: "none" }
  }

  return {
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
          const mayBeCd = sawCdPrefix
          typedLine = ""
          sawCdPrefix = false
          escSeq = null
          action = finishLine(line, mayBeCd)
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
              // autosuggestion); the completed command may still be a cd.
              sawCdPrefix = sawCdPrefix || cdLinePrefix.test(typedLine)
              typedLine = ""
            }
            escSeq = null
          }
          continue
        }
        if (ch === "\x7f") {
          typedLine = typedLine.slice(0, -1)
          sawCdPrefix = cdLinePrefix.test(typedLine)
          continue
        }
        if (/[\x00-\x1f]/.test(ch)) {
          // Tab completion, Ctrl+U/A/E, ...: readline rewrites the line, so
          // the visible command is no longer what the tracker accumulated.
          sawCdPrefix = sawCdPrefix || cdLinePrefix.test(typedLine)
          typedLine = ""
          continue
        }
        typedLine += ch
        if (typedLine.length > 8192) {
          // A single line far beyond any shell/path limit: stop tracking it.
          typedLine = ""
          sawCdPrefix = false
          continue
        }
        sawCdPrefix = cdLinePrefix.test(typedLine)
      }
      return action
    },
  }
}
