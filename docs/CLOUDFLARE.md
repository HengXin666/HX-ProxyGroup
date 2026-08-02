# Cloudflare 反向代理接入

HX-ProxyGroup 仍由 Mihomo 提供实际服务端协议。为了让雷池只配置一个 HX 上游，控制面
内置一个受限的 WebSocket Edge Relay：它只识别固定的 `/__hx-proxy__/` 路由前缀，按
`Host + 完整路径` 查找已启用的环回 Mihomo Listener，再把 Upgrade 连接转发过去。
它不解析 VLESS、VMess 或 Trojan，也不接受任意 URL、TCP、UDP 或 QUIC 转发。

```text
客户端 -> Cloudflare :443 -> Nginx/Caddy/雷池 :443
       -> 127.0.0.1:19090 -> HX Edge Relay
       -> 127.0.0.1:<listener-port> -> Mihomo -> Proxy Group/DIRECT
```

## 创建入口

在「代理服务」中选择 `VLESS WS`、`VMess WS` 或 `Trojan WS`，填写：

- 绑定 IP：固定为 `127.0.0.1`，不得直接公开无 TLS 的回源端口。
- 本地端口：反向代理连接的端口，例如 `18088`。
- Cloudflare 域名：客户端实际连接的橙云域名，例如 `proxy.example.com`。
- WebSocket Path：填写一个路径片段，例如 `/edge-7f3a`。服务端会统一规范化为
  `/__hx-proxy__/edge-7f3a`；已经带此前缀的路径保持不变。路径只允许安全 ASCII 片段，
  不允许查询串、片段标识、反斜杠、重复斜杠和 `.` / `..` 路径段。
- 凭据：VLESS/VMess 使用 UUID；Trojan 使用高强度随机密码。

`/__hx-proxy__/` 是 HX 预留的代理路由命名空间。订阅导出和 Mihomo 配置使用规范化后的
完整路径，雷池也必须原样保留这个路径。路径不是认证机制，真正的凭据仍由 Mihomo
Listener 校验。

## Cloudflare 设置

1. 为代理域名创建 `A`/`AAAA` 记录并开启代理（橙云）。
2. SSL/TLS 模式使用 `Full (strict)`，回源反向代理部署有效证书或 Cloudflare Origin Certificate。
3. 保持 WebSocket 可用；不要为代理 Path 启用会改写请求体、缓存响应或挑战客户端的规则。
4. 管理 API 使用另一个仅内网可达的域名或 SSH 隧道，不与公开代理 Path 共用路由。
5. 防火墙只允许 Cloudflare 公布的地址段访问公网 `80/443`，本地 Mihomo 端口始终只监听环回。

Nginx 回源示例：

```nginx
# 订阅下载必须转发到 HX 控制面。不要让 SPA fallback、默认站点或
# Mihomo WebSocket Listener 处理 /sub/。
location ^~ /sub/ {
    proxy_pass http://127.0.0.1:19090;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
    proxy_read_timeout 30s;
}

# HX Edge Relay 处理所有规范化的 WebSocket 代理路径，再转给对应 Mihomo Listener。
location ^~ /__hx-proxy__/ {
    proxy_pass http://127.0.0.1:19090;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
    proxy_read_timeout 300s;
}
```

雷池中使用同一拓扑时：

1. 创建域名为 `proxy.example.com` 的 HTTPS 站点并启用 WebSocket。
2. 代理路径 `/__hx-proxy__/` 和订阅路径 `/sub/` 都回源到同一个
   `http://127.0.0.1:19090`，不要把 WebSocket 路径直接回源到某个 Mihomo 端口。
3. 保留原始 `Host` 和完整 URI Path，启用 WebSocket Upgrade，代理读取超时至少设置为
   `300s` 或按雷池的长连接配置处理。
4. 不把 `/api/`、管理页面或 Mihomo External Controller 加入这个公网站点。

「代理服务」页会根据已创建的高级入口显示实际域名、环回端口和 WebSocket Path，并提供客户端订阅；
雷池配置必须与这些值完全一致。

示例中的 `19090` 是 HX 控制面和 Edge Relay 端口，`18088` 是 Mihomo Listener 端口；
使用该拓扑时雷池的代理 WebSocket 和 `/sub/` 都回源到 `19090`，不能把代理路径直接回源到
`18088`。HX 会依据 Host 和 `/__hx-proxy__/...` 完整路径选择 `18088` 或其他 Listener。
`/sub/` 路由需要放在通用 SPA fallback 或默认 `location /` 之前，并保留原始查询参数。不要将
`/api/`、管理页面或 Mihomo External Controller 一并公开。Cloudflare 对 `/sub/*` 应关闭缓存、
浏览器完整性检查和交互式质询，否则代理客户端可能下载到 HTML 质询页面。

