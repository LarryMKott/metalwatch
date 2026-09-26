# MetalWatch 项目长期约定

## 项目定位

飞牛 fnOS **原生应用**（FPK，`metalwatch.fpk`）形态的硬件服务器监控。
**架构定型（2026-09-21 起）：单进程 + 嵌入式存储，零外部依赖 —— 不用容器、不装数据库、不拉镜像。**
服务端跑在飞牛 OS 上，Agent 跨平台采集；同时支持 IPMI/BMC 带外采集。工作区 `D:\pj\MetalWatch`。

## 架构硬约束（改动前先读 docs/05）

- **服务端**：Go 1.23+，`CGO_ENABLED=0` 静态编译；前端 `go:embed` 进二进制；HTTP 用标准库。
- **存储**：元数据 SQLite（`modernc.org/sqlite`，**必须免 CGO**）；时序 `prometheus/prometheus/tsdb` 作为库嵌入进程。
- **进程**：`cmd/main` 自管（PID 文件 + `TERM→KILL`），`status` 返回 0=运行 / 3=未运行 / 1=失败；`start` 幂等；启动后 `/healthz` 就绪探测。
- **端口**：manifest 只声明一个 `service_port`（18080），WebUI / 管理 API / Agent 上报 / 桌面入口共用，按路径分流；`manifest.service_port` 与 `app/ui/config` 的 `port` **必须两处一致**。
- **架构声明**：包内含原生二进制 → `platform=x86`（**禁止写 all**）；ARM 需另出 `platform=arm` 包。
- **持久化**：一切数据走 `TRIM_PKGVAR`；脚本与代码禁止硬编码路径，用 `TRIM_*` 变量。

## 设计决策索引

- `docs/01` 是决策唯一入口，最新为 **D1–D17**；D6/D7 已失效（被 D13/D14 取代）。
- `docs/02` 模块 M1–M9 与验收标准；`docs/03` SQLite DDL + 内嵌 TSDB；`docs/04` 接口契约；`docs/05` 开发计划（W0–W12、P1–P5、R1–R10、C1–C4 待拍板）。
- 改动设计必须同步更新对应文档，并在 `docs/01` 追加或修订决策编号。

## 工程约定

- **校验**：改完 FPK 骨架跑 `python deploy/tools/check_fpk.py`（强制文件、JSON、LF 换行、端口一致、native 形态断言、向导字段引用）。
- **换行符**：`.gitattributes` 强制全仓库 LF；`cmd/` 脚本带 CRLF 会在飞牛真机上执行失败。自查 `git ls-files --eol`。
- **不入库**：`.idea/`、`*.fpk`、运行时 `data/`、`logs/`、密钥文件。

## Git 约定

- **托管 GitHub**：`origin = https://github.com/LarryMKott/metalwatch.git`；`main` 稳定 / `dev` 日常开发，HEAD 在 `dev`。
- **Go module 路径统一为 GitHub**：`github.com/LarryMKott/metalwatch`（backend）与 `github.com/LarryMKott/metalwatch/agent`（agent）。改 module 路径后 `.pb.go` 必须用 protoc 重新生成——其 rawDesc 是带长度前缀的序列化描述符，文本替换会破坏编码。
- 提交身份（local config）：`YiDaZhang` / `LarryMKott@users.noreply.github.com`。
- **推送需代理**：直连 `github.com:443` 超时，本仓库 local config 已写 `http.proxy`/`https.proxy = http://127.0.0.1:7890`。
- 提交信息：`类型: 摘要` + 要点列表（`feat`/`fix`/`docs`/`chore`/`refactor`）。
- 提交前必须 `git diff --cached --stat` 核对暂存区，防止夹带无关改动。

## ⚠️ 本机文件/git 操作铁律

- **禁用 `git rm -r`**：2026-09-21 实测它在 `deploy/` 子树下连带删掉 7 个未修改文件（索引未变，工作区被删）。
- **禁用** `git stash push` / `git checkout -b` / `git update-ref`（本机历史事故：refs 被删、文件被真实删除）。
- 建分支用 `git branch <name>` + `git switch <name>`，切换前后做 sha256 清单比对。
- **任何批量删除或树形改动后，立即用 Python 遍历全仓库核对文件清单** —— `git status` 不会报告未跟踪文件被删。
- git 写操作前先整包备份到 `D:\pj\.git-backups\<项目>-<时间戳>`（本项目已有可复用的 `restore_lost_files.py`）。
- 该项目被 PyCharm 打开着（`.idea/` 活跃），操作前留意 IDE 侧是否也在改动工作区。

