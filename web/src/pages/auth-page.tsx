import { useState, type FormEvent } from "react"
import { KeyRound, Network, ShieldCheck } from "lucide-react"

import { ApiError, api } from "@/lib/api"

export function AuthPage({
  configured,
  onAuthenticated,
}: {
  configured: boolean
  onAuthenticated: () => void
}) {
  const [setupToken, setSetupToken] = useState("")
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      if (!configured) {
        await api.authSetup(setupToken.trim(), username.trim(), password)
      }
      await api.login(username.trim(), password)
      onAuthenticated()
    } catch (cause) {
      if (cause instanceof ApiError) {
        setError(cause.message)
      } else {
        setError("请求失败，请稍后重试")
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="mb-5 flex items-center justify-center gap-2.5">
          <div className="flex size-9 items-center justify-center rounded-md bg-[#24292f] text-white">
            <Network className="size-5" />
          </div>
          <div>
            <div className="text-base font-semibold leading-5">HX-ProxyGroup</div>
            <div className="text-xs text-muted-foreground">Control Plane</div>
          </div>
        </div>

        <form
          onSubmit={submit}
          className="space-y-3 rounded-md border bg-white p-4 shadow-[0_1px_0_rgba(31,35,40,0.04)]"
        >
          <div className="flex items-center gap-2 text-sm font-medium">
            {configured ? (
              <KeyRound className="size-4 text-[#0969da]" />
            ) : (
              <ShieldCheck className="size-4 text-[#1a7f37]" />
            )}
            {configured ? "管理员登录" : "初始化管理员账户"}
          </div>

          {!configured && (
            <>
              <p className="text-xs leading-5 text-muted-foreground">
                服务尚未配置管理员。请粘贴数据目录中 <code className="rounded bg-[#f6f8fa] px-1">admin-setup-token</code>{" "}
                文件的内容完成初始化。初始化前管理 API 仅监听环回地址。
              </p>
              <label className="block text-xs font-medium">
                一次性初始化 Token
                <input
                  type="password"
                  value={setupToken}
                  onChange={(event) => setSetupToken(event.target.value)}
                  required
                  autoComplete="off"
                  className="mt-1 w-full rounded-md border px-2.5 py-1.5 text-sm focus:border-[#0969da] focus:outline-none"
                />
              </label>
            </>
          )}

          <label className="block text-xs font-medium">
            用户名
            <input
              type="text"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              required
              minLength={3}
              maxLength={64}
              autoComplete="username"
              className="mt-1 w-full rounded-md border px-2.5 py-1.5 text-sm focus:border-[#0969da] focus:outline-none"
            />
          </label>

          <label className="block text-xs font-medium">
            密码
            <input
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              required
              minLength={configured ? 1 : 10}
              maxLength={128}
              autoComplete={configured ? "current-password" : "new-password"}
              className="mt-1 w-full rounded-md border px-2.5 py-1.5 text-sm focus:border-[#0969da] focus:outline-none"
            />
          </label>
          {!configured && (
            <p className="text-[11px] text-muted-foreground">密码长度需在 10 到 128 个字符之间。</p>
          )}

          {error && (
            <div className="rounded-md border border-[#ff8182] bg-[#ffebe9] px-2.5 py-2 text-xs text-[#a40e26]">
              {error}
            </div>
          )}

          <button
            type="submit"
            disabled={busy}
            className="w-full rounded-md bg-[#1f883d] px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-[#1a7f37] disabled:opacity-60"
          >
            {busy ? "处理中…" : configured ? "登录" : "初始化并登录"}
          </button>
        </form>
      </div>
    </div>
  )
}