反向代理必须保留 WebSocket Path 和 Host，且客户端订阅中的完整路径必须与 HX-ProxyGroup
中展示的规范化路径完全一致。应用配置后应先从 Cloudflare 外部网络执行真实代理请求，再
分发订阅。

## 客户端订阅

每个入口生成一个可轮换 Token。管理面可复制三类链接：

```text
/sub/<token>?format=v2rayn
/sub/<token>?format=clash
/sub/<token>?format=sing-box
```

默认格式为 v2rayN Base64 URI 列表；Clash/Mihomo 和 sing-box 客户端访问无参数旧链接时，
服务端也会按客户端 User-Agent 返回对应格式。`format=uri` 可用于诊断明文分享 URI。订阅内容只包含
当前服务器入口，不包含上游机场节点凭据。Token 本身是访问凭证，泄露后应立即轮换。

### 住宅渠道的边界

住宅渠道的 `HTTP`、`SOCKS5` 和 `Mixed` 仍是 Mihomo 的原生代理入口，不是 Clash 配置，也不是
WebSocket 协议。Cloudflare 橙云或仅支持 WebSocket 的雷池不能把它们变成 HTTP CONNECT/SOCKS5
字节流；这些入口必须使用直连、VPS 四层转发或真正的 HTTP/SOCKS5 反向代理。

住宅渠道现在也可以在「住宅代理」页选择 `VLESS WS`、`VMess WS` 或 `Trojan WS`。这类入口由
Mihomo 原生提供，允许复用本页的 Edge Relay：

```text
客户端 -> Cloudflare -> 雷池 -> 127.0.0.1:19090
       -> HX Edge Relay -> 127.0.0.1:<住宅 WS Listener>
       -> 动态住宅节点 -(dialer-proxy)-> 上游 Proxy Group -> 住宅网关
```

WS 入口必须绑定环回、配置 TLS 公网域名、保留规范化后的 `/__hx-proxy__/...` 路径，并使用
VLESS/VMess UUID 或 Trojan 密码。sticky 渠道通过 `/rot/<token>/sessions/<id>` 获取会话凭据；
VLESS/VMess 会话密码是合法 UUID，`proxy_username` 只用于控制面编译 `IN-USER` 路由，不拼入
VLESS URI。`/rot/` 仍是普通 HTTP API 路径，必须由 HTTP 反向代理单独转发，不能让 Edge Relay 处理。

住宅 HTTP/SOCKS/Mixed 页面只复制原生代理地址；WS 入口显示 `ws(s)://host/path` 端点，客户端
协议参数和凭据应按 Mihomo/v2rayN/sing-box 的 WS 配置填写。没有公网端点时不会复制 `127.0.0.1`。

### Clash Verge 导入诊断

在运行 Clash Verge 的同一台机器上请求订阅地址。执行前把示例域名替换为实际域名；不要在日志、
截图或工单中公开真实 Token：

```bash
curl -i -A 'clash-verge/v2.4.2' \
  'https://proxy.example.com/sub/REDACTED?format=clash'
```

正确响应应同时满足：

- HTTP 状态为 `200`。
- `Content-Type` 为 `application/yaml; charset=utf-8`。
- 存在 `X-HX-Subscription-Format: clash`。
- 响应正文是 YAML，并包含顶层 `proxies:`；不能是 `<!doctype html>`、Cloudflare 质询页或管理前端。

若没有 `X-HX-Subscription-Format`，请求没有到达 HX 的订阅处理器，应先修复反向代理路由；这不是
Clash YAML 生成错误。诊断完成后，应从 Shell 历史删除包含真实 Token 的命令，或使用临时环境变量
避免保存凭据。

## 能力边界

- Cloudflare 普通代理可以承载这里的 WebSocket 模板，但不转发任意原生 TCP/UDP。
- Hysteria2、TUIC 等 QUIC/UDP 协议需要直连、Cloudflare Spectrum 或其他四层产品。
- 域名经 Cloudflare 可避免客户端直接连接源站 IP，但不能保证源站 IP 永远不会被历史 DNS、旁路服务或配置错误暴露。
- 如果源站到 Cloudflare 的网络本身不可达，域名代理也无法修复该回源链路。
