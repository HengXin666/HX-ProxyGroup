# 基准测试与故障恢复测试

## 1. 测试环境

- 日期：2026-07-29
- 系统：Linux amd64
- Go：`go1.26.5-X:nodwarf5`
- CPU：13th Gen Intel Core i9-13980HX，24 核 / 32 逻辑 CPU
- 内存：62 GiB

复现命令：

```bash
go test -run '^$' \
  -bench 'Benchmark(Parse10000MihomoNodes|Compile10000Nodes|TrafficBatchWrite1000|TrafficQuery500Points)$' \
  -benchmem -count=3 \
  ./internal/nodeparse ./internal/dataplane/mihomo ./internal/store
```

## 2. 结果

下表给出 3 次运行的中间值和范围。`B/op` 是单次操作的累计分配量，不等于进程峰值 RSS。

| 场景 | 中间值 | 3 次范围 | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| 解析 10,000 个 Mihomo 节点 | 87.82 ms | 85.30-89.47 ms | 59,174,174 | 1,180,105 |
| 编译 10,000 个节点的完整配置 | 105.65 ms | 103.63-106.64 ms | 234,183,660 | 760,313 |
| SQLite 批量写入 1,000 个统计资源 | 33.80 ms | 33.65-34.06 ms | 1,040,583 | 30,007 |
| SQLite 查询 500 个统计点 | 1.30 ms | 1.29-1.33 ms | 134,523 | 3,036 |

这些结果证明对应算法和数据库路径已有可重复测量基线，但不代表生产服务器的空闲 RSS、HTTP CONNECT / SOCKS5 吞吐或并发连接能力。

## 3. 故障恢复覆盖

复现命令：

```bash
go test ./internal/store ./internal/dataplane/mihomo
```

自动化测试覆盖：

- SQLite WAL：辅助进程先提交状态，再在未提交写事务中被强制终止；重新打开数据库后，已提交状态保持、未提交状态回滚，`integrity_check` 为 `ok`。
- 数据面热重载失败：外部 Mihomo Controller 返回失败后，控制面恢复 `previous.yaml`，并再次请求加载上一版配置。
- 控制面退出隔离：外部数据面模式下关闭 Manager 不会停止或杀死 Mihomo，数据面生命周期由独立 systemd unit 所有。
- 发布升级：安装器固定 Release 版本并验证 SHA-256；双服务 readiness 失败时原子恢复上一版 `current` 链接并重启旧版本。

## 4. 尚未测量

当前环境没有可用的真实 Mihomo 可执行文件和稳定的远端测试链路，因此未测量：

- HTTP CONNECT、SOCKS5、Mixed 的真实吞吐和长短连接延迟。
- 100、1,000、10,000 并发连接下的文件描述符与 RSS。
- 数据面直接配置和 HX 管理配置的吞吐差异。
- 整机断电、磁盘满、干净发行版 VM 安装与重启恢复时间。

发布说明不得从上述控制面微基准推导或编造这些数据。
