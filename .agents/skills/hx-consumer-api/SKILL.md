---
name: hx-consumer-api
description: 把 HX-ProxyGroup 的节点接进你自己的程序：拉取冻结的节点清单 JSON（GET /nodes/<share-token>），拿到每个节点的地址、凭据、协议与传输方式，用于 HTTP/SOCKS 直拨或本机内核落地；同时说明控制面不做哪些事（探测、选点、轮询、落地端口）。Use when 需要「用 HX-ProxyGroup 的代理跑自动化流量」——注册机、爬虫、浏览器自动化（Playwright/Selenium）、多租户服务要接 HX 的节点；或该程序要写「代理接入 / 节点清单 / 代理池」相关代码；或遇到 HX 订阅与节点对接、share token、协议不兼容、ws 节点不能直拨等问题时。
---

# HX-ProxyGroup 程序内接入

> **本文件的词元（端点、字段名、protocol 枚举、错误码）受
> `scripts/verify-consumer-contract.ts` 门禁约束，与
> `docs/CONSUMER_INTEGRATION_CONTRACT.md` 逐字对齐。**
> 两者不一致时，**以契约文档为准**，并把差异报给控制面维护者 —— 你的副本可能已经过期。

## 1. 你只需要一个请求

```text
GET /nodes/<share-token>
```

- `token` 就是订阅链接 `/sub/<share-token>` 里的那一段；从控制面管理员那里拿，和订阅是同一个凭据。
- 这是**公开端点**：不需要登录、不需要 Cookie、不需要 CSRF。token 就是全部凭证。
- 只支持 `GET`。其他方法返回 405。
- 响应 `Cache-Control: no-store`，**别缓存**。
- 走 HTTPS。token 等价于密码，不要写进日志、截图、公开仓库。

### 返回什么

```json
{
  "name": "香港专线",
  "share_path": "/sub/8f3a...c1",
  "subscription_urls": {
    "clash": "/sub/8f3a...c1?format=clash",
    "v2rayn": "/sub/8f3a...c1?format=v2rayn",
    "sing-box": "/sub/8f3a...c1?format=sing-box",
    "uri": "/sub/8f3a...c1?format=uri"
  },
  "nodes": [
    {
      "name": "香港专线-01",
      "protocol": "mixed",
      "host": "proxy.example.com",
      "port": 7890,
      "auth": { "username": "svc-3f9c", "password": "…" },
      "transport": "tcp",
      "tls": true,
      "browser_compatible": true,
      "uri": "http://svc-3f9c:…@proxy.example.com:7890#香港专线-01"
    },
    {
      "name": "香港专线-02",
      "protocol": "vless",
      "host": "proxy.example.com",
      "port": 443,
      "auth": { "password": "…" },
      "transport": "ws",
      "ws_path": "/__hx-proxy__/shared",
      "tls": true,
      "server_name": "proxy.example.com",
      "browser_compatible": false,
      "uri": "vless://…@proxy.example.com:443?security=tls&type=ws&…"
    }
  ]
}
```

`nodes[]` 的顺序**是稳定的**：同一 token 连续两次请求，顺序逐字节一致。你可以依赖它做分片或轮转。

## 2. 字段速查

| 字段 | 取值 | 备注 |
| --- | --- | --- |
| `name` | string | 服务/渠道显示名，长期不变 |
| `share_path` | string | `/sub/<token>` |
| `subscription_urls` | object | 固定四个键：`clash` `v2rayn` `sing-box` `uri`，相对路径 |
| `nodes[].name` | string | 节点显示名；**轮换住宅出口后也不变** |
| `nodes[].protocol` | enum | `mixed` `http` `socks` `vless` `vmess` `trojan`。**遇到别的值就报错，别猜** |
| `nodes[].host` / `.port` | string / int | 你真正要连的地址 |
| `nodes[].auth` | object \| null | `username` / `password` |
| `nodes[].transport` | enum | `tcp` 或 `ws` |
| `nodes[].ws_path` | string | 仅 `transport=ws`；已规范化，原样使用，别改前缀 |
| `nodes[].tls` | bool | |
| `nodes[].server_name` | string | 仅 `tls=true`；SNI / Host |
| `nodes[].browser_compatible` | bool | `ws` 恒为 false |
| `nodes[].uri` | string | 单节点分享 URI，可直接塞进已有配置 |

## 3. 三种用法，按你的场景挑一种