## 本机环境

Git Bash 的 `mkdir` / `ls` / `grep` / `head` / `tail` / `dirname` / `rm` 时可用时不可用 →
文件操作用托管 Python（`C:\Users\WWTAW\.workbuddy\binaries\python\versions\3.13.12\python.exe`），
搜索用 Grep/Glob 工具，不要用 shell 管道。

## 仓库目录架构（2026-09-21 定案，改动前先读 docs/05 §3）

```
backend/    Go 服务端（独立 module）  api/{web,agentpb} · internal/{service,task,adapter,app} · pkg · proto · migrations · configs
frontend/   Vue3 + TS（Node 22/24 双兼容）  src/{api,components,views,router,store,utils,types,styles}
agent/      跨平台 Agent（独立 module，仅标准库）  cmd/{linux,windows} · internal/{model,collect,report,heartbeat}
deploy/     fpk/metalwatch（FPK 工程）· docker（仅开发验证）· script/{build.sh,pb-gen.sh} · tools/
docs/       01 决策 · 02 模块 · 03 数据 · 04 契约 · 05 计划 · 06 实测记录
```

**分层铁律**：API 层只做参数解析与响应封装 → Service 层业务规则 → Task 层定时/轮询/IPMI/Agent 管理 →
Adapter 层实现全部存储（业务对后端零感知）。`internal/app` 是依赖容器，避免 HTTP 子包互相 import 成环。

## 技术栈定案（docs/01 D18–D21）

- **Web 框架 Gin**（不是标准库 net/http，此前计划已推翻）。
- **存储可插拔**：`internal/adapter` 的 `MetadataStore` / `TimeSeriesStore` 接口 + `Register` 工厂；
  默认 SQLite（`modernc.org/sqlite`，**必须免 CGO**）；MySQL / PostgreSQL / GBase8s /
  Prometheus / VictoriaMetrics / InfluxDB2 均按**独立部署**接入（catalog 标记可用与规划中）。
- **跨方言四条硬约束**：① 时间戳由应用层写 UTC RFC3339；② 不用部分唯一索引（告警去重改 `active_key`）；
  ③ 枚举用 TEXT+CHECK、布尔用 INTEGER 0/1；④ 占位符由 `adapter.Rebind` 转换（sqlite `?` → pg `$N`）。
- **Agent 通道 = HTTP/2 + TLS + Protobuf**（`backend/proto/agent.proto`），与前端 JSON 通道严格隔离；
  proto 生成前两侧用 JSON 兼容通道联调，服务端对 protobuf 请求返回 501 + 指引。
- **前端**：Vue3 + Composition API + TS + Vite 8 + Pinia + vue-router（**hash 模式**）+ ECharts 6（按需引入）；
  `engines: ">=22.12.0 <25"`。文档里的 `">=22 <=24"` 是错的（排除 24.0.1+）。
- 飞牛清单文件是 **无扩展名的 INI `manifest`**，不是 `manifest.yml`。

## 本机工具链坑（本会话新踩，务必遵守）

- **Go 代理**：`proxy.golang.org` 返回 Bad Gateway → 必须 `GOPROXY=https://goproxy.cn,direct` + `GOSUMDB=off`。
- **跑 npm**：`npm` shell 包装器在损坏的 bash 里报 `/usr/bin/env: 'bash': No such file or directory` →
  用 `node.exe <node>/node_modules/npm/bin/npm-cli.js`（工具脚本 `D:\pj\.spikes\tools\npmrun.py`）。
- **Python `write_text` 会写 CRLF**（Windows 默认换行转换）→ 改仓库文本文件后必须复核/统一 LF
  （脚本 `D:\pj\.spikes\tools\fix_line_endings.py`）。
- **沙箱 safe-delete 护栏会拦批量删除**：`rmtree(node_modules/dist)`（>50 文件）抛
  `SAFE_DELETE_BULK_CONFIRM_REQUIRED` → 构建脚本不要自己删 outDir，交给 `vite build --emptyOutDir`。
- **npm 会写出手字面量 `%SystemDrive%` 目录**（受限环境所致）→ 已加入 `.gitignore`，出现即清理。
- **Vite 8 默认 rolldown，`manualChunks` 只接受函数**：对象写法两个 Node 版本同时构建失败。
- **`-race` 需 CGO/gcc**：本机无 gcc 跑不了 → 必须进 CI。
- **验证必须 `go test -count=1`**：默认会命中缓存，`ok ... (cached)` 会掩盖真实失败。
  2026-09-22 提交子代理产出时，全量 test 首轮显示全绿（多数 cached），`-count=1`
  才暴露出 `internal/notify` 编译失败与 `service/inventory` 逻辑缺陷。
