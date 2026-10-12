#!/usr/bin/env bash
#
# 阶段 2：在本地 WSL 里把整站跑起来。
#
#   bash scripts/start-local.sh
#
# 依次完成：
#   1. 检查 docker 与 compose 插件可用（并确认用的是 Linux 原生 docker）
#   2. 若 deploy/.env 不存在，从模板生成并填入随机密钥
#   3. 构建镜像并启动 mysql + server
#   4. 轮询 /api/health 直到就绪；失败则打印服务状态与日志尾部
#
# 重复执行安全：已有的 .env 不会被覆盖，镜像层会被复用。

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY_DIR="${REPO_ROOT}/deploy"
ENV_FILE="${DEPLOY_DIR}/.env"
ENV_EXAMPLE="${DEPLOY_DIR}/.env.example"
HEALTH_URL="http://127.0.0.1:8080/api/health"

log()  { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m[!] %s\033[0m\n' "$*"; }
die()  { printf '\033[1;31m[x] %s\033[0m\n' "$*" >&2; exit 1; }

log "1/4 检查环境"
command -v docker >/dev/null || die "找不到 docker，请先执行 scripts/setup-wsl.sh"

# 关键检查：必须用 Linux 原生 docker，而不是 Windows PATH 里那个 docker.exe。
# 用错的话 compose 会连到 Docker Desktop，行为完全不同。
DOCKER_BIN="$(command -v docker)"
case "$DOCKER_BIN" in
  /usr/*) ;;
  *) die "当前 docker 指向 ${DOCKER_BIN}（Windows 版）。请先在 PowerShell 执行 wsl --shutdown 再重进 WSL。" ;;
esac

docker info >/dev/null 2>&1 ||
  die "docker 守护进程不可用。先试 newgrp docker；仍不行就看 systemctl status docker。"

echo "  docker  : $(docker --version)"
echo "  compose : $(docker compose version --short 2>/dev/null || docker compose version | head -1)"

log "2/4 准备 deploy/.env"
if [ -f "$ENV_FILE" ]; then
  echo "  已存在，保持不变：$ENV_FILE"
else
  [ -f "$ENV_EXAMPLE" ] || die "找不到模板 ${ENV_EXAMPLE}"
  command -v openssl >/dev/null || die "找不到 openssl，无法生成随机密钥"

  cp "$ENV_EXAMPLE" "$ENV_FILE"
  # 一律用十六进制随机值：只含 0-9a-f，写进 .env 不需要任何转义，
  # 也不会在 CI 的 heredoc 传递中被 shell 二次解析。
  sed -i "s|^MYSQL_ROOT_PASSWORD=.*|MYSQL_ROOT_PASSWORD=$(openssl rand -hex 32)|" "$ENV_FILE"
  sed -i "s|^MYSQL_PASSWORD=.*|MYSQL_PASSWORD=$(openssl rand -hex 32)|" "$ENV_FILE"
  sed -i "s|^XIANGQI_JWT_SECRET=.*|XIANGQI_JWT_SECRET=$(openssl rand -hex 32)|" "$ENV_FILE"
  chmod 600 "$ENV_FILE"
  echo "  已生成 ${ENV_FILE}"
  echo "  三个密钥均为 openssl rand -hex 32，文件权限 600，已被 .gitignore 忽略"
fi

log "3/4 构建并启动"
echo "  首次会拉取 node / golang / alpine / mysql 基础镜像并编译前后端，请耐心等待"
cd "$DEPLOY_DIR"
docker compose up -d --build

log "4/4 等待健康检查"
for _ in $(seq 1 60); do
  if curl -fsS "$HEALTH_URL" >/dev/null 2>&1; then
    echo
    echo "  站点已就绪"
    docker compose ps
    printf '\n  浏览器打开：http://127.0.0.1:8080\n'
    printf '  接口自检  ：curl %s\n' "$HEALTH_URL"
    printf '  查看日志  ：cd %s && docker compose logs -f server\n' "$DEPLOY_DIR"
    printf '  停止服务  ：cd %s && docker compose down\n' "$DEPLOY_DIR"
    exit 0
  fi
  printf '.'
  sleep 2
done

echo
warn "等待超过 120 秒仍未就绪。下面是服务状态与日志尾部："
docker compose ps || true
docker compose logs --tail=60 || true
die "启动未成功，请把上面的输出贴出来"