### ① 直接拨号（HTTP / SOCKS）—— 最省事

`protocol` 是 `mixed` / `http` / `socks` 时，`host:port` + `auth` 就能直接用：

```text
Playwright   --proxy-server=http://user:pass@host:port
curl         -x http://user:pass@host:port          # mixed 也接受 socks5://
HttpClient   Proxy = new WebProxy("http://host:port") { Credentials = ... }
```

`browser_compatible: true` 说的就是这条路走得通。

### ② 逐节点落地（自己起内核）

遍历 `nodes[]`，把每个节点写进你自己的 Mihomo / sing-box 配置。
`transport=ws` 的节点由**你的内核**负责 WS 隧道 —— 控制面只给地址、路径和凭据。

### ③ 整段导入（连内核配置都不想写）

把 `subscription_urls.clash`（或 `sing-box`）拼上控制面 host 直接喂给内核。

> `transport=ws` 的节点**不能**当作 `--proxy-server` 使用：浏览器不认 WebSocket 代理。
> 必须先用内核把它落地成本地 HTTP/SOCKS 端口，见用法 ②③。

## 4. 控制面**不会**替你做的事

这不是能力缺失，是职责边界。以下全部由**你**负责：

| 你要自己做的 | 为什么 |
| --- | --- |
| 探测哪些节点能用、延迟多少、出口 IP 是什么 | 控制面不知道你要访问哪个目标站，"好节点"是目标相关的 |
| 选点、排序、轮转节奏 | 你的业务决定 |
| 重试与退避 | 你的容错策略 |
| 在本地起内核 / 落地端口 | 控制面不得进入数据转发路径 |

**唯一例外**是住宅渠道：出口 IP 只有服务器能换（供应商凭据在服务器上），所以换出口必须走
`POST /ctl/<control-token>/nodes/<index>/next`。那是一个**你主动调用**的接口，控制面不会
替你建立后台轮换任务，也不会告诉你"该换了"。完整的并发租约模型（`claim` / `heartbeat` /
`release` + `expected_alloc_version` CAS）见控制面仓库的
`docs/RESIDENTIAL_INTEGRATION_STANDARD.md`。

## 5. 错误处理

只看 HTTP 状态码，**不要解析错误消息文本**。

| 状态 | 含义 | 你该怎么做 |
| --- | --- | --- |
| 200 | 成功 | 用 §1 的 JSON |
| 404 | token 未知 / 已禁用 / 不属于任何可导出的服务 | 检查凭据。**不要重试循环** —— 这不是"稍后可能好" |
| 405 | 用了非 GET | 改客户端 |
| 500 | 控制面内部失败 | 指数退避后重试 |

## 6. 参考实现（复制即用）

```python
import json, urllib.request

def fetch_nodes(base_url: str, token: str):
    """返回 (name, nodes)。失败抛 HTTPError，按状态码分支处理。"""
    request = urllib.request.Request(
        f"{base_url}/nodes/{token}",
        headers={"Accept": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        payload = json.load(response)
    known = {"mixed", "http", "socks", "vless", "vmess", "trojan"}
    for node in payload["nodes"]:
        if node["protocol"] not in known:
            raise ValueError(f"unknown protocol {node['protocol']!r}; contract changed?")
    return payload["name"], payload["nodes"]

def direct_dialers(nodes):
    """只挑能直接喂给浏览器/HTTP 客户端的节点。"""
    return [n for n in nodes if n["transport"] == "tcp" and n["browser_compatible"]]
```

```go
// 逐节点落地的核心：host:port + auth，ws 的交给内核。
for _, node := range payload.Nodes {
    switch node.Protocol {
    case "mixed", "http", "socks":
        // 可直接拨号
    case "vless", "vmess", "trojan":
        // transport=ws：由本机内核落地
    default:
        return fmt.Errorf("unknown protocol %q; contract changed?", node.Protocol)
    }
}
```

## 7. 契约稳定性

- `nodes[]` 字段**只增不改不删**。加字段不会破坏你。
- 字段改名、改类型、改 `protocol` 枚举值 = 破坏性变更，控制面会开**新路径版本**
  （`/api/v2/nodes`）而不是改这个。
- 因此：**遇到未知 `protocol` 值要报错并上报**，那是契约破裂的信号，不是让你猜的余地。
- 完整契约（字段表、稳定性承诺、变更流程）：
  `docs/CONSUMER_INTEGRATION_CONTRACT.md`。
