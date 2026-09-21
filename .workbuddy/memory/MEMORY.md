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

- 托管 Gitee（命名空间 `zhangyilin_233`，远程地址待补）；`main` 稳定 / `dev` 日常开发，HEAD 在 `dev`。
- 提交身份（local config）：`YiDaZhang` / `YiDaZhang@noreply.gitee.com`（邮箱待用户确认）。
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
- **平台文件只放平台实现**：把共用函数写进带 `//go:build linux` 的文件会导致 Windows 构建 `undefined`。
- **重构后立刻 `go build ./...`**：漏改 `package` 声明只会在此暴露。

