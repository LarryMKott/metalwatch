# MetalWatch

飞牛 fnOS 原生应用（FPK）形态的**硬件服务器监控**：跨平台 Agent 采集 OS 内硬件状态 + IPMI/BMC 带外采集，统一在飞牛 OS 上做资产、曲线、告警与报表。

> 本仓库当前状态：**FPK 工程骨架 + 设计文档已就绪，尚未在真机验证。**

## 目录

```
MetalWatch/
├── docs/
│   ├── 01-架构评审与关键决策.md      ← 先读这个：PRD 的 12 条落地修正（含飞牛能力核对）
│   ├── 02-模块需求拆解与验收.md      ← M1~M9 模块、验收标准、里程碑映射
│   ├── 03-数据库设计.md             ← MySQL 建表语句 + Prometheus 指标模型 + 容量测算
│   └── 04-接口契约.md               ← Agent 上报协议 / 管理 API / Webhook / 鉴权
└── deploy/
    ├── fpk/metalwatch/              ← 可直接 fnpack build 的 FPK 工程
    │   ├── manifest                 ← 应用身份（appname/version/service_port/...）
    │   ├── ICON.PNG / ICON_256.PNG  ← 64 / 256 图标（脚本生成）
    │   ├── config/privilege         ← 运行身份：package
    │   ├── config/resource          ← docker-project + 三个共享目录
    │   ├── cmd/                     ← main / install_callback / config_callback / upgrade_callback / uninstall_init
    │   ├── wizard/                  ← install / config / uninstall 向导表单
    │   └── app/
    │       ├── docker/docker-compose.yaml       ← MySQL + Prometheus + API 三容器
    │       ├── docker/prometheus/prometheus.yml ← 抓取 /internal/metrics
    │       └── ui/config                        ← 桌面入口（端口与 manifest 一致）
    └── tools/gen_icons.py           ← 图标生成（纯标准库，可重跑）
```

## 快速开始

```bash
# 1. 安装飞牛官方打包工具（开发机）
#    文档：https://developer.fnnas.com/docs/cli/fnpack/
#    Windows 下载后无扩展名，重命名为 fnpack.exe

# 2. 校验并打包
cd deploy/fpk/metalwatch
fnpack build

# 3. 产物 metalwatch.fpk 上传到飞牛「应用中心 → 手动安装」
#    或 SSH 安装：appcenter-cli install-fpk metalwatch.fpk
```

**打包前检查三项**（`fnpack build` 会强校验，缺一即失败）：

1. `manifest`、`config/privilege`、`config/resource`、`ICON.PNG`、`ICON_256.PNG` 都存在；
2. `manifest.service_port` = `app/ui/config` 里入口的 `port` = compose 中映射到宿主机的端口（当前统一 **18080**）；
3. `cmd/` 下脚本是 **LF 换行**且有可执行权限（在 Linux/macOS 上用 `chmod +x cmd/*` 后打包最稳）。

容器镜像 `ghcr.io/metalwatch/metalwatch-server:0.1.0` 目前是**占位地址**，需替换为真实镜像后再构建可运行版本。

## 关键设计一句话摘要

- **单端口分流**：飞牛 manifest 只声明一个 `service_port`，所以 WebUI、管理 API、Agent 上报共用一个端口，按路径分流（`/api/v1/agent/*`）。
- **数据落 `TRIM_PKGVAR`**：MySQL、Prometheus TSDB、密钥、日志全在飞牛持久卷里，重装 FPK 不丢数据。
- **两套存储分工**：会变的关系数据进 MySQL，画曲线的数值进 Prometheus；服务端内置 exporter 供 Prometheus 抓取，不让 Agent 直写 TSDB。
- **降级诚实**：「日志进飞牛日志中心」「复用飞牛 RBAC 账号」这两条 PRD 诉求在官方文档中查不到依据，已降级为应用内实现 + 待真机验证。

## 待真机验证清单（未验证前不要对外宣称）

| # | 事项 | 影响 |
| --- | --- | --- |
| V1 | compose 中 `${TRIM_UID}:${TRIM_GID}` 与 `${wizard_*}` 变量插值是否生效 | 不生效则容器以错误身份跑，宿主目录写不进去 |
| V2 | 非 root 容器对 `TRIM_PKGVAR` 下 mysql/prometheus 目录的写权限 | 权限不足则两个存储起不来 |
| V3 | 卸载「保留数据」后重装，资产与曲线是否完整恢复 | 影响是否要额外做备份机制 |
| V4 | 第三方 FPK 能否把日志投递到飞牛日志中心 | 决定 D2 是否可从「降级方案」升级 |
| V5 | `service_port` 能否在安装后被改写并同步到桌面入口 | 决定 D3 的「自定义端口」能否开放 |
| V6 | 飞牛 OS 的 ARM 设备上多架构镜像运行情况 | 决定 `platform=all` 是否成立 |
| V7 | 8GB 内存机型上三容器常驻占用实测 | 决定是否需要 SQLite 轻量方案（D7） |

## 下一步建议

1. 冻结 Agent 上报协议（`docs/04` §2），服务端与 Agent 并行开发；
2. 用真机跑通 V1–V3，这三条是打包链路的命门；
3. 实现 M1 + M9 骨架中「能录入 3 台机器并在 WebUI 看到卡片」的最小闭环，再铺开采集。
