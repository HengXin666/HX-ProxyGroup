# 共享入口（一个客户端、两条 URL）

> 20260910 用户决策：取消「每个代理服务绑定一个本地端口、开一个客户端」的模型。
> 现在所有服务复用同一个 Mixed 端口与同一组 WebSocket 端口，由 Mihomo 按
> 用户名（`IN-USER`）分流。一个客户端进程即可覆盖所有代理组与协议。

## 1. 问题

旧模型为每个 Proxy Group 创建一条独立 Listener：

```text
服务 A -> 127.0.0.1:18080 (mixed) -> 组 A
服务 B -> 127.0.0.1:18081 (mixed) -> 组 B
服务 C -> 127.0.0.1:18082 (vless ws) -> 组 A
```

控制面本身只需要一个数据面进程，但客户端侧必须为每个组启动一个客户端实例、
维护多份配置。协议、端口、凭据三者被绑死，增加一个业务就多一个端口。

## 2. 共享模型

一条服务的身份来自**它自己的凭据**，而不是它绑定的端口。因此每个服务仍然保留
自己的 Listener 行（自己的名称、协议、凭据与 Proxy Group），但这些行共享同一个
绑定地址与端口；编译器把它们合并成少量聚合 Listener，并为每个成员生成一条
`IN-USER` 规则：

```text
服务 A --\
服务 B ----> 0.0.0.0:7890 (mixed, users: svc-group-a, svc-group-b)
                              |
                              +-- IN-USER=svc-group-a -> 组 A
                              +-- IN-USER=svc-group-b -> 组 B

服务 C --\
服务 D ----> 127.0.0.1:7891 (vless / vmess / trojan, ws-path /__hx-proxy__/shared)
                              +-- IN-USER=svc-group-a -> 组 A
                              +-- IN-USER=svc-group-c -> 组 C
```

### 端口布局

| 家族 | 协议 | 默认端口 | 监听范围 | 面向客户端 |
| --- | --- | --- | --- | --- |
| standard | http / socks / mixed | 7890 | 可配置（默认 `0.0.0.0`） | 直连或经反代 |
| websocket | vless / vmess / trojan | 7891 | 只允许环回 | 经雷池 / Cloudflare 反代到 443 |

同一家族的 WebSocket 协议共用一个端口与一个 ws-path，因此**一条 URL 同时是
VLESS / VMess / Trojan**：客户端按自己的协议解析，用户名决定落到哪个组。

### 为什么用户名能区分服务

Mihomo 的 Listener 支持 `users` 列表，规则引擎提供 `IN-USER` 匹配。住宅代理早已
使用同一机制（每个声明会话一个 `IN-USER` 路由），共享入口只是把它应用到普通服务。
编译顺序保证成员选择先于站点路由：

```text
1. 住宅会话路由      AND,((IN-NAME,...),(IN-USER,hx-session-...)),<出口>
2. 共享入口成员选择  AND,((IN-NAME,hx-in-shared-...),(IN-USER,svc-<group-id>)),<组名>
3. 管理员站点别名    AND,((IN-NAME,...),(DOMAIN-SUFFIX,...)),REJECT|DIRECT|<组>
4. 兜底              MATCH,DIRECT
```

成员用户名由 Proxy Group ID 派生（`svc-<group-id>`），因此一个服务无论被编辑
多少次、无论它的 Listener 行被重建多少次，订阅里的用户名与密码都保持稳定。

## 3. 兼容与迁移

- **升级不移动端口**：已存储的全局配置若不含 `shared_inbound` 段，加载时被标记为
  `per_service`，保持旧行为；只有管理员在「全局配置 → 性能与运行」显式打开共享入口
  才会迁移。
- **打开共享入口时自动迁移**：`internal/sharedinbound` 把现有服务逐个改写到聚合
  端口，并与聚合行收敛在同一次原子 Apply 中；失败会连同设置一起回滚。
- **迁移跳过两类服务**：
  - 未启用用户名/密码认证的服务（共享入口靠用户名分流，无凭据会让聚合端口对所有
    连接放行）；
  - 住宅渠道托管的 WebSocket 入口（它们有自己的会话级 `IN-USER` 路由）。
- **关闭共享入口**：聚合行被删除、端口释放，服务行保留（仍标记为成员，重新打开即
  恢复），不影响任何已有订阅以外的流量。

## 4. 订阅导出

成员服务的 `/sub/<token>` 不再导出自己的内部端口，而是导出聚合入口：

- standard 成员导出共享 Mixed 的绑定地址/公网主机名；
- websocket 成员导出 `shared_inbound.ws_public_host`，未配置时该链接返回
  `ErrShareDisabled`（404），避免把内部环回端口写进订阅。

## 5. 端点唯一性

- 专用（`per_service`）Listener 之间仍然禁止端口冲突；
- 同一共享家族（且仅有同一家族）允许复用端口——这正是协议多路复用的前提；
- 不同家族之间冲突仍然报错。

## 6. 配置参考

```json
{
  "shared_inbound": {
    "mode": "shared",
    "mixed_bind_address": "0.0.0.0",
    "mixed_port": 7890,
    "ws_port": 7891,
    "ws_public_host": "proxy.example.com",
    "ws_public_port": 443,
    "mixed_public_host": "",
    "mixed_public_port": 0,
    "mixed_public_tls": false
  }
}
```

`mode` 取值 `shared` 或 `per_service`。仅当 `mode=shared` 时其余字段参与校验与编译。

## 7. 验证

```bash
# 单元测试：编译形状、成员规则、端口复用
go test ./internal/dataplane/mihomo/ ./internal/listener/ ./internal/sharedinbound/

# 真实 Mihomo 校验：配置能被 mihomo -t 接受
PATH=$PATH:/path/to/mihomo go test -run TestCompileSharedInboundPassesMihomoValidation ./internal/dataplane/mihomo/

# 运行时证明：同一端口上两个用户名走到两个不同的组
PATH=$PATH:/path/to/mihomo go test -run TestSharedInboundRoutesOnePortPerService ./internal/dataplane/mihomo/
```
