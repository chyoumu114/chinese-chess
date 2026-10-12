# 三段构建：Node 构建前端 → Go 编译静态二进制 → Alpine 运行。
#
#   docker build -f deploy/Dockerfile -t xiangqi:latest .
#
# 必须在仓库根执行构建，因为上下文要同时包含 web/ 与 server/。

# ---- 第 1 段：构建 Vue 前端 ----
FROM node:20-alpine AS web
WORKDIR /app/web

# npm 源：容器内默认访问 registry.npmjs.org，境内极慢甚至超时。
# 若在境外环境构建，可用
#   --build-arg NPM_REGISTRY=https://registry.npmjs.org
# 换回官方源。
ARG NPM_REGISTRY=https://registry.npmmirror.com
RUN npm config set registry "${NPM_REGISTRY}"

# package-lock.json 已在仓库中，用 npm ci 严格按锁文件安装，保证构建可重现。
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY web/ ./
RUN npm run build

# ---- 第 2 段：编译 Go 服务端 ----
FROM golang:1.24-alpine AS server
WORKDIR /app/server

# 模块代理：默认的 proxy.golang.org 在境内不可达，构建会直接失败。
# 校验和数据库 sum.golang.org 同样依赖境外网络，所以默认关闭 GOSUMDB。
ARG GOPROXY_URL=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY_URL} \
    GOSUMDB=off

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
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories && \
    apk add --no-cache ca-certificates tzdata wget && \
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
