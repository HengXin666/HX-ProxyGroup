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
