import assert from "node:assert/strict"
import test from "node:test"

import { createTypedCdTracker, detectPwdOutput, normalizePath, parentPath, parseFirstWord, quoteForShell, resolveCdTarget } from "../src/lib/terminal-cwd.ts"

test("normalizePath collapses slashes and resolves dot segments", () => {
  assert.equal(normalizePath("//home//user/"), "/home/user")
  assert.equal(normalizePath("/home/user/../docs"), "/home/docs")
  assert.equal(normalizePath("home/./docs"), "home/docs")
  assert.equal(normalizePath("/"), "/")
  assert.equal(normalizePath(""), ".")
})

test("resolveCdTarget handles absolute, relative, and ambiguous targets", () => {
  assert.equal(resolveCdTarget("/home/user", "/tmp"), "/tmp")
  assert.equal(resolveCdTarget("/home/user", "docs"), "/home/user/docs")
  assert.equal(resolveCdTarget("/home/user", ".."), "/home")
  assert.equal(resolveCdTarget("/home/user", ""), null)
  assert.equal(resolveCdTarget("/home/user", "-"), null)
  assert.equal(resolveCdTarget("/home/user", "~/docs"), null)
  assert.equal(resolveCdTarget("/home/user", "$HOME"), null)
})

test("parseFirstWord handles bare, quoted, and escaped words", () => {
  assert.equal(parseFirstWord("/tmp"), "/tmp")
  assert.equal(parseFirstWord("'/tmp/a b'"), "/tmp/a b")
  assert.equal(parseFirstWord(`'/home/It'\\''s'`), "/home/It's")
  assert.equal(parseFirstWord('"/tmp/a b"'), "/tmp/a b")
  assert.equal(parseFirstWord("'/tmp/a b"), null) // unterminated quote
  assert.equal(parseFirstWord(""), "")
  assert.equal(parseFirstWord("   "), "")
})

test("detectPwdOutput extracts the pwd result line", () => {
  assert.equal(detectPwdOutput("/home/user\r\n"), "/home/user")
  assert.equal(detectPwdOutput("user@host:~$ pwd\r\n/home/user\r\n"), "/home/user")
  assert.equal(detectPwdOutput("/home/user$ pwd\r\n/home/user\r\n"), "/home/user")
  assert.equal(detectPwdOutput("user@host:~$ pwd\r\n/\r\n"), "/")
  assert.equal(detectPwdOutput("banner text\n"), null)
  assert.equal(detectPwdOutput(""), null)
})

test("detectPwdOutput matches a path split across accumulated frames", () => {
  assert.equal(detectPwdOutput("user@host:~$ pwd\r\n/home/us"), null)
  assert.equal(detectPwdOutput("user@host:~$ pwd\r\n/home/user\r\n"), "/home/user")
})

test("quoteForShell wraps paths safely", () => {
  assert.equal(quoteForShell("/home/user"), "'/home/user'")
  assert.equal(quoteForShell("/home/It's"), `'/home/It'\\''s'`)
})

test("parentPath mirrors server filepath.Dir semantics", () => {
  assert.equal(parentPath("/"), null)
  assert.equal(parentPath("/root"), "/")
  assert.equal(parentPath("/home/user/docs"), "/home/user")
  assert.equal(parentPath("/home/user/"), "/home")
  assert.equal(parentPath(""), null)
})

test("typed cd tracker resolves plain cd lines", () => {
  let cwd = "/root"
  const tracker = createTypedCdTracker({ currentCwd: () => cwd })
  assert.deepEqual(tracker.feed("c"), { type: "none" })
  assert.deepEqual(tracker.feed("d"), { type: "none" })
  assert.deepEqual(tracker.feed(" /tmp"), { type: "none" })
  const action = tracker.feed("\r")
  assert.deepEqual(action, { type: "cd", target: "/tmp" })
  cwd = action.type === "cd" ? action.target : cwd

  assert.deepEqual(tracker.feed("cd docs\r"), { type: "cd", target: "/tmp/docs" })
  assert.deepEqual(tracker.feed("cd /var/www\r"), { type: "cd", target: "/var/www" })
})

