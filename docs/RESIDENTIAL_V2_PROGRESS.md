# 住宅渠道发布与自动化进度

> 状态时间：2026-08-05  
> 当前契约：[`RESIDENTIAL_V2_CONTRACT.md`](RESIDENTIAL_V2_CONTRACT.md)

## 1. 当前目标

住宅渠道是客户端发布与自动化控制的唯一所有权边界：

- 每个渠道独立选择 VLESS、VMess 或 Trojan over WebSocket。
- 每个渠道独立发布声明节点订阅和自动化控制 URL。
- 普通代理服务继续使用各自 Listener 的订阅，不与住宅渠道做全局聚合。
- 两类复制动作都位于「代理服务」页面；「住宅代理」页面只负责资源管理。

推荐公网拓扑：

```text
Clash / OutlookRegister 本机 Mihomo
  -> HTTPS/WSS 域名
  -> Cloudflare 橙云
  -> VPS:443 雷池
  -> HX-ProxyGroup Edge Relay
  -> Mihomo 环回 WS Listener
  -> 住宅节点
```

浏览器需要 HTTP/SOCKS 时，由 OutlookRegister 或客户端本机 Mihomo 消费渠道 WS 端点并落地为
环回代理。Go 控制面不实现协议转发。

## 2. 已完成

### 2.1 声明住宅节点

- Schema v24 增加 `session_count`、`idle_release_seconds`、独立 `control_token`、历史可选
  `direct_listener_id`、声明序号和最后使用时间。
- sticky 渠道可声明 1..N 个稳定节点；缩扩容保留仍存在节点的名称和凭据。
- 空闲释放只释放供应商分配，节点身份仍保留；下一次使用或 `next` 时重新分配。
- 每个节点可执行 `next`，并可单独切换 `residential`、`upstream` 或 `direct` 路由。

### 2.2 渠道协议与订阅

- 新渠道可选择 VLESS、VMess 或 Trojan WS；内部端口、WS 路径和引导凭据自动分配。
- 每个渠道使用自己的 `/sub/<share_token>`，只发布本渠道声明节点。
- 渠道 provisioning 凭据不会进入订阅；每个声明节点使用自己的稳定凭据。
- Schema v26 删除旧 `client_subscription_token` 元数据，旧全局管理 API 返回 404。
- 前端删除住宅页统一订阅面板，在「代理服务」行提供 Clash/Mihomo 渠道订阅复制动作。

### 2.3 自动化控制与 OutlookRegister

- `GET /ctl/<control_token>/nodes` 返回声明节点池和协议中立的 `endpoints[]`。
- `POST /ctl/<control_token>/nodes/<index>/next` 刷新指定节点背后的住宅出口。
- `POST /ctl/<control_token>/nodes/<index>/route` 切换该节点路由。
- 控制 token 与订阅 token 分离；未知、禁用和不适用的 token 返回 404，访问日志隐藏 token。
- 「代理服务」行独立提供自动化控制 URL 复制动作。
- OutlookRegister 保留 `endpoints[]`，从 VLESS/VMess/Trojan WS URI 启动短生命周期本机 Mihomo，
  只把随机环回 HTTP 代理交给 Playwright；释放租约或验证失败时停止并清理本地实例。

### 2.4 住宅流量统计

- Schema v25 增加稳定资源类型 `residential_channel`。
- 托管 WS Listener 和历史兼容入口都归因到渠道 ID；出口 IP 轮换不会重置累计值。
- 上传、下载、连接数和趋势继续由 Mihomo 连接快照采样、内存聚合并批量写 SQLite。
- 住宅页显示渠道累计流量；统计是采样观测值，不作为精确计费账单。

## 3. 已验证

- `go test ./...`：378 passed，30 packages；覆盖三种住宅 WS 协议、渠道订阅、控制端点和旧全局
  API 的 404 行为。
- `go vet ./...`、前端 TypeScript 检查和生产构建通过。
- OutlookRegister 全量测试：100 passed；覆盖三种 URI、本地 Mihomo 生命周期、端点选择与失败清理。
- 住宅渠道 Playwright E2E 通过：真实创建 VMess 渠道，校验代理服务页两个复制动作及剪贴板 URL，
  并保存桌面和移动端截图。

## 4. 仍需真实环境验收

- 使用真实住宅供应商完成：建渠道 -> 拉渠道订阅 -> 真实代理请求 -> `next` -> 再次请求 ->
  核对渠道流量。
- 分别验证 CF 橙云 + 雷池 443 的 VLESS、VMess、Trojan WS 链路；一种协议成功不能外推为三种
  全部成功，也不能外推到公网 HTTP CONNECT/SOCKS。
- 使用真实控制 URL 验证 OutlookRegister 本地 Mihomo 的出口探测与完整注册 flow。只读
  `GET /nodes` 可用于诊断；未经授权不得调用会消耗供应商配额的 `POST /next`。

未完成上述真实边界验收前，不宣称住宅供应商兼容性、精确计费、端到端吞吐或无中断升级。
