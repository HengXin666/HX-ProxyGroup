# 住宅代理客户端会话 API

本文记录 HX-ProxyGroup 的定制住宅代理会话协议。该协议只适用于「住宅代理」中的
sticky 渠道，不扩展普通 Proxy Group 或 Listener 的公共语义。

## 目标

一个住宅渠道只需要一个 Listener、一个端口和一个公共 token。多个客户端窗口使用
不同 `session_id`，服务端为每个逻辑会话签发独立的代理认证账号，并将它们路由到不同
住宅出口：

```text
同一 Listener :18088
├── hx-session-A -> 按需分配节点 A -> 出口 IP A
├── hx-session-B -> 按需分配节点 B -> 出口 IP B
├── hx-session-C -> upstream     -> 供应商配置的普通上游代理组
└── hx-session-D -> DIRECT       -> HX-ProxyGroup 服务器公网出口
```

代理流量仍由 Mihomo 转发。Go 控制面只管理会话状态、编译 `IN-USER` 规则，并在切流后
通过 Mihomo Controller 关闭该入站用户的旧连接，不复制或转发业务流量。

## 前置条件

- 渠道必须启用且模式为 `sticky`。
- 客户端仍连接渠道原有 Listener 地址。
- 供应商的 `max_concurrent_sessions` 是安全容量上限，不会预取任何 IP。
- 供应商必须配置 TTL 和 `session_expiry_policy`：`rotate` 到期换 IP，`expire` 到期终止会话。
- `DIRECT` 表示 HX-ProxyGroup 所在服务器的网络出口，不表示运行客户端的电脑本地直连。

## 会话 ID

`session_id` 由客户端生成，必须包含 4-64 个字符，只允许：

```text
A-Z a-z 0-9 - _
```

它是调用方用于关联窗口的逻辑标识，不直接作为供应商用户名，也不是访问凭证。公共
token 才是会话 API 的访问凭证。

## API

所有响应均带 `Cache-Control: no-store`。示例中的 token、账号和密码均为占位值。

### 创建或恢复会话

```http
PUT /rot/<token>/sessions/<session_id>
```

响应：

```json
{
  "session_id": "window-01",
  "proxy_username": "hx-session-0123456789abcdef01234567",
  "proxy_password": "generated-secret",
  "route_mode": "residential",
  "session_index": -1,
  "allocated_at": "2026-08-01T08:00:00Z",
  "expires_at": "2026-08-01T08:01:00Z",
  "rotate_count": 0
}
```

客户端将响应中的账号和密码写入原 Listener URL：

```text
http://<proxy_username>:<proxy_password>@proxy-host:18088
```

首次调用时才向供应商获取或生成住宅节点。同一个未过期的 `token + session_id` 重复调用
是幂等的，并返回原代理账号。密码在数据库中使用 AEAD 加密，管理 API、普通日志和状态
接口均不回显。

到期后再次调用时，`rotate` 策略保留代理账号并换一个 IP；`expire` 策略删除会话并返回
`410 session_expired`，客户端可再次调用相同 URL 建立一个新会话和新认证。

### 查询状态

```http
GET /rot/<token>/sessions/<session_id>
```

状态响应不包含 `proxy_password`。

### 单独轮换住宅出口

```http
POST /rot/<token>/sessions/<session_id>/next
```

服务端只为该逻辑会话实时获取或生成一个新住宅节点，保留客户端代理认证并关闭该会话
的旧连接。达到供应商并发上限时返回 `409 conflict`。轮换受每个逻辑会话的最小间隔限制，
过快调用返回 `429 rotate_rate_limited`。

### 切换住宅或直连

```http
POST /rot/<token>/sessions/<session_id>/route
Content-Type: application/json

{"route_mode":"upstream"}
```

`route_mode` 只允许：

- `residential`：按需分配一个住宅节点。
- `upstream`：释放住宅节点，使用住宅供应商配置的普通上游代理组；未配置时返回 422。
- `direct`：释放住宅节点，后续流量使用 HX-ProxyGroup 服务器出口。

配置应用成功后，服务端会按 Mihomo `inboundUser` 精确关闭该会话的已有连接，使新路由
对后续请求生效。页面、Cookie 和浏览器进程不需要关闭；被中断的 HTTP/2、WebSocket
或 CONNECT 隧道由客户端按自身行为重连。其他会话的连接不会被关闭。

### 释放会话

```http
DELETE /rot/<token>/sessions/<session_id>
```

释放代理账号、住宅节点和现有连接。客户端应在浏览器关闭后调用。

## 完整调用顺序

```text
1. 生成 session_id
2. PUT .../sessions/<id>
3. 使用返回的代理账号启动浏览器
4. 完成需要住宅 IP 的注册步骤
5. POST .../sessions/<id>/route {"route_mode":"upstream"}
6. 保持原页面，继续 OAuth 或其他低成本流量
7. 关闭浏览器
8. DELETE .../sessions/<id>
```

## 旧接口

以下路径仅保留旧客户端的查询/错误兼容，不再用于 sticky 渠道分配或轮换 IP：

```text
GET  /rot/<token>
POST /rot/<token>/next
```

新客户端必须使用 `/sessions/<session_id>` 接口。

## 安全边界

- `/rot/<token>/...` 位于管理员 Session 认证之外，token 必须按密码管理并定期轮换。
- 服务端请求日志统一记录为 `/rot/[redacted]`，不得记录 token 或 session 路径。
- 未知、禁用和非 sticky token 对外统一表现为 `404`，避免枚举渠道。
- Listener 可保持一个端口，但每个逻辑会话使用独立、不可预测的代理密码。
- 该接口只管理住宅渠道，不改变普通 Listener、订阅导出或通用路由 API。
