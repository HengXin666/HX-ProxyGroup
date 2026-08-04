# 住宅代理客户端与自动化 API

新客户端使用稳定节点模型：客户端通过 `/sub/` 获取节点，自动化程序通过 `/ctl/` 指定节点
轮换出口。供应商会话和住宅 IP 是 HX-ProxyGroup 内部实现，不是客户端生命周期。

完整字段契约见 [`RESIDENTIAL_V2_CONTRACT.md`](RESIDENTIAL_V2_CONTRACT.md)。

## 统一订阅

管理员读取或轮换全局统一订阅：

```http
GET  /api/v1/client-subscription
POST /api/v1/client-subscription
Content-Type: application/json

{"action":"rotate"}
```

客户端导入：

```http
GET /sub/<share-token>?format=clash|v2rayn|sing-box|uri
```

统一订阅聚合普通 Listener 和住宅声明节点。住宅节点名称与凭据稳定，调用 `next` 后不需要
重新拉取订阅。响应带 `Cache-Control: no-store`；share token 可以读取代理凭据，必须按密码管理。

## 声明节点控制

sticky 渠道设置 `session_count` 后，服务端持久化 `s01..sNN` 逻辑节点。管理员 Session API：

```http
POST /api/v1/residential/channels/<channel-id>/sessions/<index>/next
POST /api/v1/residential/channels/<channel-id>/rotate-share
POST /api/v1/residential/channels/<channel-id>/rotate-control
```

供 OutlookRegister 等程序使用的 control-token API：

```http
GET  /ctl/<control-token>/nodes
POST /ctl/<control-token>/nodes/<index>/next
POST /ctl/<control-token>/nodes/<index>/route
Content-Type: application/json

{"route_mode":"residential|upstream|direct"}
```

`next` 只替换指定逻辑节点背后的住宅出口，保留客户端节点名称和认证，并通过 Mihomo Controller
关闭该入站用户的旧连接。其他节点和连接不受影响。轮换过快返回 429，供应商容量耗尽返回 409。

节点列表不向普通订阅用户暴露出口 IP。`/ctl/` 返回的 `exit_ip` 也可能为空，因为读取列表不会
为了填充字段发起 N 个外部探测；调用方需要真实 IP 时应从该节点的数据面代理执行出口探测。

## 数据面入口

- VLESS/VMess/Trojan over WebSocket 可经过 CF 橙云、雷池 443 和 HX Edge Relay。
- HTTP CONNECT、SOCKS5、Mixed 不能经过这条七层链路，必须使用显式直连 TCP Listener，
  或由客户端本机 Mihomo 消费 WS 订阅后落地。
- `/sub/` 和 `/ctl/` 是 HTTPS 控制/配置地址，不是 Playwright 的代理地址。
- `/ctl/` 的 `proxy_url` 只在启用 HTTP/SOCKS/Mixed 直连入口时返回；纯 WS 渠道返回 `null`
  和配置提示。

代理流量始终由 Mihomo 转发。Go 控制面只维护映射、编译 `IN-USER` 规则、应用候选配置并调用
Mihomo Controller，不实现 HTTP CONNECT、SOCKS5、VLESS、VMess 或 Trojan 协议。

## OutlookRegister 调用顺序

```text
1. GET /ctl/<token>/nodes，读取固定节点池
2. 在 OutlookRegister 进程内互斥租用一个空闲 index
3. POST /nodes/<index>/next，刷新住宅出口
4. 使用返回的 proxy_url 探测出口身份并运行完整 flow
5. flow 结束后只归还本地 index，不删除服务端节点
```

渠道 `session_count` 和供应商 `max_concurrent_sessions` 都应不小于业务并发数。控制 token 可执行
轮换并消耗供应商配额，权限高于只读订阅 token，应独立轮换且不得进入截图或日志。

## 兼容 `/rot/` 接口

以下旧接口继续服务已有集成，但新代码不得依赖它们创建临时服务端会话：

```text
GET|POST /rot/<token>
PUT|GET|DELETE /rot/<token>/sessions/<session-id>
POST /rot/<token>/sessions/<session-id>/next
POST /rot/<token>/sessions/<session-id>/route
GET  /rot/<token>/sessions/<session-id>/config?format=clash
```

旧 token、会话凭据和响应均不得写入日志。访问日志统一脱敏为 `/rot/[redacted]`；未知、禁用
或不适用的 token 对外统一为 404。旧接口的存在不改变 `/sub/` + `/ctl/` 作为推荐模型的结论。
