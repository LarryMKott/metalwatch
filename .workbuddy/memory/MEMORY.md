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
