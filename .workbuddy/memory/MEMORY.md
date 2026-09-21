# MetalWatch 项目长期约定

## 项目定位

飞牛 fnOS 原生应用（FPK）形态的硬件服务器监控。服务端以 `metalwatch.fpk` 部署在飞牛 OS，
Agent 跨平台采集；同时支持 IPMI/BMC 带外采集。工作区 `D:\pj\MetalWatch`。

## Git 约定（2026-09-21 确立）

- **托管**：Gitee，命名空间 `zhangyilin_233`（远程仓库地址待补）。
- **提交身份**（仅本仓库 local config）：`user.name = YiDaZhang`，`user.email = YiDaZhang@noreply.gitee.com`（邮箱待用户确认）。
- **分支**：`main` 稳定分支 / `dev` 日常开发分支，HEAD 默认在 `dev`，稳定后 dev 合入 main。
- **提交信息**：`类型: 摘要` + 要点列表；类型用 `feat` / `fix` / `docs` / `chore` / `refactor`。
- **行尾**：`.gitattributes` 强制全仓库 LF（`* text=auto eol=lf` + `**/cmd/* eol=lf`），
  仓库 local `core.autocrlf=false`。原因：`cmd/` 生命周期脚本带 CRLF 会在飞牛真机执行失败。
  自查：`git ls-files --eol` 应全部为 `w/lf`。
- **不入库**：`.idea/`、`*.fpk`、运行时 `data/`、`logs/`、密钥文件。

## 本机 git 避坑（经验，务必遵守）

- 禁用 `git stash push` / `git checkout -b` / `git update-ref`：本机曾出现 refs 被删、磁盘文件被真实删除的事故。
  建分支改用 `git branch <name>` + `git switch <name>`，并在切换前后做 sha256 清单比对。
- 任何 git 写操作前，先做文件系统级备份（本项目小，直接 `shutil.copytree` 到 `D:\pj\.git-backups\`）。
- 用 `git -C <abs-path>`，不要 `cd <dir> && git ...`。
- 提交前必须 `git diff --cached --stat` 核对暂存区，防止夹带无关改动。

## 工程约定

- **单端口**：manifest `service_port` = `app/ui/config` 入口 port = compose 映射端口，三处必须一致（当前 18080）。
- **持久化**：一律走 `TRIM_PKGVAR`；`cmd/` 脚本禁止硬编码路径，用 `TRIM_*` 环境变量。
- **校验**：改完 FPK 骨架跑 `python deploy/tools/check_fpk.py`（JSON 合法性、LF 换行、端口一致性、向导字段引用）。
- **文档**：`docs/01` 架构评审（D1–D12）是设计决策的唯一入口，改动设计需同步更新该文件的决策编号。
- **待验证项**：README 中 V1–V7，其中 V1/V2/V3（compose 变量插值、非 root 容器写权限、重装数据恢复）是打包链路命门。

## 本机环境

Git Bash 的 `mkdir` / `ls` / `grep` / `head` / `tail` / `dirname` / `rm` 时可用时不可用 →
文件操作用托管 Python（`C:\Users\WWTAW\.workbuddy\binaries\python\versions\3.13.12\python.exe`），
搜索用 Grep/Glob 工具，不要用 shell 管道。
