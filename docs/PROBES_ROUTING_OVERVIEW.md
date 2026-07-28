# 节点检测、路由规则与实时总览

本文记录节点检测、全局运行配置、路由规则集和实时总览的当前实现边界。HX-ProxyGroup 仍是控制面；所有代理转发、规则匹配和连接计数由 Mihomo 数据面完成。

## 1. 节点检测

### 1.1 交互语义

- 批量检测开始时，节点页先清空本轮全部检测结果。
- 后端使用有界 worker pool，默认并发数由全局配置控制，最大为 16。
- `POST /api/v1/nodes?stream=1` 返回 NDJSON；每完成一个节点立即返回一次进度。
- 前端逐个填充延迟、健康站点结果和错误状态，并按当前排序选项重新排序。
- 单击节点行只检测该节点；离开页面或开始新一轮批量检测会取消旧请求。
- 节点页的速度值是经指定代理执行 HTTP 探测得到的延迟，单位为毫秒，不代表下载吞吐量。

复测响应中的 `sources` 和 `health_checks` 始终按空数组处理，避免空值导致页面崩溃。流量统计接口不可用时，节点列表仍可独立加载。

### 1.2 健康站点

全局配置页提供 ChatGPT、Claude、GitHub、Google、Telegram 默认目标，也允许新增自定义 HTTP/HTTPS URL。附加站点探测只记录站点健康度，不改变节点的主生命周期结果。

为降低 SSRF 风险，配置拒绝指向字面量私网、环回、链路本地和其他特殊用途 IP 的探测地址。域名解析、重定向目标变化和 DNS Rebinding 仍需在后续版本中加入逐跳解析校验。

## 2. 全局配置

`GET /api/v1/settings` 和 `PUT /api/v1/settings` 管理以下配置：

- 节点检测 URL、周期、超时、批量大小、并发数和健康站点。
- Mihomo DNS 开关、IPv6、增强模式、Bootstrap DNS、主 DNS 和 Fallback DNS。
- TCP 并发建连、统一延迟、TCP Keepalive、进程识别模式和日志级别。

配置保存在 SQLite Desired State 中。更新时先校验完整候选配置，再调用 Mihomo Apply；应用失败会恢复旧设置并重新应用上一版。编译器使用结构化对象生成 YAML，不通过字符串拼接配置。

## 3. 路由规则集

`GET /api/v1/routing-rules` 和 `PUT /api/v1/routing-rules` 管理可复用的全局规则集。规则集具有稳定 ID、优先级、启用状态、目标动作和适用代理组。

支持的规则类型：

- `domain`、`domain_suffix`、`domain_keyword`
- `ip_cidr`、`geoip`、`geosite`
- `process_name`、`network`、`dst_port`

动作可以是 `REJECT`、`DIRECT` 或指定 Proxy Group。空的 `applied_group_ids` 表示全局规则；指定代理组时，编译器根据该组的 Listener 生成 Mihomo `IN-NAME` 条件。广告和垃圾域名应使用 `REJECT`，无需启动不存在的本地网站，也不会让控制面处理被拒绝的流量。

保存流程会校验规则值、动作引用和代理组引用，生成稳定优先级顺序的完整候选配置，并在 Mihomo Apply 失败时回滚。最终兜底规则固定为 `MATCH,DIRECT`。

## 4. 实时总览

总览页组合 Listener、Proxy Group 和规则集 API，展示当前入口到代理组、策略和有效规则集的关系。`GET /api/v1/overview/stream` 提供只读 SSE：

```text
event: sample
data: {"timestamp":"...","upload_bytes_per_second":0,"download_bytes_per_second":0,"active_connections":0,"running":true}
```

每个已认证的总览请求每秒读取一次 Mihomo Unix Controller 的 `/connections`。API 对相邻活动连接累计字节做非负差分；计数器重置不会产生负速率。没有流量时仍发送零值样本。请求断开后 ticker 随请求上下文停止，没有按节点或代理组创建永久 goroutine。

前端最多保留 120 个样本，可切换 30、60、120 秒滑动窗口。该实时曲线用于操作观察，不替代 SQLite 中的长期计费或审计统计；连接在两个采样点之间建立并结束时，尾部字节可能无法出现在实时曲线中。

界面验证截图：

- [桌面总览](screenshots/global-overview-desktop.png)
- [移动总览](screenshots/global-overview-mobile.png)
