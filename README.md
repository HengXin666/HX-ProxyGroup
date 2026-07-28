# HX-ProxyGroup

面向个人服务器部署的代理订阅、节点质量评估、代理组编排与多协议出口管理平台。

> 项目名中的“代理组”统一使用 **Proxy Group**，目录名为 `HX-ProxyGroup`。

## 当前阶段

- [x] 明确 v1 产品边界与核心功能
- [x] 明确控制面 / 数据面分离架构
- [x] 明确低占用、高性能、可恢复部署约束
- [x] 创建 Go 控制面工程与健康检查
- [x] 实现备份 / 便携导出归档、下载与完整性校验 API
- [x] 接入 SQLite Desired State、迁移与 Online Backup
- [x] 实现加密订阅 CRUD、手动刷新、快照与自动调度
- [x] 实现 Clash/Mihomo YAML 与常见分享 URI 解析、稳定指纹去重和节点生命周期持久化
- [ ] 补齐剩余订阅协议兼容、Provider 展开与解析兼容矩阵
- [x] 接入 Mihomo 配置编译、校验、进程管理、Listener 就绪检查与失败回滚
- [x] 创建订阅、节点、代理服务、Backup / Export 的浅色 React 管理面板
- [x] 实现跨订阅动态 Proxy Group、入口与账号聚合编排、HTTP/SOCKS/Mixed Listener，以及单节点/批量延迟检测
- [x] 实现固定阶段规则流水线（Normalize→Enrich→Predicate→Score→Bucket→Sort→Limit）与可解释评分
- [x] 实现管理员认证（Argon2id、Session、CSRF、登录限速与锁定、改密全体注销）
- [x] 实现告警状态机（冷却、恢复通知、确认、重启不重复轰炸）与 SMTP 邮件通道
- [x] 订阅调度增强：Cron 表达式、重试随机抖动与快速重试上限、结构化失败原因持久化、节点管理员禁用
- [x] v2：浏览器内终端（PTY + WebSocket + xterm.js，默认关闭、强制认证、空闲超时、审计）
- [x] 完成 Listener、代理组和节点流量统计与 30 天分层聚合
- [ ] 完成 `install.sh` 与 systemd 双服务注册
- [ ] 完成基准测试与故障恢复测试

## 当前可运行能力

当前里程碑已提供 Go 控制面、SQLite WAL/迁移、事务一致 Online Backup、加密订阅 CRUD、自动刷新与快照、常见节点格式解析和指纹去重，以及 Mihomo 配置编译、语法校验、受控重启、Listener 就绪检查与失败回滚。可在 Web 面板中创建 Proxy Group，开放独立 HTTP、SOCKS5 或 Mixed 端口，并使用 Mihomo Unix Socket API 执行真实代理延迟检测。管理 API 在管理员认证完成前强制绑定显式环回 IP。

当前已经可以在不添加任何订阅的情况下，把本机 `DIRECT` 出口创建为代理服务；也可以按订阅树批量刷新、测试节点，并将多个订阅按地区名称标签、协议、状态和延迟筛选后排序取 Top N。入口支持 HTTP、SOCKS5、Mixed，以及由 Mihomo 提供的 VLESS/VMess/Trojan over WebSocket；高级入口可配置 Cloudflare 公网域名并生成 v2rayN、Clash/Mihomo、sing-box 三类客户端订阅。

本迭代新增：固定阶段规则流水线（每条规则输出命中/未命中/修改前后值/排除原因，可配置加权评分并支持显式缺失指标策略）；单管理员认证（首次启动生成 0600 的一次性 `admin-setup-token`，通过 `/api/v1/auth/setup` 初始化，此后全部 `/api/v1/*` 强制登录并校验 CSRF）；告警状态机与 SMTP 邮件通道（订阅连续失败、空快照、空代理组、数据面异常，冷却 6 小时、恢复通知一次、确认后静默、重启不重复轰炸）；订阅 Cron 调度、退避抖动与结构化失败原因；节点管理员禁用/启用；以及 v2 浏览器内终端。

