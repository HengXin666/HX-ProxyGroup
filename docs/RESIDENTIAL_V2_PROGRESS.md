# 住宅代理与统一订阅进度

> 状态时间：2026-08-04  
> 当前契约：[`RESIDENTIAL_V2_CONTRACT.md`](RESIDENTIAL_V2_CONTRACT.md)  
> 用户在 2026-08-04 确认的新决策优先于此前的 `/rot/` 会话方案。

## 1. 当前目标

HX-ProxyGroup 对客户端发布一份统一订阅，聚合：

- 本机直接代理和机场订阅经过 Proxy Group/Listener 发布的节点。
- 住宅渠道发布的声明节点。

客户端看到的是 Mixed、HTTP、SOCKS5、VLESS WS、VMess WS、Trojan WS 等 Mihomo
当前版本实际支持的入口节点。住宅节点的显示名称和凭据保持稳定，供应商会话、真实出口 IP
和轮换过程由服务端管理；调用 `next` 只替换该逻辑节点背后的住宅出口，不要求客户端重拉订阅。

推荐的公网拓扑是：

```text
Clash 等客户端
  -> HTTPS/WSS 域名
  -> Cloudflare 橙云
  -> VPS:443 雷池
  -> HX-ProxyGroup Edge Relay
  -> Mihomo 环回 WS Listener
  -> 机场节点或住宅节点
```

新建住宅渠道固定通过 Cloudflare/雷池承载 VLESS over WebSocket。浏览器需要 HTTP/SOCKS 时，
由客户端本机 Mihomo 消费统一订阅后落地为环回端口。Go 控制面不实现协议转发。

## 2. 已完成

### 2.1 声明住宅节点

- Schema v24 增加 `session_count`、`idle_release_seconds`、独立 `control_token`、可选
  `direct_listener_id`、声明序号和最后使用时间。
- sticky 渠道可声明 1..N 个稳定节点；缩扩容保留仍存在节点的名称和凭据。
- 空闲释放只释放供应商分配，节点身份仍保留；下一次使用或 `next` 时重新分配。
- 每个节点可在管理面执行 `next`，并可单独切换 `residential`、`upstream` 或 `direct` 路由。
- 住宅 WS 入口保持环回绑定；内部端口、WS 路径和引导 UUID 自动分配，新建 API 拒绝直连入口。

### 2.2 统一客户端订阅

- `GET /api/v1/client-subscription` 返回统一订阅路径和节点数。
- `POST /api/v1/client-subscription {"action":"rotate"}` 轮换统一订阅 token。
- `GET /sub/<token>?format=clash|v2rayn|sing-box|uri` 使用同一渲染器聚合普通 Listener
  和住宅声明节点。
- 住宅渠道的内部 provisioning 凭据不会进入统一订阅；每个声明节点使用自己的稳定凭据。
- 新住宅渠道只发布 VLESS WS 节点；历史入口继续兼容渲染。
- 发布新 token 前先构建完整候选订阅；构建失败不会让旧 token 失效。
- 前端在「住宅代理 → 渠道」提供统一链接、按客户端格式复制和二次确认的 token 轮换。

### 2.3 自动化控制与 OutlookRegister

- `GET /ctl/<control_token>/nodes` 返回声明节点池。
- `POST /ctl/<control_token>/nodes/<index>/next` 刷新指定节点背后的住宅出口。
- `POST /ctl/<control_token>/nodes/<index>/route` 切换该节点路由。
- 控制 token 与订阅 token 分离；未知、禁用和不适用的 token 返回 404，访问日志隐藏 token。
- OutlookRegister 优先使用 `proxy_rotation.control_url`，在进程内互斥租用声明节点，获取时执行
  `next` 并校验出口身份，释放时只归还本地租约。
- OutlookRegister 仍兼容旧 `/rot/` 配置，但新配置不再创建或删除服务端临时会话。
- OutlookRegister 托管本机 Mihomo，将 VLESS WS 节点落地为线程独享的环回浏览器代理。

### 2.4 住宅流量统计

- Schema v25 增加稳定资源类型 `residential_channel`。
- 托管 WS Listener 和历史兼容入口都归因到渠道 ID；出口 IP 轮换不会重置累计值。
- 上传、下载、连接数和趋势继续由 Mihomo 连接快照采样、内存聚合并批量写 SQLite。
- 住宅页显示渠道累计流量；删除渠道时由数据库触发器清理对应统计。
- 统计是采样观测值，不作为精确计费账单。

### 2.5 发布、自动更新与终端

- Git tag `v*` 触发 GitHub Actions：测试、vet、前端构建、amd64/arm64 打包、固定 Mihomo
  下载、SHA-256 校验文件和 GitHub Release 发布。
- 关于页提供一键更新。接口要求已登录管理员和最近完成的 2FA step-up；root helper 只接受
  无参数的固定升级请求，并调度 `/usr/local/sbin/hx-proxygroup-install upgrade`。
- 浏览器终端默认不再因空闲 10 分钟断开，仍保留单会话 2 小时绝对上限和并发/帧大小限制。
- 前端仅在 PTY 的 echo+canonical 模式下预测显示可打印输入，并约 12ms 合并安全输入；控制键、
  回车、密码和 raw 模式立即发送。服务端只在 PTY 模式变化时发送 mode 帧。

## 3. 已验证

- `go test ./...`：373 passed，31 packages。
- `go vet ./...`：通过。
- 前端 TypeScript 检查和生产构建：通过。
- 终端预测回显测试：6 passed。
- OutlookRegister 全量测试：96 passed。
- OutlookRegister Dashboard 构建：通过。

## 4. 仍需真实环境验收

- 使用真实住宅供应商和 Mihomo 完成：建渠道 -> 拉统一订阅 -> 真实代理请求 -> `next` ->
  再次请求 -> 核对渠道流量。
- 验证 CF 橙云 + 雷池 443 的托管 VLESS WS 链路，以及客户端本机 Mihomo 落地
  HTTP/SOCKS/Mixed 端口；不得把前者的成功外推到 CONNECT/SOCKS。
- 在干净 amd64/arm64 主机上验证 GitHub Release 安装、前端一键升级、失败保留旧 Release 和回滚。
- 保存统一订阅、住宅节点和关于页更新入口的桌面/移动端浏览器截图。

未完成上述真实边界验收前，不宣称住宅供应商兼容性、精确计费、端到端吞吐或无中断升级。
