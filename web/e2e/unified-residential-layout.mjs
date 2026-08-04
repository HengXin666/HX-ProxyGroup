import { chromium } from "playwright"

const baseURL = process.env.HX_UI_BASE_URL
const setupToken = process.env.HX_UI_SETUP_TOKEN
if (!baseURL || !setupToken) throw new Error("HX_UI_BASE_URL and HX_UI_SETUP_TOKEN are required")

const browser = await chromium.launch({
  executablePath: process.env.HX_UI_CHROMIUM || "/usr/bin/chromium",
  headless: true,
})
const page = await browser.newPage({ viewport: { width: 1440, height: 960 }, deviceScaleFactor: 1 })

async function assertNoPageOverflow(label) {
  const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }))
  if (dimensions.scrollWidth > dimensions.clientWidth) {
    throw new Error(`${label} overflows horizontally: ${JSON.stringify(dimensions)}`)
  }
}

await page.goto(baseURL, { waitUntil: "networkidle" })
const setupField = page.getByLabel("一次性初始化 Token")
if (await setupField.isVisible()) {
  await setupField.fill(setupToken)
  await page.getByLabel("用户名").fill("unified-layout")
  await page.getByLabel("密码").fill("unified-layout-password-2026")
  await page.getByRole("button", { name: "初始化并登录" }).click()
} else {
  await page.getByLabel("用户名").fill("unified-layout")
  await page.getByLabel("密码").fill("unified-layout-password-2026")
  await page.getByRole("button", { name: "登录", exact: true }).click()
}
await page.getByRole("heading", { name: "总览" }).waitFor()

await page.goto(`${baseURL}/#/residential`, { waitUntil: "networkidle" })
await page.getByRole("heading", { name: "住宅代理" }).waitFor()
await page.getByRole("heading", { name: "统一客户端订阅" }).waitFor()
await page.getByText(/个入口节点/).waitFor()
await assertNoPageOverflow("unified residential desktop")
await page.screenshot({ path: "../docs/screenshots/unified-subscription-desktop.png", fullPage: true })
await page.getByRole("button", { name: "新建渠道" }).click()
await page.getByRole("heading", { name: "新建渠道" }).waitFor()
await page.getByLabel("节点数量").waitFor()
await page.getByText("VLESS over WebSocket · TLS", { exact: true }).waitFor()
await page.getByLabel("Cloudflare / 雷池域名").waitFor()
await assertNoPageOverflow("residential channel desktop")
await page.screenshot({ path: "../docs/screenshots/residential-unified-channel-desktop.png", fullPage: true })

await page.getByRole("button", { name: "取消" }).click()
await page.route("**/api/v1/system/info", async (route) => {
  const response = await route.fetch()
  const body = await response.json()
  await route.fulfill({ response, json: { ...body, automatic_update: true } })
})
await page.route("**/api/v1/auth/2fa/status", (route) => route.fulfill({
  status: 200,
  contentType: "application/json",
  body: JSON.stringify({ configured: true, enabled: true, verified: false, verification_ttl_seconds: 900 }),
}))
await page.goto(`${baseURL}/#/about`, { waitUntil: "networkidle" })
await page.getByRole("button", { name: "更新至最新版" }).waitFor()
await page.getByLabel("2FA 验证码").waitFor()
if (await page.getByRole("button", { name: "更新至最新版" }).isEnabled()) throw new Error("update must stay disabled before 2FA step-up")
await assertNoPageOverflow("about automatic update desktop")
await page.screenshot({ path: "../docs/screenshots/about-automatic-update-desktop.png", fullPage: true })

await page.setViewportSize({ width: 390, height: 844 })
await page.goto(`${baseURL}/#/residential`, { waitUntil: "networkidle" })
await page.getByRole("heading", { name: "统一客户端订阅" }).waitFor()
await page.getByText(/个入口节点/).waitFor()
await assertNoPageOverflow("unified subscription mobile")
await page.screenshot({ path: "../docs/screenshots/unified-subscription-mobile.png", fullPage: true })

await browser.close()
