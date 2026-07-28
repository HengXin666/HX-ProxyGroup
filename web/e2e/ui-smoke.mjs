import { chromium } from "playwright"

const baseURL = process.env.HX_UI_BASE_URL
const setupToken = process.env.HX_UI_SETUP_TOKEN
if (!baseURL || !setupToken) throw new Error("HX_UI_BASE_URL and HX_UI_SETUP_TOKEN are required")

const browser = await chromium.launch({ executablePath: process.env.HX_UI_CHROMIUM || "/usr/bin/chromium", headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 960 }, deviceScaleFactor: 1 })
await page.goto(baseURL, { waitUntil: "networkidle" })
await page.getByLabel("一次性初始化 Token").fill(setupToken)
await page.getByLabel("用户名").fill("ui-review")
await page.getByLabel("密码").fill("ui-review-password-2026")
await page.getByRole("button", { name: "初始化并登录" }).click()
await page.getByRole("heading", { name: "订阅" }).waitFor()

await page.getByRole("button", { name: "新建订阅" }).first().click()
await page.getByRole("dialog").waitFor()
await page.getByLabel("订阅名称").fill("Clash 文件验证")
await page.getByRole("tab", { name: "粘贴内容" }).click()
await page.getByLabel("Clash / Mihomo YAML 或分享内容").fill("proxies:\n  - name: test\n    type: ss\n    server: 192.0.2.10\n    port: 443\n    cipher: aes-128-gcm\n    password: test-only\n")
await page.getByRole("button", { name: "创建订阅" }).click()
await page.getByText("Clash 文件验证", { exact: true }).waitFor()
const subscriptionRow = page.getByRole("row").filter({ hasText: "Clash 文件验证" })
await subscriptionRow.getByRole("button", { name: "刷新" }).click()
await subscriptionRow.getByText("1 个节点").waitFor()
await page.getByText("Clash 文件验证", { exact: true }).click()
await page.getByRole("heading", { name: "查看与编辑订阅" }).waitFor()
await page.getByText("替换加密来源").waitFor()
await page.screenshot({ path: "../docs/screenshots/subscription-editor-desktop.png", fullPage: true })
await page.getByRole("button", { name: "取消" }).click()

await page.goto(`${baseURL}/#/routing`, { waitUntil: "networkidle" })
await page.getByRole("heading", { name: "代理服务" }).waitFor()
await page.getByLabel("服务名称").fill("本机订阅验证")
await page.getByLabel("本地端口").fill("17891")
await page.getByRole("button", { name: "创建并启动" }).click()
await page.getByText("本机订阅验证", { exact: true }).waitFor()

const listener = await page.evaluate(async () => {
  const response = await fetch("/api/v1/listeners")
  const body = await response.json()
  return body.items.find((item) => item.name.includes("本机订阅验证"))
})
if (!listener?.share_path) throw new Error("created listener has no share path")

const results = await page.evaluate(async (sharePath) => {
  const request = async (format, userAgent) => {
    const response = await fetch(`${sharePath}?format=${format}`, { headers: { "User-Agent": userAgent } })
    return { status: response.status, type: response.headers.get("content-type"), format: response.headers.get("x-hx-subscription-format"), body: await response.text() }
  }
  return {
    clash: await request("clash", "Clash-Verge/v2.4.2"),
    v2rayn: await request("v2rayn", "v2rayN/7"),
    shadowrocket: await request("v2rayn", "Shadowrocket/2"),
    singbox: await request("sing-box", "sing-box/1.11"),
  }
}, listener.share_path)
if (results.clash.status !== 200 || results.clash.format !== "clash" || !results.clash.body.includes("proxies:")) throw new Error("Clash localhost subscription failed")
if (results.v2rayn.status !== 200 || results.v2rayn.format !== "v2rayn" || !Buffer.from(results.v2rayn.body, "base64").toString().includes("127.0.0.1:17891")) throw new Error("v2rayN localhost subscription failed")
if (results.shadowrocket.status !== 200 || results.shadowrocket.format !== "v2rayn" || !Buffer.from(results.shadowrocket.body, "base64").toString().includes("127.0.0.1:17891")) throw new Error("Shadowrocket localhost subscription failed")
if (results.singbox.status !== 200 || results.singbox.format !== "sing-box" || JSON.parse(results.singbox.body).outbounds.length === 0) throw new Error("sing-box localhost subscription failed")

await page.getByText("本机订阅验证", { exact: true }).click()
await page.getByText("DIRECT（当前服务器出口）").waitFor()
await page.getByRole("tab", { name: "动态编辑" }).click()
const editPanel = page.getByRole("tabpanel", { name: "动态编辑" })
await editPanel.getByText("实时编辑服务").waitFor()
await editPanel.getByLabel("服务名称").fill("本机代理服务已编辑")
await editPanel.getByRole("button", { name: "保存并应用" }).click()
await page.getByText("本机代理服务已编辑", { exact: true }).waitFor()

const serviceAction = page.getByRole("combobox").filter({ hasText: "服务操作" })
await serviceAction.click()
await page.getByRole("option", { name: "复制入口地址" }).click()
await page.getByText("入口地址已复制").waitFor()
await serviceAction.getByText("服务操作").waitFor()

await page.getByRole("tab", { name: "流量统计" }).click()
await page.getByText("最近 24 小时").waitFor()
await page.screenshot({ path: "../docs/screenshots/proxy-service-traffic-desktop.png", fullPage: true })
await page.getByRole("tab", { name: "实时日志" }).click()
await page.getByText("实时连接").waitFor({ timeout: 10_000 })
await page.screenshot({ path: "../docs/screenshots/proxy-service-logs-desktop.png", fullPage: true })
await page.getByRole("tab", { name: "节点成员" }).click()
await page.screenshot({ path: "../docs/screenshots/proxy-services-expanded-desktop.png", fullPage: true })

await page.goto(`${baseURL}/#/nodes`, { waitUntil: "networkidle" })
await page.getByRole("heading", { name: "节点" }).waitFor()
await page.getByRole("button", { name: /Clash 文件验证/ }).waitFor()
await page.getByText("test", { exact: true }).waitFor()
await page.screenshot({ path: "../docs/screenshots/nodes-by-subscription-desktop.png", fullPage: true })

await page.setViewportSize({ width: 390, height: 844 })
await page.goto(`${baseURL}/#/routing`, { waitUntil: "networkidle" })
await page.getByRole("heading", { name: "代理服务" }).waitFor()
await page.getByText("本机代理服务已编辑", { exact: true }).click()
await page.getByText("DIRECT（当前服务器出口）").waitFor()
await page.screenshot({ path: "../docs/screenshots/proxy-services-expanded-mobile.png", fullPage: true })
await browser.close()
