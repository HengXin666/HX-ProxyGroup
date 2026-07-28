# Cloudflare 反向代理接入

HX-ProxyGroup 仍是控制面，实际服务端协议由本机受管 Mihomo Listener 提供。当前可创建
VLESS、VMess、Trojan over WebSocket 入口，并让反向代理在公网 `443` 终止 TLS 后转发到
环回 Listener。

```text
客户端 -> Cloudflare :443 -> Nginx/Caddy/雷池 :443
       -> 127.0.0.1:<listener-port> -> Mihomo -> Proxy Group/DIRECT
```

## 创建入口

在「代理服务」中选择 `VLESS WS`、`VMess WS` 或 `Trojan WS`，填写：

- 绑定 IP：固定为 `127.0.0.1`，不得直接公开无 TLS 的回源端口。
- 本地端口：反向代理连接的端口，例如 `18088`。
- Cloudflare 域名：客户端实际连接的橙云域名，例如 `proxy.example.com`。
- WebSocket Path：独立且不可与管理 API 重叠的路径，例如 `/hx-edge-7f3a`。
- 凭据：VLESS/VMess 使用 UUID；Trojan 使用高强度随机密码。

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

location = /hx-edge-7f3a {
    proxy_pass http://127.0.0.1:18088;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_read_timeout 300s;
}
```

雷池中使用同一拓扑时：

1. 创建域名为 `proxy.example.com` 的 HTTPS 站点并启用 WebSocket。
2. 上游填写 `http://127.0.0.1:18088`，转发路径精确匹配 `/hx-edge-7f3a`。
3. 为 `/sub/` 单独创建上游 `http://127.0.0.1:19090`，不要落入管理前端的 SPA 回退。
4. 不把 `/api/` 或 Mihomo External Controller 加入这个公网站点。

「代理服务」页会根据已创建的高级入口显示实际域名、环回端口和 WebSocket Path，并提供客户端订阅；
雷池配置必须与这些值完全一致。

示例中的 `19090` 是 HX 控制面端口，`18088` 是 Mihomo Listener 端口，两者不能互换。
`/sub/` 路由需要放在通用 SPA fallback 或默认 `location /` 之前，并保留原始查询参数。不要将
`/api/`、管理页面或 Mihomo External Controller 一并公开。Cloudflare 对 `/sub/*` 应关闭缓存、
浏览器完整性检查和交互式质询，否则代理客户端可能下载到 HTML 质询页面。

反向代理配置的 WebSocket Path 和本地端口必须与 HX-ProxyGroup 中的入口完全一致。应用配置后
应先从 Cloudflare 外部网络执行真实代理请求，再分发订阅。

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
