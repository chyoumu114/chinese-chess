#!/usr/bin/env bash
#
# 阶段 1：在 WSL Ubuntu 里准备 Docker 运行环境。
#
#   bash scripts/setup-wsl.sh https://<你的ID>.mirror.aliyuncs.com
#
# 依次完成：
#   1. 把 apt 源换成阿里云
#   2. 添加 Docker 的阿里云软件源
#   3. 安装 Docker Engine 与 Compose 插件
#   4. 写 /etc/docker/daemon.json（镜像加速器 + 容器日志上限）
#   5. 启动 Docker 服务，并把当前用户加入 docker 组
#   6. 关闭 WSL 的 Windows PATH 注入
#
# 每一步都做了幂等判断，重复执行是安全的。
# 镜像加速器地址是个人专属的，因此不写进仓库，由命令行传入。

set -euo pipefail

MIRROR="${1:-}"

UBUNTU_SOURCES="/etc/apt/sources.list.d/ubuntu.sources"
DOCKER_LIST="/etc/apt/sources.list.d/docker.list"
KEYRING="/etc/apt/keyrings/docker.gpg"
DAEMON_JSON="/etc/docker/daemon.json"
WSL_CONF="/etc/wsl.conf"

log()  { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m[!] %s\033[0m\n' "$*"; }
die()  { printf '\033[1;31m[x] %s\033[0m\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- 前置检查
if [ -z "$MIRROR" ]; then
  cat <<'USAGE'
用法：bash scripts/setup-wsl.sh <镜像加速器地址>

  地址从阿里云「容器镜像服务 → 镜像工具 → 镜像加速器」获取，
  形如 https://xxxxx.mirror.aliyuncs.com

  这是个人专属地址，所以不写进仓库，由命令行传入。
USAGE
  exit 1
fi

case "$MIRROR" in
  http://* | https://*) ;;
  *) die "镜像地址必须以 http:// 或 https:// 开头" ;;
esac

grep -qi ubuntu /etc/os-release || die "本脚本只适用于 Ubuntu"
[ "$(id -u)" -ne 0 ] || die "请用普通用户运行（脚本内部会调用 sudo），不要 sudo bash"
command -v sudo >/dev/null || die "找不到 sudo"

log "0/6 环境确认"
echo "  系统   : $(. /etc/os-release && echo "$PRETTY_NAME")"
echo "  用户   : $(id -un)"
echo "  加速器 : $MIRROR"

# ------------------------------------------------------------- 1. apt 源
log "1/6 换用阿里云 apt 源"
if grep -qs "mirrors.aliyun.com" "$UBUNTU_SOURCES"; then
  echo "  已是阿里云源，跳过"
else
  [ -f "${UBUNTU_SOURCES}.bak" ] || sudo cp "$UBUNTU_SOURCES" "${UBUNTU_SOURCES}.bak"
  sudo sed -i \
    -e 's|http://archive.ubuntu.com/ubuntu|https://mirrors.aliyun.com/ubuntu|g' \
    -e 's|http://security.ubuntu.com/ubuntu|https://mirrors.aliyun.com/ubuntu|g' \
    "$UBUNTU_SOURCES"
  echo "  已替换为 mirrors.aliyun.com（原文件备份为 ubuntu.sources.bak）"
fi

sudo apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
  ca-certificates curl gnupg git make

# ------------------------------------------------------- 2. Docker 软件源
log "2/6 添加 Docker 的阿里云软件源"
if [ -f "$DOCKER_LIST" ]; then
  echo "  软件源已存在，跳过"
else
  sudo install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://mirrors.aliyun.com/docker-ce/linux/ubuntu/gpg |
    sudo gpg --dearmor -o "$KEYRING"
  sudo chmod a+r "$KEYRING"

  CODENAME="$(. /etc/os-release && echo "$VERSION_CODENAME")"
  ARCH="$(dpkg --print-architecture)"
  echo "deb [arch=${ARCH} signed-by=${KEYRING}] https://mirrors.aliyun.com/docker-ce/linux/ubuntu ${CODENAME} stable" |
    sudo tee "$DOCKER_LIST" >/dev/null
  echo "  已写入 $DOCKER_LIST（${CODENAME} / ${ARCH}）"
fi
sudo apt-get update -qq

# ---------------------------------------------------------- 3. Docker Engine
log "3/6 安装 Docker Engine 与 Compose 插件"
# 注意：必须确认是 Linux 原生 docker，而不是 Windows PATH 里那个 docker.exe
DOCKER_BIN="$(command -v docker || true)"
if [ -n "$DOCKER_BIN" ] && [ "${DOCKER_BIN#/usr/}" != "$DOCKER_BIN" ]; then
  echo "  已安装：$(docker --version)"
else
  if [ -n "$DOCKER_BIN" ]; then
    warn "当前 docker 指向 $DOCKER_BIN（Windows 版），将安装 Linux 原生版本覆盖"
  fi
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  hash -r
  echo "  已安装：$(/usr/bin/docker --version)"
fi

# ------------------------------------------------------------ 4. daemon.json
log "4/6 写 /etc/docker/daemon.json"
sudo mkdir -p /etc/docker
sudo tee "$DAEMON_JSON" >/dev/null <<EOF
{
  "registry-mirrors": ["${MIRROR}"],
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" }
}
EOF
echo "  已写入镜像加速器；容器日志限制为单文件 10m、最多 3 个"

# ------------------------------------------------------------- 5. 启动服务
log "5/6 启动 Docker 服务"
sudo systemctl enable --now docker >/dev/null 2>&1 || true
sudo systemctl daemon-reload
sudo systemctl restart docker
sleep 2
echo "  服务状态：$(systemctl is-active docker)"

if id -nG "$(id -un)" | tr ' ' '\n' | grep -qx docker; then
  echo "  当前用户已在 docker 组"
else
  sudo usermod -aG docker "$(id -un)"
  echo "  已把 $(id -un) 加入 docker 组"
fi

# --------------------------------------------------------------- 6. wsl.conf
log "6/6 关闭 WSL 的 Windows PATH 注入"
if grep -qs "appendWindowsPath" "$WSL_CONF"; then
  echo "  已配置过，跳过"
else
  printf '\n[interop]\n# 不把 Windows 的 PATH 追加进 WSL，避免 node/npm/go/docker 误用 Windows 版。\n# 需要调用 Windows 程序时用完整路径，例如 /mnt/c/Windows/explorer.exe\nappendWindowsPath = false\n' |
    sudo tee -a "$WSL_CONF" >/dev/null
  echo "  已追加 [interop] appendWindowsPath = false"
fi

# -------------------------------------------------------------------- 验证
log "验证"
echo "  Engine  : $(/usr/bin/docker --version)"
echo "  Compose : $(/usr/bin/docker compose version)"
printf '  加速器  : '
if sudo docker info 2>/dev/null | grep -A1 "Registry Mirrors" | tail -1 | grep -q "$(echo "$MIRROR" | sed 's|https\?://||')"; then
  echo "已生效"
else
  warn "未能从 docker info 确认，稍后可手工执行 docker info | grep -A2 'Registry Mirrors'"
fi

cat <<'NEXT'

------------------------------------------------------------------
接下来还有两件事需要你手工做：

1) 让 docker 组生效（免 sudo）：
     newgrp docker
   或者干脆退出再重进 WSL。

2) 让 appendWindowsPath 生效（PATH 变干净）：
   在 Windows PowerShell 里执行  wsl --shutdown
   然后重新进入 Ubuntu。

最后验证加速器真的能拉到镜像：
     docker run --rm hello-world
------------------------------------------------------------------
NEXT
