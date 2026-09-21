#!/bin/bash
# 生成 Agent 通信的 protobuf Go 代码
#
# 依赖：protoc（protobuf 编译器）+ protoc-gen-go
# 用法：bash deploy/script/pb-gen.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PROTO_DIR="$ROOT/backend/proto"
OUT_DIR="$PROTO_DIR/gen"
PROTO_FILE="$PROTO_DIR/agent.proto"

if ! command -v protoc >/dev/null 2>&1; then
  cat <<'EOF' >&2
缺少 protoc。安装方式：
  macOS : brew install protobuf
  Debian: apt-get install -y protobuf-compiler
  Windows: 从 https://github.com/protocolbuffers/protobuf/releases 下载并加入 PATH
EOF
  exit 1
fi

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export GOSUMDB="${GOSUMDB:-off}"

if ! command -v protoc-gen-go >/dev/null 2>&1; then
  echo "==> 安装 protoc-gen-go"
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  export PATH="$PATH:$(go env GOPATH)/bin"
fi

mkdir -p "$OUT_DIR"
echo "==> 生成 $PROTO_FILE"
protoc \
  --proto_path="$PROTO_DIR" \
  --go_out="$OUT_DIR" \
  --go_opt=paths=source_relative \
  "$PROTO_FILE"

echo "==> 产物："
ls -l "$OUT_DIR"
