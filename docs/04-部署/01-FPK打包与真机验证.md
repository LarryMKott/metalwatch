# 01 · FPK 打包与真机验证

> 交付形态是**飞牛原生应用（native）**：单进程、无容器运行时、无外部依赖。
> `deploy/docker/` 仅用于本机验证「外接数据库」链路，**不是交付形态**。

## 1. FPK 工程结构

```
deploy/fpk/metalwatch/
├── manifest                 清单（无扩展名 INI，见 D23）
├── ICON.PNG / ICON_256.PNG  应用图标
├── config/
│   ├── privilege            run-as: package
│   └── resource             资源声明（data-share 等）
├── cmd/                     生命周期脚本，**九个必须齐全**（见下）
├── wizard/
│   ├── install              安装向导表单
│   ├── config               配置页表单
│   └── uninstall            卸载确认（是否保留数据）
└── app/
    ├── ui/config            桌面入口（port 必须与 manifest.service_port 一致）
    └── server/              服务端二进制产物目录
```

## 2. cmd/ 的九个生命周期脚本

`cmd/` 下**必须**有九个脚本，`fnpack build` 会逐个检查，缺一个就
`Required file "cmd/xxx" is missing` 打包失败。清单取自 `fnpack create <app>`
生成的官方模板 + [官方文档目录结构](https://developer.fnnas.com/docs/cli/fnpack/)。

| 脚本 | 调用时机 | 本项目做什么 |
| --- | --- | --- |
| `main` | 平台启停应用 | `start` / `stop` / `status`（PID 文件 + `TERM→KILL`） |
| `install_init` | 用户安装**前**（文件尚未解压） | 环境检查：`TRIM_*` 是否注入、端口与向导取值是否合法 |
| `install_callback` | 安装**后** | 建数据目录 → 渲染配置 → 写一次性管理员引导文件 |
| `config_init` | 配置变更**前** | 只校验新取值（此时不落文件） |
| `config_callback` | 配置变更**后** | 重渲染配置并重启进程（原生应用需自行重启才生效） |
| `upgrade_init` | 升级**前** | 前置检查 → 停进程 → 元数据库快照（回滚网） |
| `upgrade_callback` | 升级**后** | 用新二进制跑结构迁移 → 拉起进程 |
| `uninstall_init` | 卸载**前** | 备份元数据到共享目录（此时数据还完整） |
| `uninstall_callback` | 卸载**后** | 按用户选择删除数据；清 PID / socket 残留 |

两条设计约定（都不只是风格问题）：

- **校验放在 `*_init`，落地放在 `*_callback`**。非法取值在变更生效**之前**就被拒绝，
  否则 callback 里渲染到一半失败，会留下「配置写了一半 + 进程已重启」的不一致状态。
- **删除用户数据放在 `uninstall_callback`，不在 `uninstall_init`**。飞牛卸载流程中间
  还会删 `target` / `tmp` / `home` / `etc`，一旦中途失败，在 init 阶段就删掉的数据
  没有任何找回机会；备份则必须在「文件还在、数据完整」的 init 阶段做。
  官方文档也是这么分工的。

## 3. `cmd/main` 的子命令契约

| 子命令 | 行为 | 退出码 |
| --- | --- | --- |
| `start` | 幂等：已运行则直接成功；拉起后轮询 `/healthz` 就绪 | 0 |
| `stop` | 先 `TERM`，超时后 `KILL` | 0 |
| `status` | 运行中 0 / 未运行 3 / 异常 1 | 见左 |

（`0=运行 / 3=未运行` 是平台约定，官方模板同样如此。）

## 4. 打包前校验

```bash
python deploy/tools/check_fpk.py
```

校验项：强制文件存在（含**九个 cmd 脚本**）、JSON 合法、**LF 换行**（CRLF 会让脚本
在真机执行失败）、端口一致（manifest / `app/ui/config` / 实际监听）、native 形态断言
（无 `docker-project`）、`platform` 不得为 `all`、cmd 引用的向导字段都已定义。

## 5. 本机出包

```bash
# fnpack 是飞牛官方打包 CLI，独立二进制、按**开发机**平台分发（不是目标设备）
# 官方下载页：https://developer.fnnas.com/docs/cli/fnpack/
# 本机（Windows）已装到 D:\dev\fnpack\fnpack.exe，调用时把该目录加进 PATH

python deploy/script/build_fpk.py <版本> --skip-frontend \
  --goos linux --goarch amd64 --platform x86 --require-fnpack
python deploy/script/build_fpk.py <版本> --skip-frontend \
  --goos linux --goarch arm64 --platform arm --require-fnpack
```

产物是 `deploy/fpk/metalwatch/metalwatch.fpk`（tar.gz；`app.tgz` 里装着原生二进制）。
`.fpk` 已被 `.gitignore` 忽略，不入库。

三个容易踩的点：

1. **打包脚本会改写 `manifest`** 的 `version=` 与 `platform=`。本机连出两个架构后，
   `platform` 会停在上一次的值（例如 `arm`）——**提交前先看一眼 `git status`**，
   别把架构改动夹带进去（仓库里的默认值是 `platform=x86`）。
2. **产物名固定**：两次打包都叫 `metalwatch.fpk`，所以 `pack()` 会先删旧产物；
   真要留档请手动改名（CI 里由 `collect_release_assets.py` 按架构改名为
   `metalwatch-fpk-x86_64-<版本>.fpk` / `-arm64-`）。
3. **fnpack 打包失败仍可能返回退出码 0**（只打印 `Packing failed. ...`）。
   所以判断成功不能只看退出码，必须同时确认「有 `.fpk` 产出」——
   `build_fpk.py` 已按此判定，不要改回去。

## 6. 真机验证清单（**尚未执行**）

| # | 验证项 | 判据 |
| --- | --- | --- |
| **V1** | 静态二进制可运行 | `CGO_ENABLED=0` 编译的二进制在飞牛上能启动，无动态链接缺失 |
| **V2** | package 用户写权限 | 以 `run-as: package` 运行时，`TRIM_PKGVAR` 下可读写数据目录 |
| **V3** | 重装数据恢复 | 卸载（选保留）→ 重装 → 资产与配置完整恢复 |
| **V4** | 升级不丢数据 | 0.1.0 → 0.2.0 走 `upgrade_init`/`upgrade_callback` 迁移成功 |
| **V5** | 桌面入口可打开 | 点击图标能打开 WebUI，端口与 manifest 一致 |
| **V6** | 单端口分流生效 | 同一端口既能访问 `/api/v1/*`，也能建立 gRPC 流 |
| **V7** | 资源基线 | 单进程常驻内存 ≤250MB |

V1–V3 是**命门**：任何一项不过，整个交付形态就要重新评估。
注意 **V5 目前挡在已知缺口上**：服务端还没有 `go:embed` 前端产物、也没有静态路由，
装完包点图标会得到 404（详见 `02-CI与发布流水线.md` §5）。

## 7. 多架构

包内含原生二进制 → `manifest.platform` 只有 `x86` / `arm` 两个合法值，
**不可写 `all`**（`check_fpk.py` 第 6 组断言）。

| goarch | platform | 目标设备 |
| --- | --- | --- |
| `amd64` | `x86` | x86 飞牛设备（绝大多数） |
| `arm64` | `arm` | ARM 飞牛设备 |

`build_fpk.py` 缺省按 `--goarch` 推导 `--platform`；推导不出来（未知架构）时
**直接报错退出**，不回落 x86 —— 那会产出一个架构标错的包，装到设备上起不来，
而 `check_fpk.py` 只看「有没有二进制」，查不出这种错配。CI 发布会同时出这两个包。
