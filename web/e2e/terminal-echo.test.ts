import assert from "node:assert/strict"
import test from "node:test"

import { PredictiveEcho, shouldBatchTerminalInput } from "../src/lib/terminal-echo.ts"

const bytes = (value: string) => new TextEncoder().encode(value)
const text = (value: Uint8Array) => new TextDecoder().decode(value)

test("predicts printable command input and consumes delayed PTY echo", () => {
  const echo = new PredictiveEcho()
  echo.setMode({ echo: true, canonical: true })
  assert.equal(echo.predict("status --all"), "status --all")
  assert.equal(text(echo.consume(bytes("status "))), "")
  assert.equal(text(echo.consume(bytes("--all\r\nresult\r\n"))), "\r\nresult\r\n")
})

test("never predicts passwords, raw mode, or control input", () => {
  const echo = new PredictiveEcho()
  echo.setMode({ echo: false, canonical: true })
  assert.equal(echo.predict("secret"), null)
  echo.setMode({ echo: true, canonical: false })
  assert.equal(echo.predict("vim input"), null)
  echo.setMode({ echo: true, canonical: true })
  assert.equal(echo.predict("\r"), null)
  assert.equal(echo.predict("secret-after-enter"), null)
  echo.setMode({ echo: true, canonical: true })
  assert.equal(echo.predict("\u001b[A"), null)
  assert.equal(echo.predict("text-after-arrow"), null)
})

test("keeps earlier command echo pending while control input suspends prediction", () => {
  const echo = new PredictiveEcho()
  echo.setMode({ echo: true, canonical: true })
  assert.equal(echo.predict("read -s password"), "read -s password")
  assert.equal(echo.predict("\r"), null)
  echo.setMode({ echo: false, canonical: true })
  assert.equal(text(echo.consume(bytes("read -s password"))), "")
  assert.equal(echo.predict("not-visible"), null)
})

test("batches only safe predictively echoed input", () => {
  assert.equal(shouldBatchTerminalInput("a", "a"), true)
  assert.equal(shouldBatchTerminalInput("命令", "命令"), true)
  assert.equal(shouldBatchTerminalInput("secret", null), false)
  assert.equal(shouldBatchTerminalInput("\r", null), false)
  assert.equal(shouldBatchTerminalInput("x".repeat(1025), "x".repeat(1025)), false)
})

test("falls back to server output when the echo diverges", () => {
  const echo = new PredictiveEcho()
  echo.setMode({ echo: true, canonical: true })
  assert.equal(echo.predict("abc"), "abc")
  assert.equal(text(echo.consume(bytes("xyz"))), "xyz")
  assert.equal(text(echo.consume(bytes("abc"))), "abc")
})

test("handles UTF-8 input without splitting bytes", () => {
  const echo = new PredictiveEcho()
  echo.setMode({ echo: true, canonical: true })
  assert.equal(echo.predict("你好"), "你好")
  assert.equal(text(echo.consume(bytes("你好"))), "")
})