- **多包并发跑测试的偶发假失败**：Windows 上偶发 `TempDir RemoveAll cleanup: unlinkat ...
  directory is not empty`（多个包同时建/删临时目录）。不是业务缺陷——单独重复跑验证；
  Linux CI 不会出现（可 unlink 已打开的文件）。
- **平台文件只放平台实现**：把共用函数写进带 `//go:build linux` 的文件会导致 Windows 构建 `undefined`。
- **重构后立刻 `go build ./...`**：漏改 `package` 声明只会在此暴露。

## W11 鉴权与审计（2026-09-22 落地，改动前先读）

- **管理接口 `/api/v1/*` 一律鉴权**（中间件 `api/web/auth.go`，无条件挂载 —— 不要加
  "没主密钥就跳过" 的兜底，那等于静默裸奔）。Agent 通道 `/api/v1/agent/*` 走自己的 Bearer 校验。
- **主体定义放 `internal/authz`**：`api/web` 与 `api/web/handler` 都要读主体，而 web 已 import
  handler，三者不能互引 → 该包存在的唯一理由就是切断这个环，别把 Subject 塞回 web 包。
- **权限判定在中间件**：写操作（非 GET）需 operator+；高危路径（用户 / Token / BMC 凭据 /
  主机增删 / 明文导出）需 admin，按 `method + 路由模板` 精确匹配 `adminOnly` 表。
  ⚠️ **新增高危接口必须手动加进 `adminOnly`**，否则默认是"operator 可写"。
- 会话令牌是**无状态**自签 HMAC（密钥取 master.key，12h）：登出=客户端丢弃，无黑名单表。
- **新表注意 `created_at`**：迁移 DDL **不带 DEFAULT**（跨方言约定），INSERT 必须显式写
  UTC RFC3339 —— 本轮三张表全踩过一次 NOT NULL 失败。

## FPK 构建与验证入口（W12，2026-09-22 定案）

- **构建用 `python deploy/script/build_fpk.py [版本]`**（不是 `build.sh`）。
  `build.sh` 依赖 `dirname`/`npm`/`sed`，本机 Git Bash 会随机失败；Python 版 21s 跑通全流程。
- **真机前先跑 `python deploy/tools/smoke_server.py`**：按 `cmd/main` 同款参数拉起真实进程，
  10 项冒烟（含重启复用数据目录）。它是唯一能抓到 `-trimpath` 产物特有问题（如时区）的手段。
- **二进制内置 tzdata**（`pkg/config` 空导入 `time/tzdata`）：`-trimpath` 会抹掉 GOROOT，
  `time.LoadLocation` 无法回退 zoneinfo.zip，飞牛又不一定有 `/usr/share/zoneinfo`。
  同类"单测全绿但产物必挂"的坑只有实跑能发现。
- 产物 `deploy/fpk/*/app/server/metalwatch` 已被 .gitignore 忽略（30MB，勿入库）。
- **WSL 不可用**：`wsl.exe` 在沙箱黑名单里，Linux 产物本机跑不了 → 用 Windows 产物跑同套代码路径。

## 全量重设计后的架构定案（2026-09-22）

- **Agent 通道 = gRPC 双向流**（`AgentStreamService.Stream/Enroll`），不再是 HTTP/2 + Protobuf 单向上报。
  proto 三件套在 `backend/proto/{agent,bmc,mesh}.proto`，生成代码 `backend/proto/gen`（包名 `mwpb`）。
- **单端口 18080**：gRPC 与 Gin 按 `Content-Type: application/grpc` 分流（飞牛 manifest 只声明一个 service_port，决策 D22）。
- **三大新子系统**：Mesh 最优路径（服务端 Dijkstra 集中计算，D27）、BMC 管控（指令由同网段 Agent 代执行 IPMI，D28）、
  Windows 采集走 C# NativeAOT 组件 + `go:embed`（D26，本机无 .NET SDK 无法编译）。
- **内嵌时序存储自研**：基于已有 SQLite 的分块压缩存储（时间线目录表 + BLOB 块表），
  **不引入 prometheus/tsdb 库**（实测 go get 超 12 分钟未完成，D25）。DDL 在 `backend/migrations/tsdb/0001_init.sql`。
