# Reference Projects

参考项目默认放在本目录，但不作为 HX-ProxyGroup 源码的一部分提交。

## easy-proxies

- 仓库：`https://github.com/daimon3332/easy-proxies`
- 目标目录：`ref/easy-proxies`
- 许可证：MIT；使用具体代码前仍需保留原始许可和署名。
- 同步脚本：`../scripts/sync-reference.sh`

### 值得参考的能力

根据项目公开文档，easy-proxies 是一个基于 sing-box 的订阅优先代理节点导入、测试、池管理和多端口网关，主要包含：

- HTTP / HTTPS 订阅、URI 列表、Base64 和 Clash / Mihomo YAML 导入。
- 并发异步节点测试和实时进度。
- candidate、pooled、failed 节点生命周期，而不是静默丢弃失败节点。
- `multi-port`、`pool`、`hybrid` 三种运行模式。
- 节点复测、地区识别、订阅刷新、端口检查和日志。
- 使用成熟代理内核处理协议，而不是业务层自行实现协议。

### HX-ProxyGroup 重点借鉴

- [ ] 订阅导入后的候选、成功、失败状态均可见。
- [ ] 刷新失败保留上一版节点。
- [ ] 测试任务提供实时进度。
- [ ] 节点和端口具有稳定生命周期。
- [ ] 参考其 sing-box 配置生成和进程管理方式。
- [ ] 参考其订阅格式测试样例。

### HX-ProxyGroup 不直接照搬

- HX-ProxyGroup v1 的数据面选用 Mihomo，以复用 Proxy Provider、Proxy Group、Load Balance、Sticky Sessions 和多 Listener。
- HX-ProxyGroup 以“多个订阅 + 可解释规则流水线 + 多代理组”为核心，而不是默认一节点一端口。
- HX-ProxyGroup 需要 30 天统计、告警、配置事务、回滚、systemd 服务和长期运行约束。
- HX-ProxyGroup 管理面和数据面严格分离，控制面不进入转发路径。

## 使用方式

```bash
bash scripts/sync-reference.sh
```

脚本行为：

- 目录不存在时执行浅克隆。
- 已存在 Git 仓库时执行 fast-forward 更新。
- 工作区有未提交修改时拒绝覆盖。
- 不使用 `reset --hard` 清理本地改动。
