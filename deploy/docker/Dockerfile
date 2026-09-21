# MetalWatch 服务端镜像（仅用于 CI 构建与本地联调）
#
# 注意：交付给飞牛 OS 的形态是 **native FPK（单二进制，无容器）**，
# 本 Dockerfile 不参与 FPK 打包流程，只服务于：
#   1) CI 里产出可复现的构建环境
#   2) 开发机上快速起一个带前端资源的服务端做接口联调
#
# 构建：docker build -f deploy/docker/Dockerfile -t metalwatch-server:dev .

# ---------- 前端构建 ----------
FROM node:22-alpine AS webbuild
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ---------- 服务端构建（静态二进制，与 FPK 内一致） ----------
FROM golang:1.26-alpine AS gobuild
ENV CGO_ENABLED=0 GOOS=linux GOFLAGS=-trimpath
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=webbuild /src/frontend/dist ./internal/web/assets
RUN go build -ldflags "-s -w" -o /out/metalwatch ./cmd/server

# ---------- 运行 ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && adduser -D -u 10001 metalwatch
WORKDIR /app
COPY --from=gobuild /out/metalwatch /app/metalwatch
COPY backend/configs/app.yaml /app/configs/app.yaml
USER 10001
# 默认数据目录（容器内）；生产由 FPK 用 TRIM_PKGVAR 覆盖
ENV METALWATCH_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 18080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s \
  CMD wget -q -O /dev/null http://127.0.0.1:18080/healthz || exit 1
ENTRYPOINT ["/app/metalwatch"]
CMD ["serve", "--config", "/app/configs/app.yaml", "--data", "/data"]
