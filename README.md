# MetalWatch

飞牛 fnOS **原生应用**（FPK）形态的**硬件服务器监控**：跨平台 Agent 采集 + IPMI/BMC 带外采集，
统一在飞牛 OS 上做资产、曲线、告警与报表。

> **架构一句话**：单进程 Go 服务端（Gin + 可插拔存储，默认 SQLite，**所有数据库都支持独立部署**）+ Vue3 前端
> + 跨平台 Agent（gRPC 双向流，与前端 JSON 通道共用单端口分流）。交付形态是**无容器的 native FPK**：不用容器、不装数据库、不拉镜像。

**当前状态**：工程骨架已落地并**通过构建与测试**（详见 `docs/05-记录/01-实测记录.md`）；
业务功能按 `docs/03-计划/01-开发计划.md` 的 W0–W12 工作流推进。

## 目录

```
MetalWatch/
├── backend/      Go 服务端（Gin；api / internal{service,task,adapter,app} / pkg / proto / migrations）
├── frontend/     Vue3 + TypeScript（Vite + Pinia + vue-router；Node 22/24 双兼容）
├── agent/        跨平台 Agent（gRPC 双向流 + bbolt 断网续传；linux / windows 分平台实现）
├── deploy/       FPK 工程 + 构建脚本 + 开发用 Docker 环境
└── docs/         01-架构 · 02-设计 · 03-计划 · 04-部署 · 05-记录（见 docs/README.md）
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

# 本地运行（开发环境：无参数即可，配置 / 数据目录 / 监听地址都走开发默认值）
cd backend && go run ./cmd/server
# → http://127.0.0.1:18080  （/healthz 探针、/api/v1/hosts 资产接口）
#   数据落在 backend/tmp-data，自动拾取 configs/app.yaml，只监听本机

# 生产环境：必须显式给出数据目录，否则拒绝启动
cd backend && go run ./cmd/server serve --data /path/to/data --listen 0.0.0.0:18080

# Windows 一键启动（幂等：缺二进制自动构建 / 端口或进程已占用自动跳过）
deploy\script\start-backend.bat    # 后端 :18080（数据目录 backend\tmp-data）
deploy\script\start-frontend.bat   # 前端 :5173（首次自动 npm install）
deploy\script\start-agent.bat      # 本机 Agent gRPC 流模式（需已注册）

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

## 运行环境（开发 / 生产）

服务端与 Agent 共用同一个全局环境变量 **`METALWATCH_ENV`**（取值 `dev` / `prod`），判定顺序：

| 顺序 | 条件 | 结果 |
| --- | --- | --- |
| 1 | `METALWATCH_ENV=dev\|prod` | 按显式取值；**其他值直接报错**，不静默回退 |
| 2 | 存在 `TRIM_PKGVAR`（飞牛 FPK 注入） | `prod` |
| 3 | 其余（源码目录里直接跑） | `dev` |

**开发环境：各自 main 处无参数直接运行**

```bash
cd backend && go run ./cmd/server        # 配置 configs/app.yaml、数据 tmp-data/、只听 127.0.0.1
cd agent   && go run ./cmd/windows       # spool bin-local/spool，凭据自动读本地
```

Agent 首次接入需注册一次，之后即可无参数启动：

```bash
cd backend && go run ./cmd/server agent-code --days 7   # 签发注册码
cd agent   && go run ./cmd/windows --code <注册码>       # 注册并把凭据写入 agent/bin-local/
```

**生产环境：只认显式注入**

- 服务端必须给出数据目录（`--data` / `METALWATCH_DATA_DIR` / 配置 `server.data_dir`），
  缺失即启动失败；监听所有网卡，由飞牛网关转发；
- Agent 的 `METALWATCH_AGENT_TOKEN` / `METALWATCH_HOST_ID` 必须由服务管理器注入
  （systemd EnvironmentFile / Windows 服务配置），**不读**本地凭据文件；
- FPK 的 `cmd/main` 已显式声明 `METALWATCH_ENV=prod`。

## 技术栈

| 层 | 选型 | 说明 |
| --- | --- | --- |
| 服务端 | Go 1.23+，Gin 1.12 | `CGO_ENABLED=0` 静态编译；前端产物 `go:embed` |
| 存储（可插拔） | 默认 **SQLite**（`modernc.org/sqlite`，纯 Go）；MySQL / PostgreSQL / GBase8s 支持**独立部署** | 通过 `db.driver` + `db.dsn` 切换，业务代码零改动 |
| 时序（可插拔） | 默认**进程内嵌 TSDB**；Prometheus / VictoriaMetrics / InfluxDB2 支持独立部署 | `timeseries.driver` + `timeseries.endpoint` |
| Agent 通道 | **gRPC 双向流 + Protobuf** | 与前端 JSON 通道严格隔离；与 WebUI 共用 18080，按 `Content-Type` 分流（D22）；proto 见 `backend/proto/` |
| 前端 | Vue 3.5 + Composition API + TS + Vite 8 + Pinia + vue-router + ECharts 6 | `engines: ">=22.12.0 <25"` |
| 打包 | 飞牛 native FPK（`manifest` + `cmd/` + `wizard/` + `app/server/metalwatch`） | 无容器；`platform=x86`，ARM 另出包 |

## 存储后端现状（`GET /api/v1/system/storage/backends` 返回同一份数据）

| 后端 | 类型 | 部署 | 状态 |
| --- | --- | --- | --- |
| sqlite | 元数据 | 内嵌 | ✅ 可用（默认） |
| postgres / mysql / gbase8s | 元数据 | 独立部署 | 规划中（W1c / W2） |
| embedded | 时序 | 内嵌 | ✅ 可用（默认，W1c 自研分块压缩） |
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


## 文档

完整文档按类型整理在 `docs/`（[文档中心](docs/README.md)）：

| 目录 | 内容 |
| --- | --- |
| `docs/01-架构/` | 总体架构、关键决策 D1–D30 |
| `docs/02-设计/` | 数据设计、接口契约、通信协议、模块验收 |
| `docs/03-计划/` | 开发计划、**进度看板** |
| `docs/04-部署/` | FPK 打包与真机验证 |
| `docs/05-记录/` | 实测记录 |

## 下一步

进度与缺口以 [`docs/03-计划/02-进度看板.md`](docs/03-计划/02-进度看板.md) 为准
（§6 已知缺口与风险、§7 下一步建议顺序）——此处不再重复维护，以免两处说法漂移。

当前主线：

1. **W12 收尾**：装 `fnpack` 产出 `.fpk` → 上飞牛真机验证三项（静态二进制运行 / package 用户写权限 / 重装数据恢复）；
2. **W15 BMC 管控后端接入**（proto + `pkg/ipmi` + 前端页面已就绪，缺服务端接线）；
3. **W9 巡检报表**（XLSX 优先）。
