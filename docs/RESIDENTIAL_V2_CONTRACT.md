# 住宅渠道订阅与自动化契约

本文定义住宅渠道的客户端、自动化程序和管理面契约。协议处理和代理流量转发属于 Mihomo；
HX-ProxyGroup 控制面只维护 Desired State、生成配置、调用数据面并提供管理 API。

## 1. 所有权模型

供应商负责住宅网关接入和配额，渠道负责一个可发布的住宅代理服务。每个 sticky 渠道声明
`session_count: N` 后，发布 N 个名字和凭据稳定的逻辑节点，并拥有自己的订阅 share token 和
自动化 control token。不存在跨普通 Listener、跨供应商或跨渠道的全局统一客户端订阅。

```text
住宅供应商
  └── 住宅渠道
      ├── /sub/<share-token>  -> 本渠道 Clash/Mihomo 等客户端节点
      └── /ctl/<control-token> -> 本渠道声明节点池与换出口操作
```

客户端不感知供应商会话、网关节点或出口 IP 轮换。TTL 到期、空闲释放和 `next` 只改变服务端
内部映射，渠道订阅内容保持不变。

公开导出（token 即凭证）：

```http
GET /sub/<share-token>?format=clash|v2rayn|sing-box|uri
```

响应使用 `Cache-Control: no-store`。住宅 Listener 的 provisioning 凭据永远不进入渠道订阅。
管理前端只在「代理服务」页面提供“复制 Clash / Mihomo 订阅”和“复制自动化控制 URL”；
「住宅代理」页面只管理供应商、渠道配置和声明节点。

## 2. 网络入口

新建住宅渠道创建一个由控制面托管的 Mihomo Listener，并在创建时选择协议：

| 入口 | 传输 | 绑定 | 公网链路 |
| --- | --- | --- | --- |
| VLESS | WebSocket + TLS | 自动分配的 loopback 端口 | CF -> 雷池:443 -> Edge Relay -> Mihomo |
| VMess | WebSocket + TLS | 自动分配的 loopback 端口 | CF -> 雷池:443 -> Edge Relay -> Mihomo |
| Trojan | WebSocket + TLS | 自动分配的 loopback 端口 | CF -> 雷池:443 -> Edge Relay -> Mihomo |

控制面自动生成内部端口、WebSocket 路径和协议引导凭据；声明节点使用各自稳定凭据。浏览器需要
HTTP/SOCKS 时，由本机 Mihomo 消费渠道 WS 端点并落地到环回地址。新建 API 拒绝
`direct_listener`；历史直连入口仅为升级兼容保留，不会被静默删除。

## 3. 渠道与节点字段

渠道管理视图包含：

```json
{
  "session_count": 8,
  "idle_release_seconds": 0,
  "sessions": [],
  "endpoint": {"kind": "vless", "share_path": "/sub/<share-token>"},
  "subscription_url": "https://proxy.example.com/sub/<share-token>?format=clash",
  "control_path": "/ctl/<control-token>",
  "control_url": "https://proxy.example.com/ctl/<control-token>"
}
```

- `session_count`：0..64，同时受供应商 `max_concurrent_sessions` 约束；`0` 保留旧按需模式，
  不发布声明住宅节点。
- `idle_release_seconds`：`0` 表示常驻；非零值至少为 60 秒。空闲释放保留逻辑节点和凭据。
- `subscription_url` 和 `control_url` 由渠道公网端点生成，只返回给已认证管理员。
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
      "endpoints": [
        {
          "protocol": "vless",
          "transport": "ws",
          "uri": "vless://<uuid>@proxy.example.com:443?...",
          "browser_compatible": false
        }
      ],
      "proxy_url": null,
      "country_code": "US",
      "route_mode": "residential"
    }
  ]
}
```

`endpoints[]` 是协议中立的入口事实来源，按渠道选择返回 VLESS、VMess 或 Trojan WS URI。
`proxy_url` 只是向后兼容的首个浏览器兼容端点；新托管渠道通常为 `null`。浏览器自动化不能直接
消费 WS 协议 URI，应由客户端受管的本地 Mihomo 或 sing-box 落地。`GET nodes` 会更新声明节点
最后使用时间。未知、禁用、不属于 sticky 声明渠道的 token 和越界 index 统一返回 404。

OutlookRegister 的推荐配置是：

```json
{
  "proxy_rotation": {
    "control_url": "https://proxy.example.com/ctl/<control-token>",
    "required_pool_size": 4
  }
}
```

它在本地互斥租用节点，获取时调用 `next`，从 `endpoints[]` 启动短生命周期 Mihomo 环回代理并
探测出口，释放时停止本地实例并归还本地租约，不删除服务端节点。

## 5. 订阅渲染

普通 Listener 与住宅节点共用 `internal/listener` 渲染器，但每个 share token 只解析所属服务：

- Clash/Mihomo：完整配置，包含本服务的 proxy、select 组和 MATCH 规则。
- v2rayN：Base64 包裹的 URI 列表。
- sing-box：outbound JSON。
- `uri`：明文 URI 列表。

新建住宅渠道只渲染该渠道选择的 VLESS、VMess 或 Trojan WS 节点。历史渠道仍按已保存的 Listener
类型渲染，以保证升级后旧客户端不会被控制面静默切断；新 API 和前端不再提供创建明文直连入口。

## 6. 流量统计

住宅流量使用稳定的 `residential_channel` 资源维度。托管 WS Listener 映射到渠道 ID；替换供应商
节点、轮换出口 IP 或重建内部节点记录不会清零累计值。历史直连 Listener 继续归因到原渠道。
采集器每秒读取连接快照，在内存计算增量，每分钟批量写 SQLite。该数据适合趋势和使用量观察，
不作为精确计费账单。

## 7. Token 与日志安全

- `/sub/` 的 share token 允许读取客户端凭据；`/ctl/` 的 control token 还允许消耗供应商配额和
  切换路由，两者都按密码管理并只通过 HTTPS 传输。
- 两种 token 按渠道独立轮换，旧链接立即失效。
- 请求日志记录 `/sub/[redacted]`、`/ctl/[redacted]`、`/rot/[redacted]`，不记录完整 URL。
- API 客户端只能依赖 HTTP 状态码和稳定 `error.code`，不能解析错误消息字符串。

## 8. 兼容接口

`/rot/<token>/sessions/...` 保留给旧客户端，行为不变，但不再是新集成的推荐模型。新客户端使用
渠道 `/sub/` 获取稳定节点，自动化程序使用同渠道 `/ctl/` 指定节点执行 `next`。兼容接口不改变
渠道是唯一发布和控制边界的语义。