test("typed cd tracker probes for ambiguous targets", () => {
  let cwd = "/root"
  const tracker = createTypedCdTracker({ currentCwd: () => cwd })
  assert.deepEqual(tracker.feed("cd\r"), { type: "probe" })
  assert.deepEqual(tracker.feed("cd ~/docs\r"), { type: "probe" })
  assert.deepEqual(tracker.feed("cd $HOME\r"), { type: "probe" })
  assert.deepEqual(tracker.feed("cd -\r"), { type: "probe" })
  assert.deepEqual(tracker.feed("cd /tmp && ls\r"), { type: "probe" })
})

test("typed cd tracker probes when readline rewrote the line", () => {
  let cwd = "/root"
  const tracker = createTypedCdTracker({ currentCwd: () => cwd })
  // Tab completion: the shell rewrites `cd /va` to the completed path.
  assert.deepEqual(tracker.feed("cd /va\t\r"), { type: "probe" })
  // Arrow-key autosuggestion acceptance followed by Enter.
  assert.deepEqual(tracker.feed("cd /va\x1b[C\r"), { type: "probe" })
  // Bracketed paste (xterm wraps pastes when readline enables it).
  assert.deepEqual(tracker.feed("\x1b[200~cd /tmp\x1b[201~\r"), { type: "cd", target: "/tmp" })
  // Paste with Enter inside the wrapper.
  assert.deepEqual(tracker.feed("\x1b[200~cd /var\r\x1b[201~"), { type: "cd", target: "/var" })
  // Multi-line paste reports the final line.
  assert.deepEqual(tracker.feed("ls -la\ncd /opt\n"), { type: "cd", target: "/opt" })
})

test("typed cd tracker ignores non-cd commands", () => {
  const tracker = createTypedCdTracker({ currentCwd: () => "/root" })
  assert.deepEqual(tracker.feed("ls\r"), { type: "none" })
  assert.deepEqual(tracker.feed("vim /tmp/a.txt\r"), { type: "none" })
  assert.deepEqual(tracker.feed("ls -la /var\r"), { type: "none" })
  assert.deepEqual(tracker.feed("\r"), { type: "none" }) // empty line: nothing ran
})

test("typed cd tracker probes unknown and directory-changing commands", () => {
  const tracker = createTypedCdTracker({ currentCwd: () => "/root" })
  assert.deepEqual(tracker.feed("z foo\r"), { type: "probe" })
  assert.deepEqual(tracker.feed("pushd /opt\r"), { type: "probe" })
  assert.deepEqual(tracker.feed("popd\r"), { type: "probe" })
  assert.deepEqual(tracker.feed("QQQQcd /x\r"), { type: "probe" })
  // A line rewritten by completion probes even for a safe command.
  assert.deepEqual(tracker.feed("ls\t\r"), { type: "probe" })
})

test("typed cd tracker blocks probes after a raw-mode command", () => {
  // Inside vim/less the tracker must never emit a probe: sending pwd would
  // type into the application.  The blocked flag is set on Enter of a
  // raw-app command (vim/less/top/htop/...) and cleared by clearBlocked().
  const tracker = createTypedCdTracker({ currentCwd: () => "/root" })
  // Start vim (raw-app command sets blocked).
  assert.deepEqual(tracker.feed("vim foo\r"), { type: "none" })
  // Now blocked: ambiguous cd targets should NOT probe.
  assert.deepEqual(tracker.feed("cd /va\t\r"), { type: "none" })
  assert.deepEqual(tracker.feed("cd ~\r"), { type: "none" })
  assert.deepEqual(tracker.feed("cd\r"), { type: "none" })
  // Unblock (vim exited).
  tracker.clearBlocked()
  // Now probes fire again.
  assert.deepEqual(tracker.feed("cd /tmp\r"), { type: "cd", target: "/tmp" })
  assert.deepEqual(tracker.feed("cd ~\r"), { type: "probe" })
})
test("typed cd tracker handles backspace editing", () => {
  let cwd = "/root"
  const tracker = createTypedCdTracker({ currentCwd: () => cwd })
  assert.deepEqual(tracker.feed("cd /tmp\x7f\x7f\r"), { type: "cd", target: "/t" })
  // Backspacing down to a lone `c` still looks like a possible cd prefix, so
  // the tracker probes instead of guessing.
  assert.deepEqual(tracker.feed("cd /x\x7f\x7f\x7f\r"), { type: "probe" })
})
