# MetalWatch 构建入口
#
# 常用：
#   make pb        生成 protobuf 代码
#   make web       构建前端（Vue3 + Vite）
#   make build     构建服务端二进制（前端产物 go:embed 进二进制）
#   make test      跑单测（两个 module）
#   make race      竞态检测（两个 module，需 gcc）
#   make check     交付前门禁：格式检查 + vet + 单测 + 竞态
#   make pack      产出飞牛 FPK 安装包
#
# 说明：本机（Windows + 受限 shell）下 Makefile 未必可用，等价命令见 README「快速开始」。

SHELL := /bin/bash
ROOT  := $(shell pwd)
BACKEND := $(ROOT)/backend
AGENT   := $(ROOT)/agent
FRONTEND := $(ROOT)/frontend

VERSION ?= 0.1.0
GOOS    ?= linux
GOARCH  ?= amd64
BIN     := $(BACKEND)/bin/metalwatch
AGENT_BIN_DIR := $(ROOT)/agent/bin

GOPROXY ?= https://goproxy.cn,direct
export GOPROXY
export GOSUMDB := off

.PHONY: help pb web build build-agent test race cover vet fmt fmtcheck lint check fpk pack run clean tools

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

## 生成 protobuf 代码（需要 protoc）
pb:
	bash $(ROOT)/deploy/script/pb-gen.sh

## 构建前端（Node 22 / 24 均可）
web:
	cd $(FRONTEND) && npm ci && npm run build

## 构建服务端（CGO_ENABLED=0 静态编译，供飞牛 OS 直接运行）
build: web
	cd $(BACKEND) && CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
		-o $(BIN) ./cmd/server
	@echo "==> $(BIN)"

## 交叉编译 Agent（Linux / Windows；agent 是独立 module，入口按平台分目录）
build-agent:
	mkdir -p $(AGENT_BIN_DIR)
	cd $(ROOT)/agent && for t in linux/amd64 linux/arm64 windows/amd64; do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" \
			-o $(AGENT_BIN_DIR)/metalwatch-agent-$$os-$$arch$$ext ./cmd/$$os; \
	done
	@ls -l $(AGENT_BIN_DIR)

## 单元测试（两个 module：backend 与 agent 是独立 module）
## -count=1 不能省：默认命中缓存会输出 `ok ... (cached)`，把真实失败掩盖过去。
test:
	cd $(BACKEND) && go test -count=1 ./...
	cd $(AGENT) && go test -count=1 ./...

## 竞态检测（CI 必跑；本机需 CGO/gcc）
## 两个 module 都要跑：agent 的 session.go 里 s.mu 保护着 gRPC 发送路径，
## 只跑 backend 会漏掉它（D41 修的正是那里的并发问题）。
race:
	cd $(BACKEND) && go test -race -count=1 ./...
	cd $(AGENT) && go test -race -count=1 ./...

## 覆盖率
cover:
	cd $(BACKEND) && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -20

vet:
	cd $(BACKEND) && go vet ./...
	cd $(AGENT) && go vet ./...

fmt:
	cd $(BACKEND) && gofmt -s -w .
	cd $(AGENT) && gofmt -s -w .

## 格式检查（只报告、不改写；门禁必须用这个）
## 门禁里跑 fmt（-w）会把不合格的地方先改掉再判通过 —— 绿灯是假绿灯，
## 而且工作区被静默改动，提交时容易夹带。
fmtcheck:
	@out=$$(cd $(BACKEND) && gofmt -s -l .); \
	out2=$$(cd $(AGENT) && gofmt -s -l .); \
	if [ -n "$$out$$out2" ]; then \
		echo "以下文件未格式化（先跑 make fmt）："; \
		printf '%s\n' "$$out" "$$out2" | sed '/^$$/d'; \
		exit 1; \
	fi; \
	echo "格式检查通过（backend + agent）"

## 静态检查（需先安装 golangci-lint）
lint:
	cd $(BACKEND) && golangci-lint run ./...

## 交付前质量门禁（与 CI 的覆盖面保持一致：两个 module）
check: fmtcheck vet test race
	@echo "==> 质量门禁通过"

## 打包飞牛 FPK（native 形态）
fpk: build
	bash $(ROOT)/deploy/script/build.sh

pack: fpk

## 本地运行（开发态，使用临时数据目录）
run:
	cd $(BACKEND) && go run ./cmd/server serve --config ../backend/configs/app.yaml --data /tmp/metalwatch-data

## 安装开发工具
tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

clean:
	rm -rf $(BACKEND)/bin $(BACKEND)/coverage.out $(FRONTEND)/dist