尚未完成的是 Provider 展开、完整资源统计和更多服务端协议入口等扩展能力。流量统计的采样精度边界见 [`docs/TRAFFIC_STATS.md`](docs/TRAFFIC_STATS.md)。

### v2 浏览器内终端

终端默认关闭。启用方式：

```bash
HX_PROXYGROUP_TERMINAL=1 bash run.sh
```

约束：必须先完成管理员初始化并登录；每个会话空闲 10 分钟自动断开、最长 2 小时；并发会话上限 2；全部会话的建立与关闭（含操作者、来源地址、时长与关闭原因）写入结构化审计日志。可用 `HX_PROXYGROUP_TERMINAL_SHELL` 覆盖默认 Shell。

```bash
cd HX-ProxyGroup
go test ./...
bash run.sh
```

`run.sh` 仅在项目目录下构建并启动进程，不要求 root，不注册 systemd，也不会写入 `/usr/local`、`/etc` 或 `/var/lib`。本地状态默认保存在 `./.tmp/run-data`，按 `Ctrl+C` 会统一停止后端和前端子进程。

`run.sh` 会自动识别 npm / pnpm / yarn / bun，并同时启动 Go 后端和 Vite 前端。首次运行若缺少 `web/node_modules`，脚本会优先根据锁文件自动安装依赖。前端默认地址为 `http://127.0.0.1:5173`，节点页为 `http://127.0.0.1:5173/#/nodes`。前端通过 Vite 代理访问后端，因此无需额外开放 CORS。管理面板仅提供浅色模式。

受管 Mihomo 默认从 Linux 主路由表选择物理出口网卡，并将 `interface-name` 写入运行配置。这使 HX 与 Clash Verge、Mihomo Party 等 TUN 客户端在同一台机器运行时，HX 的上游拨号不会再次进入宿主机 TUN。默认还将 Mihomo 的 `GOMAXPROCS` 限制为最多 4，并把 `runtime/mihomo.log` 限制为单文件 8 MiB、保留 2 份。

多网卡环境可设置 `HX_PROXYGROUP_MIHOMO_EGRESS_INTERFACE=eth0` 指定出口。只有确实需要让 HX 再经过宿主机 VPN / TUN 时，才设置 `HX_PROXYGROUP_MIHOMO_EGRESS_INTERFACE=off`。资源上限可通过 `HX_PROXYGROUP_MIHOMO_MAX_PROCS`、`HX_PROXYGROUP_MIHOMO_LOG_MAX_BYTES` 和 `HX_PROXYGROUP_MIHOMO_LOG_BACKUPS` 调整。

如需禁止自动安装依赖：

```bash
bash run.sh --no-install-frontend-deps
```

只启动后端：

```bash
bash run.sh --backend-only
```

查看全部参数：

```bash
bash run.sh --help
```

创建并校验一个便携导出：

```bash
curl -fsS -X POST http://127.0.0.1:19090/api/v1/exports \
  -H 'Content-Type: application/json' \
  -d '{"description":"manual export","include_secrets":false}'

curl -fsS http://127.0.0.1:19090/api/v1/exports
```

创建并刷新一个内联订阅：

```bash
subscription_id="$({
  curl -fsS -X POST http://127.0.0.1:19090/api/v1/subscriptions \
    -H 'Content-Type: application/json' \
    -d '{"name":"example","source_type":"inline","source_config":{"inline":"vless://11111111-1111-1111-1111-111111111111@example.com:443?security=tls#example-node"}}'
} | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"

curl -fsS -X POST \
  "http://127.0.0.1:19090/api/v1/subscriptions/${subscription_id}/refresh"
```

该示例中的 `python3` 只用于提取演示响应字段；生产服务本身不依赖 Python。

服务器安装当前控制面：

```bash
sudo bash install.sh install --version dev
```

Backup 通过 SQLite Online Backup API 包含事务一致的数据库快照，但普通归档不包含主密钥、Mihomo 运行配置或原始订阅快照。完整加密灾难恢复、Restore 和 Portable Import 仍在后续清单中。

## 核心定位

