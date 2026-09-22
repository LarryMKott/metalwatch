# MetalWatch Agent

部署在被监控物理服务器上的采集端，linux / windows 双平台分入口（build tag 隔离）。

## 传输层

| 模式 | 说明 | 选择 |
| --- | --- | --- |
| `grpc`（默认） | gRPC 双向流（h2c/TLS 自适应）+ bbolt 断网续传 + 指数退避重连（1s→60s 带抖动） | W6 正式通道 |
| `json` | JSON HTTP + 文件式 spool | 排障与旧服务端兼容 |

## 快速开始

```bash
# 1. 注册（一次性注册码换取令牌，令牌同时落盘 <spool 同级>/token）
metalwatch-agent --server http://<server>:18080 --code <注册码> --spool <缓存目录>

# 2. 运行（gRPC 流模式；令牌与 host_id 由注册输出提供）
export METALWATCH_AGENT_TOKEN=mwa_...
export METALWATCH_HOST_ID=1
metalwatch-agent --server http://<server>:18080 --spool <缓存目录> --interval 30s
```

常用 flag：`--transport grpc|json`、`--tls-skip-verify`（自签证书内网选项）、`--interval`（≥10s）。

## 断网续传语义

- 断网期间采集不中断，数据落 `<spool>/spool.db`（bbolt，按采集时间序）；
- 重连成功先按序补传积压（at-least-once），再进入实时；
- 缓存保留 24h（超期丢弃）、容量上限 2 万条（丢最旧）；服务端按 `batch_id` 幂等去重。

## 依赖说明

采集侧零外部进程依赖（Linux 直读 sysfs/procfs，Windows 走 PowerShell CIM）。
传输层依赖 gRPC + bbolt（W6 起引入；「零外部依赖」约束指的是目标机不需要装任何
数据库/容器/工具，与 Go module 依赖无关，见 docs/01 D14）。
