# 三段构建：Node 构建前端 → Go 编译静态二进制 → Alpine 运行。
#
#   docker build -f deploy/Dockerfile -t xiangqi:latest .
#
# 必须在仓库根执行构建，因为上下文要同时包含 web/ 与 server/。

# ---- 第 1 段：构建 Vue 前端 ----
FROM node:26.13.0-alpine AS web
WORKDIR /app/web

# npm 源：默认官方 registry。境内构建慢时用
#   --build-arg NPM_REGISTRY=https://registry.npmmirror.com
# 显式切到镜像 —— 默认走可信通道，换源是明确的构建期选择而不是隐式默认。
# scripts/deploy.sh 会在构建时按需传入镜像地址。
ARG NPM_REGISTRY=https://registry.npmjs.org
RUN npm config set registry "${NPM_REGISTRY}"

# package-lock.json 已在仓库中，用 npm ci 严格按锁文件安装，保证构建可重现。
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY web/ ./
RUN npm run build

# ---- 第 2 段：编译 Go 服务端 ----
FROM golang:1.27.3-alpine AS server
WORKDIR /app/server

# 模块代理：默认官方源，境内不可达时用 --build-arg 显式切换（见 deploy.sh）。
#
# GOSUMDB 必须保留默认值，**不能设成 off**：golang:1.27.3-alpine 镜像自带
# GOTOOLCHAIN=go1.27.3，而镜像里的编译器其实只有 1.26.10，首次构建需要下载
# 1.27.3 工具链；关闭校验会让这个下载被直接拒绝：
#   go: golang.org/toolchain@v0.0.1-go1.27.3.linux-amd64: verifying module:
#   checksum database disabled by GOSUMDB=off
# 第三方代理（goproxy.cn 等）会代理 sum.golang.org，校验仍然成立。
ARG GOPROXY_URL=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY_URL} \
    GOSUMDB=sum.golang.org

# 模块下载单独一层：源码变动时可以复用这一层，不必重新下载。
COPY server/go.mod server/go.sum ./
RUN go mod download

COPY server/ ./
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/xiangqi ./cmd/server

# ---- 第 3 段：运行时 ----
FROM alpine:3.20

# Alpine 的软件源一并换成国内镜像，避免 apk 拉包超时。
# 用发行版自带的官方 apk 源；不替换成镜像，避免依赖链多一个可被替换的环节。
RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -u 10001 app

WORKDIR /app

COPY --from=server /out/xiangqi /app/xiangqi
COPY --from=web /app/web/dist /app/web/dist

# 生产模式：config.Load 会拒绝默认的 JWT 占位密钥，
# 因此运行时必须由外部注入 XIANGQI_JWT_SECRET。
ENV XIANGQI_ADDR=:8080 \
    XIANGQI_PUBLIC_DIR=/app/web/dist \
    XIANGQI_ENV=production \
    XIANGQI_DB_HOST=mysql \
    XIANGQI_DB_PORT=3306 \
    XIANGQI_DB_NAME=xiangqi

EXPOSE 8080
USER app

HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=5 \
  CMD wget -qO- http://127.0.0.1:8080/api/health || exit 1

ENTRYPOINT ["/app/xiangqi"]
