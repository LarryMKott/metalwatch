#!/bin/bash
# 一键构建飞牛 FPK 安装包（native 形态）
#
# 流程：前端构建 → Go 静态编译 → 落 app/server → 校验骨架 → fnpack 打包
# 用法：bash deploy/script/build.sh [版本号]
#
# 注意：本脚本依赖 dirname / node / npm / sed。若所在环境的 PATH 损坏
# （例如 Git Bash 报 `dirname: command not found`、npm 报 `'bash': No such file`），
# 请改用等价的 Python 版：python deploy/script/build_fpk.py [版本号]
set -euo pipefail

VERSION="${1:-0.1.0}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BACKEND="$ROOT/backend"
FRONTEND="$ROOT/frontend"
FPK="$ROOT/deploy/fpk/metalwatch"
OUT_BIN="$FPK/app/server/metalwatch"

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export GOSUMDB="${GOSUMDB:-off}"

echo "==> [1/5] 构建前端（Node $(node -v)）"
if [ -d "$FRONTEND/node_modules" ]; then
  (cd "$FRONTEND" && npm run build)
else
  (cd "$FRONTEND" && npm ci && npm run build)
fi

echo "==> [2/5] 编译服务端（CGO_ENABLED=0 静态二进制）"
mkdir -p "$(dirname "$OUT_BIN")"
(cd "$BACKEND" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath -ldflags "-s -w -X main.version=$VERSION" \
  -o "$OUT_BIN" ./cmd/server)
chmod +x "$OUT_BIN"

echo "==> [3/5] 校验 FPK 骨架"
python "$ROOT/deploy/tools/check_fpk.py"

echo "==> [4/5] 同步版本号到 manifest"
# 仅替换 version= 行，避免影响其它字段
sed -i "s/^version=.*$/version=$VERSION/" "$FPK/manifest"

echo "==> [5/5] fnpack 打包"
if command -v fnpack >/dev/null 2>&1; then
  (cd "$FPK" && fnpack build)
  ls -lh "$FPK"/*.fpk 2>/dev/null || true
else
  echo "未找到 fnpack，跳过打包。安装方式见 https://developer.fnnas.com/docs/cli/fnpack/" >&2
  echo "骨架已就绪：$FPK（可手动执行 fnpack build）"
fi
