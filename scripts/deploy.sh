#!/usr/bin/env bash
#
# 一键部署：构建镜像 → 推送阿里云 ACR → 逐台更新服务器
#
# 用法：
#   bash scripts/deploy.sh v1.0.0                  # 构建 + 推送 + 部署到全部服务器
#   bash scripts/deploy.sh v1.0.0 --no-deploy      # 只构建 + 推送（服务器还没就绪时用）
#   bash scripts/deploy.sh v1.0.0 --server prod-1  # 只部署到指定的一台
#
# 配置放在 deploy/.env.deploy（该文件不入库）：
#   ACR_REGISTRY=registry.cn-hangzhou.aliyuncs.com
#   ACR_NAMESPACE=你的命名空间
#   ACR_USERNAME=你的登录名
#   ACR_PASSWORD=你的固定密码
#   DEPLOY_SSH_KEY=~/.ssh/id_ed25519      # 可选，默认用这个
#
# 服务器清单来自 deploy/servers.json（不含密钥，可入库）：
#   [{"name":"prod-1","host":"1.2.3.4","port":22,"user":"root","app_dir":"/opt/xiangqi"}]
#
# 服务器上的 .env 由本地 deploy/.env.prod 生成（没有则回退 deploy/.env），
# 并自动把 XIANGQI_IMAGE 指向本次推送的镜像、XIANGQI_ENV 设为 production。

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY_DIR="$REPO_ROOT/deploy"
ENV_DEPLOY="$DEPLOY_DIR/.env.deploy"
ENV_PROD="$DEPLOY_DIR/.env.prod"
ENV_DEV="$DEPLOY_DIR/.env"
SERVERS_JSON="$DEPLOY_DIR/servers.json"

TAG=""
DO_DEPLOY=true
ONLY_SERVER=""

log()  { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m  ✓ %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m  ! %s\033[0m\n' "$*"; }
die()  { printf '\033[1;31m  ✗ %s\033[0m\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,26p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

# ---------------------------------------------------------------- 参数解析
while [ $# -gt 0 ]; do
  case "$1" in
    --no-deploy) DO_DEPLOY=false ;;
    --server)    shift; ONLY_SERVER="${1:-}" ;;
    -h|--help)   usage; exit 0 ;;
    -*)          die "未知参数：$1（用 --help 看用法）" ;;
    *)           TAG="$1" ;;
  esac
  shift
done

[ -n "$TAG" ] || die "请提供版本标签，例如：bash scripts/deploy.sh v1.0.0"

