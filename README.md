# MetalWatch

飞牛 fnOS **原生应用**（FPK）形态的**硬件服务器监控**：跨平台 Agent 采集 OS 内硬件状态 + IPMI/BMC 带外采集，统一在飞牛 OS 上做资产、曲线、告警与报表。

> **架构要点：单进程、零外部依赖。** 一个静态编译的 Go 二进制，前端 `go:embed` 进二进制，元数据存 SQLite 单文件，时序数据存进程内嵌 TSDB —— **不用容器、不装数据库、不拉镜像**，离线内网可部署，常驻内存约 120–250MB。
>
> 状态：**FPK 打包骨架 + 设计文档已就绪；应用源码尚未开始（下一步见 `docs/05` 的 P1 里程碑）。**

## 目录

```
MetalWatch/
├── docs/
│   ├── 05-开发计划.md               ← 先读这个：架构定型、技术选型、W0-W12 工作分解、里程碑与风险
│   ├── 01-架构评审与关键决策.md      ← D1-D17（含飞牛能力核对与三处 PRD 降级项）
│   ├── 02-模块需求拆解与验收.md      ← M1~M9 模块与验收标准
│   ├── 03-数据设计.md               ← SQLite 表结构 DDL + 内嵌时序存储设计 + 容量测算
│   └── 04-接口契约.md               ← Agent 上报协议 / 管理 API / Webhook / 鉴权
└── deploy/
    ├── fpk/metalwatch/              ← 可直接 fnpack build 的 FPK 工程
    │   ├── manifest                 ← 应用身份（platform=x86、service_port=18080）
    │   ├── ICON.PNG / ICON_256.PNG
    │   ├── config/privilege         ← 运行身份：package
    │   ├── config/resource          ← data-share：reports / backups / geoip
    │   ├── cmd/                     ← main（PID 自管）+ install/config/upgrade/uninstall 回调
    │   ├── wizard/                  ← install / config / uninstall 向导表单
    │   └── app/
    │       ├── server/              ← 打包时放入 metalwatch 二进制（见该目录 README）
    │       └── ui/                  ← 飞牛桌面入口配置与图标
    └── tools/
        ├── gen_icons.py             ← 生成应用图标（纯标准库，可重跑）
        └── check_fpk.py             ← 骨架校验：强制校验文件、JSON、LF 换行、端口一致、native 形态断言
```

## 快速开始（打包）

```bash
# 0. 前置：飞牛官方打包工具 fnpack（https://developer.fnnas.com/docs/cli/fnpack/）
#    Windows 下载后无扩展名，重命名为 fnpack.exe

# 1. 校验骨架（改完 cmd/、wizard/、manifest 后必跑）
python deploy/tools/check_fpk.py

# 2. 构建（源码完成后的目标形态，当前尚未实现）
#    make build          # 前端 vite build → go:embed → CGO_ENABLED=0 交叉编译
#    make pack           # 拷贝二进制到 app/server → chmod +x → fnpack build

# 3. 产物 fpk 上传到飞牛「应用中心 → 手动安装」
#    或 SSH 安装：appcenter-cli install-fpk metalwatch.fpk
```

**打包前检查三项**（`check_fpk.py` 已自动覆盖）：

1. `manifest`、`config/privilege`、`config/resource`、`ICON.PNG`、`ICON_256.PNG` 齐全；
2. `manifest.service_port`（18080）= `app/ui/config` 入口 `port`，两处必须一致；
3. `cmd/` 下脚本是 **LF 换行**且有可执行权限（CRLF 会让 `cmd/main` 在真机上直接失败）。

## Git 工作流

- **分支**：`main` 为稳定分支，日常开发提交到 `dev`；阶段稳定后 `dev` 合入 `main`。
- **提交信息**：`类型: 摘要` + 要点列表，类型用 `feat` / `fix` / `docs` / `chore` / `refactor`。
- **换行符**：`.gitattributes` 强制全仓库 LF。自查：`git ls-files --eol`（应全部 `w/lf`）。
- **不入库**：`.idea/`、`*.fpk` 构建产物、运行时 `data/`、`logs/`、密钥文件（见 `.gitignore`）。

远程仓库首次关联（地址填入后执行）：

```bash
git remote add origin <你的 Gitee 仓库地址>
git push -u origin main
git push -u origin dev
```

## 关键设计一句话摘要

- **零外部依赖**：无容器、无外部数据库、无运行时依赖（不引入 `install_dep_apps`），Agent 自带 `smartctl`。
- **单端口分流**：飞牛 manifest 只声明一个 `service_port`，WebUI、管理 API、Agent 上报、桌面入口共用 18080，按路径分流（`/api/v1/agent/*`）。
- **数据落 `TRIM_PKGVAR`**：SQLite、TSDB、密钥、日志全在飞牛持久卷里，重装 FPK 不丢数据。
- **两套存储分工**：会变的关系数据进 SQLite，画曲线的数值进进程内嵌 TSDB；告警判定在内存中做，不用查询语言承担业务逻辑。
- **进程自管**：`cmd/main` 用 PID 文件 + `TERM→KILL` 管理进程，`status` 严格返回 0/3 语义，启动后做 `/healthz` 就绪探测。
- **降级诚实**：「日志进飞牛日志中心」「复用飞牛 RBAC 账号」在官方文档中查不到依据，已降级为应用内实现 + 待真机验证。

## 待真机验证清单（未验证前不要对外宣称）

| # | 事项 | 影响 |
| --- | --- | --- |
| V1 | `CGO_ENABLED=0` 静态二进制在飞牛 OS 上直接可运行（无 glibc 依赖） | 起不来则整套 native 方案要重估 |
| V2 | 进程以 `package` 用户运行，能否读写 `TRIM_PKGVAR` 与共享目录 | 权限不足则数据写不进去 |
| V3 | 卸载「保留数据」→ 重装后资产与曲线是否完整恢复 | 决定是否需要额外备份机制 |
| V4 | 升级流程中 `stop → 迁移 → start` 的时序与端口释放 | 处理不当会出现「端口占用」或双进程 |
| V5 | `curl`/`wget` 是否存在（`cmd/main` 就绪探测有降级路径） | 影响启动判定的准确性 |
| V6 | 飞牛是否允许自定义/改写 `service_port` 并同步桌面入口 | 决定 D3 的「自定义端口」能否开放 |
| V7 | 4GB 内存机型上单进程常驻实测（目标 ≤250MB） | 决定默认采集频率与保留策略 |

## 下一步建议

1. 先做 `docs/05` §9 的前 5 项：Go 骨架 → SQLite 迁移器 → native `cmd/main` 真机验证（含 V1–V3）；
2. 协议冻结（`docs/04` §2）后，服务端与 Agent 并行开发；
3. 打包链路（`build_fpk.sh` + CI 矩阵）在 P1 阶段就跑通，不要留到最后。
