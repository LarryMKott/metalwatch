# MetalWatch 构建入口
#
# 常用：
#   make pb        生成 protobuf 代码
#   make web       构建前端（Vue3 + Vite）
#   make build     构建服务端二进制（前端产物 go:embed 进二进制）
#   make test      跑单测 + 竞态检测
#   make check     格式化 + vet + lint + test 一条龙
#   make pack      产出飞牛 FPK 安装包
#
# 说明：本机（Windows + 受限 shell）下 Makefile 未必可用，等价命令见 README「快速开始」。

SHELL := /bin/bash
ROOT  := $(shell pwd)
BACKEND := $(ROOT)/backend
FRONTEND := $(ROOT)/frontend

VERSION ?= 0.1.0
GOOS    ?= linux
GOARCH  ?= amd64
BIN     := $(BACKEND)/bin/metalwatch
AGENT_BIN_DIR := $(BACKEND)/bin/agent

GOPROXY ?= https://goproxy.cn,direct
export GOPROXY
export GOSUMDB := off

.PHONY: help pb web build build-agent test race cover vet fmt lint check fpk pack run clean tools

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

## 交叉编译 Agent（Linux / Windows）
build-agent:
	cd $(BACKEND) && for t in linux/amd64 linux/arm64 windows/amd64; do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" \
			-o $(AGENT_BIN_DIR)/metalwatch-agent-$$os-$$arch$$ext ./cmd/agent; \
	done
	@ls -l $(AGENT_BIN_DIR)

## 单元测试
test:
	cd $(BACKEND) && go test ./...

## 竞态检测（CI 必跑）
race:
	cd $(BACKEND) && go test -race ./...

## 覆盖率
cover:
	cd $(BACKEND) && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -20

vet:
	cd $(BACKEND) && go vet ./...

fmt:
	cd $(BACKEND) && gofmt -s -w .

## 静态检查（需先安装 golangci-lint）
lint:
	cd $(BACKEND) && golangci-lint run ./...

## 交付前质量门禁
check: fmt vet test race
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
