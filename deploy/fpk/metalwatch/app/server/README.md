# app/server（服务端产物目录）

打包时由 `deploy/tools/build_fpk.sh` 写入，**不要手工提交二进制**。

```
app/server/
└── metalwatch        静态编译的 Go 二进制（CGO_ENABLED=0），已通过 go:embed 内嵌前端资源
```

说明：

- `manifest.platform=x86` 表示本包含 x86 原生二进制；ARM 设备需要单独构建 `platform=arm` 的包（同一份源码，`GOARCH=arm64`）。
- 前端构建产物（`web/dist`）通过 `go:embed` 编译进二进制，因此本目录只放一个文件；`app/ui/` 仅保留飞牛桌面入口配置与图标。
- 该目录在仓库中仅由本说明占位，二进制与 `ffpk` 产物均在 `.gitignore` 中排除。