# ---------------------------------------------------------------- 配置加载
if [ -f "$ENV_DEPLOY" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$ENV_DEPLOY"
  set +a
else
  die "找不到 $ENV_DEPLOY，请从下面的模板创建：
      ACR_REGISTRY=registry.cn-hangzhou.aliyuncs.com
      ACR_NAMESPACE=你的命名空间
      ACR_USERNAME=你的登录名
      ACR_PASSWORD=你的固定密码"
fi

: "${ACR_REGISTRY:?请设置 ACR_REGISTRY}"
: "${ACR_NAMESPACE:?请设置 ACR_NAMESPACE}"
: "${ACR_USERNAME:?请设置 ACR_USERNAME}"
: "${ACR_PASSWORD:?请设置 ACR_PASSWORD}"

SSH_KEY="${DEPLOY_SSH_KEY:-$HOME/.ssh/id_ed25519}"
# .env 里若写成 "~/.ssh/xxx"（带引号），bash 的 source 不会展开波浪号，
# 会得到一个字面量路径。这里统一补展开，避免调试半天。
SSH_KEY="${SSH_KEY/#\~/$HOME}"
IMAGE="$ACR_REGISTRY/$ACR_NAMESPACE/xiangqi"
IMAGE_TAG="$IMAGE:$TAG"

# ---------------------------------------------------------------- 1. 环境检查
log "1/5 检查环境"
command -v docker >/dev/null || die "找不到 docker"

DOCKER_BIN="$(command -v docker)"
case "$DOCKER_BIN" in
  /usr/*) ok "docker：$DOCKER_BIN" ;;
  *) die "当前 docker 指向 $DOCKER_BIN（Windows 版）。请在 PowerShell 执行 wsl --shutdown 后重进 WSL。" ;;
esac

docker info >/dev/null 2>&1 || die "docker 守护进程不可用，先试 newgrp docker"
ok "镜像将推送到：$IMAGE_TAG"

if [ "$DO_DEPLOY" = true ]; then
  [ -f "$SSH_KEY" ] || die "找不到 SSH 私钥 $SSH_KEY（可在 deploy/.env.deploy 里用 DEPLOY_SSH_KEY 指定）"
  command -v python3 >/dev/null || die "找不到 python3，无法解析 servers.json"
fi

# ---------------------------------------------------------------- 2. 登录仓库
log "2/5 登录镜像仓库 $ACR_REGISTRY"
printf '%s' "$ACR_PASSWORD" | docker login "$ACR_REGISTRY" -u "$ACR_USERNAME" --password-stdin
ok "登录成功"

# ---------------------------------------------------------------- 3. 构建
log "3/5 构建镜像"
# --provenance=false --sbom=false 是必须的，不是可选优化：
# Docker 29 默认启用 containerd 镜像存储，BuildKit 会附带 provenance/SBOM 证明，
# 产出的 manifest 里包含 application/vnd.oci.empty.v1+json 这个媒体类型。
# 阿里云 ACR 不认识它，推送时会报：
#   error from registry: unknown manifest class for application/vnd.oci.empty.v1+json
# 关掉这两个附件后推送正常（已在本机对 ACR 实测通过）。
docker build \
  --provenance=false \
  --sbom=false \
  -f "$DEPLOY_DIR/Dockerfile" \
  -t "$IMAGE_TAG" \
  "$REPO_ROOT"
ok "构建完成：$IMAGE_TAG"

# ---------------------------------------------------------------- 4. 推送
log "4/5 推送镜像"
docker push "$IMAGE_TAG"
ok "已推送"

if [ "$DO_DEPLOY" != true ]; then
  warn "已按 --no-deploy 跳过部署"
  printf '\n  服务器上手动更新时执行：\n'
  printf '    cd /opt/xiangqi/deploy\n'
  printf '    sed -i "s|^XIANGQI_IMAGE=.*|XIANGQI_IMAGE=%s|" .env\n' "$IMAGE_TAG"
  printf '    docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d\n\n'
  exit 0
fi

# ---------------------------------------------------------------- 5. 部署
[ -f "$SERVERS_JSON" ] || die "找不到 $SERVERS_JSON，请从 servers.example.json 复制并填写"

# 服务器端的 .env 来源：优先生产配置，回退开发配置
SRC_ENV="$ENV_PROD"
[ -f "$SRC_ENV" ] || SRC_ENV="$ENV_DEV"
[ -f "$SRC_ENV" ] || die "找不到 $ENV_PROD 或 $ENV_DEV，无法生成服务器端 .env"
warn "服务器端 .env 由 $SRC_ENV 生成（XIANGQI_IMAGE 与 XIANGQI_ENV 会被覆盖）"

deploy_one() {
  local name="$1" host="$2" port="$3" user="$4" app_dir="$5"
  # shellcheck disable=SC2086
  local ssh_opts="-i $SSH_KEY -p $port -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10"

  log "5/5 部署到 $name（$user@$host:$port，目录 $app_dir）"

  ssh $ssh_opts "$user@$host" "mkdir -p '$app_dir/deploy'"

  scp -i "$SSH_KEY" -P "$port" -o StrictHostKeyChecking=accept-new \
    "$DEPLOY_DIR/docker-compose.yml" \
    "$DEPLOY_DIR/docker-compose.prod.yml" \
    "$user@$host:$app_dir/deploy/"

  # nginx 站点配置也纳入仓库：反代与证书的配置不再只能手工维护在服务器上。
  # 分发后由下面的远端脚本建软链并 reload，机器上没有 nginx 时自然跳过。
  # 日志 format 先落地：站点配置引用它，顺序反了 nginx -t 会失败。
  if [ -f "$DEPLOY_DIR/nginx/00-chess-log.conf" ]; then
    scp -i "$SSH_KEY" -P "$port" -o StrictHostKeyChecking=accept-new \
      "$DEPLOY_DIR/nginx/00-chess-log.conf" \
      "$user@$host:/etc/nginx/conf.d/00-chess-log.conf"
  fi
  if [ -f "$DEPLOY_DIR/nginx/xiangqi.example.com" ]; then
    scp -i "$SSH_KEY" -P "$port" -o StrictHostKeyChecking=accept-new \
      "$DEPLOY_DIR/nginx/xiangqi.example.com" \
      "$user@$host:/etc/nginx/sites-available/xiangqi.example.com"
  fi

  # 生成服务器端 .env：沿用源文件的变量，但镜像与运行环境由本次部署决定
  local tmp_env
  tmp_env="$(mktemp)"
  grep -vE '^(XIANGQI_IMAGE|XIANGQI_ENV|MYSQL_IMAGE)=' "$SRC_ENV" > "$tmp_env" || true
  {
    printf 'XIANGQI_IMAGE=%s\n' "$IMAGE_TAG"
    printf 'XIANGQI_ENV=production\n'
    # 服务器若拉不动 Docker Hub（加速器缺某些镜像），
    # 就把 .env.deploy 里的 MYSQL_IMAGE 指向自己的镜像仓库。
    if [ -n "${MYSQL_IMAGE:-}" ]; then
      printf 'MYSQL_IMAGE=%s\n' "$MYSQL_IMAGE"
    fi
  } >> "$tmp_env"
  chmod 600 "$tmp_env"
  scp -i "$SSH_KEY" -P "$port" -o StrictHostKeyChecking=accept-new \
    "$tmp_env" "$user@$host:$app_dir/deploy/.env"
  rm -f "$tmp_env"

  # heredoc 未加引号：$VAR 在本地展开后再交给远端 bash
  ssh $ssh_opts "$user@$host" bash -s <<REMOTE
set -euo pipefail
cd "$app_dir/deploy"
printf '%s' "$ACR_PASSWORD" | docker login "$ACR_REGISTRY" -u "$ACR_USERNAME" --password-stdin
docker compose -f docker-compose.yml -f docker-compose.prod.yml pull
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --remove-orphans
docker image prune -f >/dev/null
docker compose -f docker-compose.yml -f docker-compose.prod.yml ps

# 站点反代配置随之生效（含 HTTPS）。没有 nginx 的机器跳过。
if [ -f /etc/nginx/sites-available/xiangqi.example.com ]; then
  ln -sfn /etc/nginx/sites-available/xiangqi.example.com /etc/nginx/sites-enabled/xiangqi.example.com
  nginx -t && systemctl reload nginx && echo "nginx reloaded"
fi
REMOTE

  ssh $ssh_opts "$user@$host" \
    'for i in $(seq 1 30); do curl -fsS http://127.0.0.1:8080/api/health >/dev/null && exit 0; sleep 2; done; echo "健康检查超时"; exit 1'
  ok "$name 更新完成且健康检查通过"
}

FAILED=0
DEPLOYED=0

# python3 解析 JSON，输出 name|host|port|user|app_dir 每行一条
while IFS='|' read -r name host port user app_dir; do
  [ -n "$name" ] || continue
  if [ -n "$ONLY_SERVER" ] && [ "$name" != "$ONLY_SERVER" ]; then
    continue
  fi
  if deploy_one "$name" "$host" "$port" "$user" "$app_dir"; then
    DEPLOYED=$((DEPLOYED + 1))
  else
    FAILED=$((FAILED + 1))
    warn "$name 部署失败"
  fi
done < <(python3 - "$SERVERS_JSON" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as fh:
    for s in json.load(fh):
        print("|".join([
            s["name"], s["host"], str(s.get("port", 22)),
            s.get("user", "root"), s.get("app_dir", "/opt/xiangqi"),
        ]))
PY
)

log "完成：成功 $DEPLOYED 台，失败 $FAILED 台"
[ "$FAILED" -eq 0 ] || exit 1
