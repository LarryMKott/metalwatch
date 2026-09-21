# MetalWatch

飞牛 fnOS **原生应用**（FPK）形态的**硬件服务器监控**：跨平台 Agent 采集 + IPMI/BMC 带外采集，
统一在飞牛 OS 上做资产、曲线、告警与报表。

> **架构一句话**：单进程 Go 服务端（Gin + 可插拔存储，默认 SQLite，**所有数据库都支持独立部署**）+ Vue3 前端
> + 跨平台 Agent（HTTP/2 + Protobuf）。交付形态是**无容器的 native FPK**：不用容器、不装数据库、不拉镜像。

**当前状态**：工程骨架已落地并**通过构建与测试**（详见 `docs/06-实测记录.md`）；
业务功能按 `docs/05-开发计划.md` 的 W0–W12 工作流推进。

## 目录

```
MetalWatch/
├── backend/      Go 服务端（Gin；api / internal{service,task,adapter,app} / pkg / proto / migrations）
├── frontend/     Vue3 + TypeScript（Vite + Pinia + vue-router；Node 22/24 双兼容）
├── agent/        跨平台 Agent（仅标准库；linux / windows 分平台实现）
├── deploy/       FPK 工程 + 构建脚本 + 开发用 Docker 环境
└── docs/         01 决策 · 02 模块 · 03 数据 · 04 契约 · 05 计划 · 06 实测记录
```

## 快速开始

```bash
# 后端：构建 / 测试 / 质量门禁（Windows 上若无 make，可逐条执行等价命令）
make build          # 前端构建 → go:embed → CGO_ENABLED=0 交叉编译服务端
make test           # go test ./...
make race           # go test -race ./...（需 CGO/gcc，本机不可用，CI 必跑）
make cover          # 覆盖率报告
make check          # fmt + vet + test + race

# 端到端
make pb             # 生成 protobuf 代码（需 protoc）
make fpk            # 产出飞牛 FPK 安装包（需 fnpack）

# 本地运行
cd backend && go run ./cmd/server serve --config configs/app.yaml --data ./tmp-data
# → http://127.0.0.1:18080  （/healthz 探针、/api/v1/hosts 资产接口）

# 签发 Agent 注册码（明文只出现一次）
cd backend && go run ./cmd/server agent-code --days 7 --uses 1
```

非 make 环境下的等价命令（本机实测可用）：

```bash
export GOPROXY=https://goproxy.cn,direct GOSUMDB=off   # proxy.golang.org 在本网络不可达
go -C backend build ./...
go -C backend test ./...
go -C backend vet ./...
go -C backend run ./cmd/server migrate --data ./tmp-data
```

## 技术栈

| 层 | 选型 | 说明 |
| --- | --- | --- |
| 服务端 | Go 1.23+，Gin 1.12 | `CGO_ENABLED=0` 静态编译；前端产物 `go:embed` |
| 存储（可插拔） | 默认 **SQLite**（`modernc.org/sqlite`，纯 Go）；MySQL / PostgreSQL / GBase8s 支持**独立部署** | 通过 `db.driver` + `db.dsn` 切换，业务代码零改动 |
| 时序（可插拔） | 默认**进程内嵌 TSDB**；Prometheus / VictoriaMetrics / InfluxDB2 支持独立部署 | `timeseries.driver` + `timeseries.endpoint` |
| Agent 通道 | **HTTP/2 + TLS + Protobuf** | 与前端 JSON 通道严格隔离；proto 见 `backend/proto/agent.proto` |
| 前端 | Vue 3.5 + Composition API + TS + Vite 8 + Pinia + vue-router + ECharts 6 | `engines: ">=22.12.0 <25"` |
| 打包 | 飞牛 native FPK（`manifest` + `cmd/` + `wizard/` + `app/server/metalwatch`） | 无容器；`platform=x86`，ARM 另出包 |

## 存储后端现状（`GET /api/v1/system/storage/backends` 返回同一份数据）

| 后端 | 类型 | 部署 | 状态 |
| --- | --- | --- | --- |
| sqlite | 元数据 | 内嵌 | ✅ 可用（默认） |
| postgres / mysql / gbase8s | 元数据 | 独立部署 | 规划中（W1c / W2） |
| embedded | 时序 | 内嵌 | 规划中（W1b） |
| prometheus / victoriametrics / influxdb2 | 时序 | 独立部署 | 规划中（W1c / W2） |

切换到外部数据库时，开发验证环境一键起：`docker compose -f deploy/docker/docker-compose.dev.yml up -d`
（**该 compose 不属于交付形态**，仅用于适配器开发验证）。

## 两处已纠正的规格（避免踩坑）

| 原写法 | 问题 | 正确写法 |
| --- | --- | --- |
| `deploy/fpk/manifest.yml` | 飞牛清单文件是**无扩展名的 INI 文件**，写成 `.yml` 打包校验直接失败 | `deploy/fpk/metalwatch/manifest`（key=value，LF 换行） |
| `engines: { "node": ">=22 <=24" }` | 该区间**排除 24.0.1+**，与「兼容 Node24」意图相反 | `">=22.12.0 <25"` |

## Git 工作流

- 分支：`main` 稳定 / `dev` 日常开发（当前 HEAD 在 `dev`）；提交信息 `类型: 摘要` + 要点列表。
- 换行符：`.gitattributes` 强制全仓库 LF —— `cmd/` 脚本带 CRLF 会在飞牛真机上**执行失败**。自查 `git ls-files --eol`。
- 不入库：`.idea/`、`node_modules/`、`dist/`、`*.fpk`、运行时 `data/`、密钥。
- ⚠️ 本机禁用 `git rm -r`（曾连带删除 `deploy/` 子树下 7 个未修改文件，详见 `.workbuddy/memory/`）。

## 下一步（对应 `docs/05` §9）

1. `make pb` 打通 Protobuf 编解码，Agent 通道由 JSON 兼容切换为二进制；
2. W1b 落地内嵌 TSDB（写入队列 + rollup + retention），补齐 `metalwatch_up` 与曲线查询；
3. W4/W5 采集落地：IPMI 池接入真实 BMC、Linux Agent 采集项补齐（`smartctl` 内置）；
4. W11 鉴权与审计（当前管理接口**尚未接鉴权**，仅限内网使用）；
5. 真机验证 V1–V3（静态二进制可运行 / 进程写权限 / 卸载重装数据恢复）——打包链路命门。