- **引擎层**：`internal/engine/{mesh,schedule,alarm,control}_engine.go` + `pkg/ipmi`，均为纯逻辑可测。
- **前端栈**：Vite 5 + ECharts 5 + Arco Design Vue + Vue 3.5 + Pinia + vue-router(hash)。
- **文档按类型分目录**：`docs/{01-架构,02-设计,03-计划,04-部署,05-记录}/`，入口 `docs/README.md`，
  进度看板在 `docs/03-计划/02-进度看板.md`。

## 子代理协作铁律

子代理报告"已完成"**不等于正确**。本次引擎 + IPMI 子代理产出 `go build` 通过但 3 个测试失败，
其中 1 个是实现 bug（SDR 解析）、1 个是测试断言写反。**必须自己跑 `go build` + `go test` 复核后再采纳。**

## 工具链（装在 D:\\dev）

- `D:\dev\protoc\bin\protoc.exe`（v36.2）
- `D:\dev\gobin\protoc-gen-go.exe`、`protoc-gen-go-grpc.exe`
- 生成命令（在 `backend/proto` 目录下执行）：
  `protoc -I . --go_out=gen --go_opt=paths=source_relative --go-grpc_out=gen --go-grpc_opt=paths=source_relative agent.proto bmc.proto mesh.proto`

## 运行环境约定（D35，2026-09-26 起）

- **全局环境变量 `METALWATCH_ENV`**（`dev` / `prod`），两个 module 共用同一个判定逻辑；
  顺序为「显式取值 > 存在 `TRIM_PKGVAR` 即判 prod > 默认 dev」，非法取值**直接报错不回退**。
  实现在 `backend/pkg/env`（纯标准库），Agent 经既有 `replace` 复用。
- **开发环境无参数直接运行**：`cd backend && go run ./cmd/server`（自动拾取 `configs/app.yaml`、
  数据 `tmp-data/`、只听 127.0.0.1、打印开发横幅）；`cd agent && go run ./cmd/windows`
  （spool 落 `bin-local/spool`、凭据自动读 `bin-local/credentials.json`，注册一次后免参数）。
- **生产环境只认显式注入**：服务端缺数据目录即启动失败；Agent **不读**本地凭据文件，
  靠 systemd EnvironmentFile / Windows 服务配置；FPK `cmd/main` 已 `export METALWATCH_ENV=prod`。
- 默认路径与凭据来源的唯一出处是 `agent/internal/agentcfg`（`report` 包不再自读环境变量，
  hostID 由调用方注入）。凭据文件 `credentials.json` 兼容旧的 `token` + `host_id.txt`。
- **`config.Default()` 的 `DataDir` 是空串**：不要再给它加"看似无害"的默认路径——
  dev 缺失时补 `tmp-data`，prod 缺失时启动失败，这个判断留在 main。
- 判定「用户是否显式传了 flag」用 `flag.Visit`，**不要比较 flag 默认值**（巧合相等会误判）。
- `bin-local/` 已加入 `.gitignore`——内含 Agent 凭据，提交前务必确认未被 `git add`。

## 代码评审结论索引（2026-09-26，全文 `docs/05-记录/02-代码评审-20260926.md`）

对未提交改动全量评审（四路并行子代理 + 逐条回读复核），结论 **2 H / 17 M / 12 L**。

- **两个 H 必须在提交前堵**：① `backend/pkg/env/env_test.go` gofmt 未对齐 → CI `backend`
  job 第一步 `exit 1`（`ci.yml:33-40`），改动整体无法合并；② `.gitignore` 未忽略 `ci-data/`
  与 `*.db` → CI 用的 `backend/ci-data/bootstrap_admin.txt` 是**明文管理员口令**，照 CI 命令
  本地复现后 `git add -A` 即入库。
- **最值得先修的 4 条 M 都是「改了一半」形态**：`alerting.go:401` 漏回填 id（本次只修了
  `Evaluate`）；`start-agent.ps1:33` 仍注入 `host_id=1` 覆盖新逻辑；两个平台 main 写盘失败
  仍报「注册成功」；`credentials.go` 写入非原子且损坏时不降级到 legacy token。
- **已识别的存量缺陷（不在本轮改动内）**：删除主机不做告警侧收尾 + 告警去重键用 hostname
  → `CountFiring` 长期虚高、同名主机重建时告警挂到 `host_id=NULL` 的无主行。
- **复核纠错提醒**：子代理关于 `run_tests.py` 复用旧 JUnit XML 的结论**只对本地成立**
  （CI 全新 checkout 不受影响）；`adminOnly` 的 5 条死条目不构成安全放行。
