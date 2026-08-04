# 住宅代理与统一订阅契约（0.2.1）

本文定义 0.2.1 的客户端、自动化程序和管理面契约。协议处理和代理流量转发属于 Mihomo；
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

新建住宅渠道拥有一个由控制面托管的 Mihomo Listener：

| 入口 | 支持协议 | 绑定 | 可用链路 |
| --- | --- | --- | --- |
| VLESS WS | VLESS over WebSocket | 自动分配的 loopback 端口 | CF -> 雷池:443 -> Edge Relay -> Mihomo |

控制面自动生成内部端口、WebSocket 路径和引导 UUID；客户端凭据来自声明节点，不暴露内部状态。
浏览器需要 HTTP/SOCKS 时，由本机 Mihomo 消费 VLESS WS 端点并落地到环回地址。新建 API 拒绝
`direct_listener`；历史直连入口仅为升级兼容保留，不会被静默删除。

## 3. 渠道与节点字段

渠道管理视图增加：

```json
{
  "session_count": 8,
  "idle_release_seconds": 0,
  "sessions": [],
  "subscription_url": "https://proxy.example.com/sub/<share-token>?format=clash",
  "share_path": "/sub/<share-token>",
  "control_path": "/ctl/<control-token>"
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
      "endpoints": [
        {
          "protocol": "vless",
          "transport": "ws",
          "uri": "vless://<uuid>@proxy.example.com:443?...",
          "browser_compatible": false
        },
        {
          "protocol": "http",
          "transport": "tcp",
          "uri": "http://user:password@203.0.113.10:18443",
          "browser_compatible": true
        }
      ],
      "proxy_url": "http://user:password@203.0.113.10:18443",
      "country_code": "US",
      "route_mode": "residential"
    }
  ]
}
```

`endpoints[]` 是协议中立的入口事实来源；`proxy_url` 只是向后兼容的首个浏览器兼容端点。
托管渠道的 `proxy_url` 为 `null`，但仍返回 VLESS WS URI。浏览器自动化不能直接消费该 URI，
应由客户端受管的本地 Mihomo 或 sing-box 落地。`GET nodes` 会更新声明节点最后使用时间。
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

新建住宅渠道只渲染 VLESS WS 节点。历史渠道仍按已保存的 Listener 类型渲染，以保证升级后
旧客户端不会被控制面静默切断；新 API 和前端不再提供创建明文直连入口的能力。

## 6. 流量统计

住宅流量使用稳定的 `residential_channel` 资源维度。托管 VLESS WS Listener 映射到渠道 ID；
替换供应商节点、轮换出口 IP 或重建内部节点记录不会清零累计值。历史直连 Listener 继续归因到原渠道。
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