HX-ProxyGroup 不是新的代理协议实现，而是一个代理控制平面：

1. 加载并定时刷新多个机场订阅。
2. 对节点执行可扩展的质量检测、过滤、评分和排序。
3. 将多个订阅和规则组合为可复用代理组。
4. 为每个代理组暴露独立的 HTTP、SOCKS5 或 Mixed 端口。
5. 支持负载均衡、故障切换和会话粘滞。
6. 将当前服务器作为直连出口，生成可供 Clash、V2Ray 等客户端使用的节点订阅。
7. 提供 30 天统计、告警和管理员 Web 面板。

## 固定技术路线

| 层级 | 选择 | 约束 |
| --- | --- | --- |
| 控制面 | Go 单体服务 | 单二进制、低常驻内存、无运行时脚本依赖 |
| 数据面 | Mihomo 独立受管进程 | 复用成熟协议栈、Proxy Provider、Proxy Group、Listener 与 API |
| 数据库 | SQLite + WAL | 单机部署优先，批量写入，按周期聚合和清理 |
| 前端 | React + TypeScript + Tailwind + shadcn/ui | 生产环境仅部署静态资源，不运行 Node.js |
| 实时通信 | SSE 为主，WebSocket 按需 | 状态推送避免高频轮询 |
| 服务管理 | systemd | 开机启动、崩溃重启、Watchdog、资源限制 |
| 部署 | `install.sh` | 幂等安装、升级、回滚和完整卸载 |

## 文档

- [`docs/V1_CORE.md`](docs/V1_CORE.md)：v1 核心功能清单与验收标准。
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)：系统架构、领域模型和扩展点。
- [`docs/RELIABILITY.md`](docs/RELIABILITY.md)：可靠性、安全、安装和运行约束。
- [`docs/BACKUP_EXPORT.md`](docs/BACKUP_EXPORT.md)：备份、恢复、便携导出与当前 API。
- [`docs/SUBSCRIPTIONS.md`](docs/SUBSCRIPTIONS.md)：订阅加密存储、刷新、SSRF 边界与自动调度。
- [`docs/PROBES_ROUTING_OVERVIEW.md`](docs/PROBES_ROUTING_OVERVIEW.md)：节点检测、全局配置、路由规则集与秒级总览。
- [`docs/CLOUDFLARE.md`](docs/CLOUDFLARE.md)：本机 WebSocket 服务端入口、Cloudflare/反向代理配置和能力边界。
- [`docs/TRAFFIC_STATS.md`](docs/TRAFFIC_STATS.md)：流量聚合、查询、保留策略和精度边界。
- [`docs/V2.md`](docs/V2.md)：v2 交付说明——规则流水线、管理员认证、告警、调度增强与浏览器内终端。
- [`AGENTS.md`](AGENTS.md)：后续 AI / Agent 开发必须遵守的工程规范。
- [`ref/README.md`](ref/README.md)：参考项目与参考范围。

## v1 明确不做

- [ ] 多管理员、多租户和复杂 RBAC。
- [ ] 多节点控制面集群或分布式数据库。
- [ ] Kubernetes Operator。
- [x] ~~浏览器内 SSH 终端；该功能进入 v2。~~ 已随 v2 交付为本机 Shell 终端（默认关闭），见 [`docs/V2.md`](docs/V2.md)。
- [ ] 自研 VMess、VLESS、Trojan、Hysteria、TUIC 等协议实现。
- [ ] 承诺“任意未知协议均可解析”；协议能力以当前数据面版本实际支持范围为准。

## 设计原则

- **协议能力由数据面提供，控制面不手搓协议。**
- **一个控制面进程 + 一个数据面进程，不为每个节点启动独立进程。**
- **配置变更必须先生成、校验、原子替换，再热重载或平滑重启。**
- **刷新失败不得清空上一版可用节点。**
- **质量检测必须有并发、带宽和频率预算，禁止无界探测。**
- **生产环境不依赖 Node.js、Python、Redis、MySQL。**
- **所有关键操作必须可追踪、可回滚、可在服务器重启后恢复。**
