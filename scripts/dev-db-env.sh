#!/usr/bin/env bash
#
# 打印 IDE 调试后端所需的数据库环境变量，直接粘贴到 Run Configuration。
#
#   bash scripts/dev-db-env.sh          # GoLand 的 Environment 格式（分号分隔）
#   bash scripts/dev-db-env.sh json     # VS Code launch.json 的 env 片段
#   bash scripts/dev-db-env.sh export   # shell 的 export 语句，可直接 eval
#
# 为什么需要它：
#   config.Load() 的默认凭据是 root:root@127.0.0.1:3306/xiangqi，
#   而 compose 起的 MySQL，root 密码是 deploy/.env 里 openssl rand -hex 32
#   生成的随机值。两者对不上，IDE 里的后端就会这样死在启动阶段：
#
#     fatal: store: ping server 127.0.0.1:3306: Error 1045 (28000):
#     Access denied for user 'root'@'172.18.0.1' (using password: YES)
#
#   这条报错与端口、防火墙、WSL 端口转发都无关：能收到 MySQL 的 1045，
#   就说明 127.0.0.1:3306 这条路是通的，缺的只是凭据。
#
# 默认用业务账号（MYSQL_USER，通常 xiangqi）而不是 root：
#   * 它是 'xiangqi'@'%'，不挑源地址（docker-proxy 转发后源地址是网桥网关）
#   * 它对 xiangqi.* 有 ALL PRIVILEGES，建表够用
#   * 与容器里 server 的配置一致，少一类"只有我这儿能跑"的问题

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${REPO_ROOT}/deploy/.env"
FORMAT="${1:-goland}"

die() { printf '[x] %s\n' "$*" >&2; exit 1; }

[ -f "$ENV_FILE" ] || die "找不到 ${ENV_FILE}，先执行 bash scripts/start-local.sh"

# 只按行提取，不 source 整个 .env：值里即便将来出现特殊字符，
# 也不会被 shell 二次解析。
get() {
  grep -E "^$1=" "$ENV_FILE" | head -1 | cut -d= -f2- | tr -d '"' | tr -d "'"
}

DB_USER="$(get MYSQL_USER)"
DB_PASS="$(get MYSQL_PASSWORD)"
DB_NAME="$(get MYSQL_DATABASE)"

[ -n "$DB_USER" ] || die "deploy/.env 里没有 MYSQL_USER"
[ -n "$DB_PASS" ] || die "deploy/.env 里没有 MYSQL_PASSWORD"
[ -n "$DB_NAME" ] || DB_NAME=xiangqi

# 提示一律走 stderr，stdout 只留可粘贴的正文，方便 $(...) 取值。
{
  echo "# 来源：${ENV_FILE}"
  echo "# 账号 ${DB_USER} 对 ${DB_NAME}.* 有全部权限；库由 MySQL 容器创建，"
  echo "# 应用侧即便带 -create-db 也能通过：CREATE DATABASE IF NOT EXISTS"
  echo "# 对一个已存在的库不会报权限错。"
} >&2

case "$FORMAT" in
  goland)
    printf 'XIANGQI_DB_HOST=127.0.0.1;XIANGQI_DB_PORT=3306;XIANGQI_DB_USER=%s;XIANGQI_DB_PASSWORD=%s;XIANGQI_DB_NAME=%s\n' \
      "$DB_USER" "$DB_PASS" "$DB_NAME"
    ;;
  json)
    printf '{\n  "XIANGQI_DB_HOST": "127.0.0.1",\n  "XIANGQI_DB_PORT": "3306",\n  "XIANGQI_DB_USER": "%s",\n  "XIANGQI_DB_PASSWORD": "%s",\n  "XIANGQI_DB_NAME": "%s"\n}\n' \
      "$DB_USER" "$DB_PASS" "$DB_NAME"
    ;;
  export)
    printf 'export XIANGQI_DB_HOST=127.0.0.1\nexport XIANGQI_DB_PORT=3306\nexport XIANGQI_DB_USER=%s\nexport XIANGQI_DB_PASSWORD=%s\nexport XIANGQI_DB_NAME=%s\n' \
      "$DB_USER" "$DB_PASS" "$DB_NAME"
    ;;
  *)
    die "未知格式 ${FORMAT}：可用 goland | json | export"
    ;;
esac
