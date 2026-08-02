# ── 阶段 1：构建管理面板（Vue 3 + Vite）────────────────────────────────────
# 前端产物与架构无关，固定在构建机原生平台上跑，避免在 arm64 上通过 QEMU 模拟
# 执行 npm（那会把这一步拖慢数倍）。
FROM --platform=$BUILDPLATFORM node:22-alpine AS web

WORKDIR /web

# 先只复制清单：依赖未变时这一层可命中缓存，不必因改了组件而重装依赖。
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci

COPY web/ ./
# npm run build 前置执行 scripts/verify.mjs（校验 @/ 导入、图标、i18n 键），
# 任一不通过即构建失败——这类问题留到运行时才暴露的代价高得多。
RUN npm run build

# ── 阶段 2：编译 Go 二进制 ─────────────────────────────────────────────────
# builder 阶段始终运行在构建机原生平台（amd64），用 Go 交叉编译目标平台二进制
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o kiro-go .

# ── 阶段 3：运行镜像 ───────────────────────────────────────────────────────
FROM alpine:latest
RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /app/kiro-go .
# 只带上构建产物：源码与 node_modules 不进运行镜像。
COPY --from=web /web/dist ./web/dist
RUN mkdir -p /app/data

EXPOSE 8080
# Enterprise SSO (Microsoft 365) loopback callback port — see docker-compose.yml.
EXPOSE 3128
VOLUME /app/data

CMD ["./kiro-go"]
