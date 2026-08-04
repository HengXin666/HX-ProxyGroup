const maximumPendingBytes = 4 * 1024
const encoder = new TextEncoder()

export type TerminalMode = {
  echo: boolean
  canonical: boolean
}

// PredictiveEcho renders safe command-line text immediately and removes the
// identical PTY echo when it arrives. It is deliberately disabled for control
// characters, password prompts, raw/full-screen programs, and unknown modes.
export class PredictiveEcho {
  private enabled = false
  private pending = new Uint8Array()

  setMode(mode: TerminalMode) {
    this.enabled = mode.echo && mode.canonical
  }

  reset() {
    this.enabled = false
    this.pending = new Uint8Array()
  }

  predict(data: string): string | null {
    if (!isSafeText(data)) {
      // A control input may submit a command that immediately disables PTY
      // echo without producing a prompt (for example, `read -s`). Suspend
      // prediction until the server explicitly reports the next mode.
      this.enabled = false
      return null
    }
    if (!this.enabled) return null
    const bytes = encoder.encode(data)
    if (bytes.byteLength === 0 || this.pending.byteLength + bytes.byteLength > maximumPendingBytes) {
      this.pending = new Uint8Array()
      this.enabled = false
      return null
    }
    const combined = new Uint8Array(this.pending.byteLength + bytes.byteLength)
    combined.set(this.pending)
    combined.set(bytes, this.pending.byteLength)
    this.pending = combined
    return data
  }

  consume(output: Uint8Array): Uint8Array {
    if (this.pending.byteLength === 0 || output.byteLength === 0) return output
    const comparable = Math.min(this.pending.byteLength, output.byteLength)
    let matched = 0
    while (matched < comparable && this.pending[matched] === output[matched]) matched++
    if (matched === 0) {
      this.pending = new Uint8Array()
      return output
    }
    this.pending = this.pending.slice(matched)
    if (matched < comparable) this.pending = new Uint8Array()
    return output.slice(matched)
  }
}

function isSafeText(data: string): boolean {
  if (!data) return false
  for (const character of data) {
    const codePoint = character.codePointAt(0) ?? 0
    if (codePoint < 0x20 || (codePoint >= 0x7f && codePoint <= 0x9f)) return false
  }
  return true
}

export function shouldBatchTerminalInput(data: string, predicted: string | null): boolean {
  if (predicted == null || !data || encoder.encode(data).byteLength > 1024) return false
  return isSafeText(data)
}
