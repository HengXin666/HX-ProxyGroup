# 住宅代理与统一订阅契约（0.2.0）

本文定义 0.2.0 的客户端、自动化程序和管理面契约。协议处理和代理流量转发属于 Mihomo；
HX-ProxyGroup 控制面只维护 Desired State、生成配置、调用数据面并提供管理 API。

## 1. 客户端模型

统一订阅同时发布普通 Listener 和住宅渠道。住宅 sticky 渠道声明 `session_count: N` 后，
发布 N 个名字和凭据稳定的逻辑节点。客户端不感知供应商会话、网关节点或出口 IP 轮换；
TTL 到期、空闲释放和 `next` 都只改变服务端内部映射，订阅内容保持不变。

```text
普通 Listener -----------+
机场订阅 -> Proxy Group -+-> 统一 /sub/<token> -> Clash/v2rayN/sing-box
住宅渠道 -> N 个逻辑节点-+
```

统一订阅管理 API（管理员认证）：

```http
GET  /api/v1/client-subscription
POST /api/v1/client-subscription
Content-Type: application/json

{"action":"rotate"}
```

公开导出（token 即凭证）：

```http
GET /sub/<token>?format=clash|v2rayn|sing-box|uri
```

响应使用 `Cache-Control: no-store`。住宅渠道自己的 share token 也可导出该渠道节点；统一 token
则聚合所有可发布入口。住宅 Listener 的 provisioning 凭据永远不进入任一客户端订阅。

## 2. 网络入口

一个住宅渠道最多同时拥有两个 Mihomo Listener，共用同一组逻辑节点和住宅出口映射：

| 入口 | 支持协议 | 默认绑定 | 可用链路 |
| --- | --- | --- | --- |
| WS 入口 | VLESS/VMess/Trojan over WebSocket | loopback | CF -> 雷池:443 -> Edge Relay -> Mihomo |
| 直连入口 | HTTP/SOCKS5/Mixed | 显式地址和端口 | 客户端直接连接 VPS TCP 端口 |

七层 HTTPS 反向代理不能承载普通 HTTP CONNECT 或 SOCKS5。需要这些协议时，必须显式配置
带用户名密码的直连入口，或在客户端本机运行 Mihomo 消费 WS 订阅并落地为本机端口。
`direct_listener` 绕过 CF/WAF 并暴露源站，前端必须显示此风险；服务端不会默认创建公网入口。

## 3. 渠道与节点字段

渠道管理视图增加：

```json
{
  "session_count": 8,
  "idle_release_seconds": 0,
  "sessions": [],
  "subscription_url": "https://proxy.example.com/sub/<share-token>?format=clash",
  "share_path": "/sub/<share-token>",
  "control_path": "/ctl/<control-token>",
  "direct_endpoint": {
    "kind": "mixed",
    "bind_address": "203.0.113.10",
    "port": 18443,
    "auth_enabled": true
  }
}
```

- `session_count`：0..64，同时受供应商 `max_concurrent_sessions` 约束；`0` 保留旧按需模式，
  不发布声明住宅节点。
- `idle_release_seconds`：`0` 表示常驻；非零值至少为 60 秒。空闲释放保留逻辑节点和凭据。
- `share_path`、`control_path` 和完整 URL 只返回给已认证管理员。
- 管理端 `sessions` 不包含代理账号或密码；`exit_ip` 当前可为空，不为列表读取发起 N 次探测。

声明节点管理 API（管理员认证）：

```http
POST /api/v1/residential/channels/<id>/sessions/<index>/next
POST /api/v1/residential/channels/<id>/rotate-share
POST /api/v1/residential/channels/<id>/rotate-control
```

`next` 保留节点名称和凭据，替换住宅出口并关闭该入站用户的旧连接。供应商容量耗尽返回
`409 conflict`；轮换过快返回 `429 rotate_rate_limited`。

## 4. 自动化控制接口

`control_token` 供 OutlookRegister 等自动化程序使用，与订阅 share token 分离：

```http
GET  /ctl/<control-token>/nodes
POST /ctl/<control-token>/nodes/<index>/next
POST /ctl/<control-token>/nodes/<index>/route
Content-Type: application/json

{"route_mode":"residential|upstream|direct"}
```

节点列表示例：

```json
{
  "channel": "住宅美国",
  "nodes": [
    {
      "index": 1,
      "node_name": "住宅美国-01",
      "proxy_url": "http://user:password@203.0.113.10:18443",
      "country_code": "US",
      "route_mode": "residential"
    }
  ]
}
```

只有启用的 HTTP/SOCKS/Mixed 直连入口才返回 `proxy_url`。纯 WS 渠道返回 `null` 和 `hint`；
浏览器自动化不能直接消费 VLESS/VMess/Trojan WS。`GET nodes` 会更新声明节点最后使用时间。
未知、禁用、不属于 sticky 声明渠道的 token 和越界 index 统一返回 404。

OutlookRegister 的推荐配置是：

```json
{
  "proxy_rotation": {
    "control_url": "https://proxy.example.com/ctl/<control-token>",
    "required_pool_size": 4
  }
}
```

它在本地互斥租用节点，获取时调用 `next` 并探测出口，释放时只清理本地租约，不删除服务端节点。

## 5. 订阅渲染

普通 Listener 与住宅节点共用 `internal/listener` 渲染器：

- Clash/Mihomo：完整配置，包含全部 proxy、select 组和 MATCH 规则。
- v2rayN：Base64 包裹的 URI 列表。
- sing-box：outbound JSON。
- `uri`：明文 URI 列表。

WS 与直连入口同时存在时，节点名分别追加 `-ws`、`-direct`。Mixed 入口会产生 HTTP 和
SOCKS5 两个 URI。最终协议集合以打包的 Mihomo 版本和候选配置校验结果为准。

## 6. 流量统计

住宅流量使用稳定的 `residential_channel` 资源维度。Mihomo 的主 WS Listener 和可选直连
Listener 都映射到同一渠道 ID；替换供应商节点、轮换出口 IP 或重建内部节点记录不会清零累计值。
采集器每秒读取连接快照，在内存计算增量，每分钟批量写 SQLite。该数据适合趋势和使用量观察，
不作为精确计费账单。

## 7. Token 与日志安全

- `/sub/` 的 share token 允许读取客户端凭据；`/ctl/` 的 control token 还允许消耗供应商
  配额和切换路由，两者都按密码管理并只通过 HTTPS 传输。
- 两种 token 可独立轮换，旧链接立即失效。
- 请求日志记录 `/sub/[redacted]`、`/ctl/[redacted]`、`/rot/[redacted]`，不记录完整 URL。
- API 客户端只能依赖 HTTP 状态码和稳定 `error.code`，不能解析错误消息字符串。

## 8. 兼容接口

`/rot/<token>/sessions/...` 保留给旧客户端，行为不变，但不再是新集成的推荐模型。新客户端使用
`/sub/` 获取稳定节点，自动化程序使用 `/ctl/` 指定节点执行 `next`。兼容接口不代表未来的
供应商会话模型，也不得重新进入统一订阅的公开语义。
