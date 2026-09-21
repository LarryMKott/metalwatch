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
│   └── resource             资源声明
├── cmd/
│   ├── main                 启停脚本：PID 文件 + TERM→KILL
│   ├── install_callback     校验向导参数、建数据目录、渲染配置
│   ├── config_callback      配置变更后自动重启
│   ├── upgrade_callback     停进程 → 快照元数据 → 迁移 → 拉起
│   └── uninstall_init       卸载前备份（可选保留数据）
├── wizard/
│   ├── install              安装向导表单
│   ├── config               配置页表单
│   └── uninstall            卸载确认
└── app/
    ├── ui/config            桌面入口（port 必须与 manifest.service_port 一致）
    └── server/              服务端二进制产物目录
```

## 2. 打包前校验

```bash
python deploy/tools/check_fpk.py
```

校验项：强制文件存在、JSON 合法、**LF 换行**（CRLF 会让脚本在真机执行失败）、
端口三处一致（manifest / app/ui/config / 实际监听）、native 形态断言（无 docker-project）、
`platform` 不得为 `all`。

## 3. 生命周期脚本语义（`cmd/main`）

| 子命令 | 行为 | 退出码 |
| --- | --- | --- |
| `start` | 幂等：已运行则直接成功；拉起后轮询 `/healthz` 就绪 | 0 |
| `stop` | 先 `TERM`，超时后 `KILL` | 0 |
| `status` | 运行中 0 / 未运行 3 / 异常 1 | 见左 |

## 4. 真机验证清单（**尚未执行**）

| # | 验证项 | 判据 |
| --- | --- | --- |
| **V1** | 静态二进制可运行 | `CGO_ENABLED=0` 编译的二进制在飞牛上能启动，无动态链接缺失 |
| **V2** | package 用户写权限 | 以 `run-as: package` 运行时，`TRIM_PKGVAR` 下可读写数据目录 |
| **V3** | 重装数据恢复 | 卸载（选保留）→ 重装 → 资产与配置完整恢复 |
| **V4** | 升级不丢数据 | 0.1.0 → 0.2.0 走 `upgrade_callback` 迁移成功 |
| **V5** | 桌面入口可打开 | 点击图标能打开 WebUI，端口与 manifest 一致 |
| **V6** | 单端口分流生效 | 同一端口既能访问 `/api/v1/*`，也能建立 gRPC 流 |
| **V7** | 资源基线 | 单进程常驻内存 ≤250MB |

V1–V3 是**命门**：任何一项不过，整个交付形态就要重新评估。

## 5. 多架构

包内含原生二进制 → `manifest` 必须写 `platform=x86`。
ARM 设备需另出一个 `platform=arm` 的包，**不可写 `all`**（校验脚本已断言）。
