import assert from "node:assert/strict"
import test from "node:test"

import { detectPwdOutput, normalizePath, parseFirstWord, quoteForShell, resolveCdTarget } from "../src/lib/terminal-cwd.ts"

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
