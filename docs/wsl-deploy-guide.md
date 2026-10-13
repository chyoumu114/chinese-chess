# WSL2 + Docker Compose 本地开发环境搭建手册

> 目标环境：Windows 11 24H2 + WSL2 Ubuntu 24.04
> 目标状态：项目跑在 WSL 内的 Docker Compose 里，IDE 从 Windows 侧实时调试
> 本文覆盖**阶段 0～2**（环境补齐 → WSL 基础 → Compose 编排）。
> 阶段 3～8（IDE 调试、CI/CD、多服务器发布）见后续文档。

---

## 0. 现状与设计原则

### 起点勘察结果

| 组件 | 状态 |
|---|---|
| Windows 11 24H2 (build 26100) | 可用，E 盘空闲 435 GB |
| Git | **未安装** |
| WSL | `wsl.exe` 存在，**无任何发行版**（`Lxss` 注册表为空） |
| Docker Desktop 27.3.1 + Compose v2.29.7 | 已安装，daemon 未运行 |
| Go 1.26.5 / Node 22.18 / npm 10.9.3 | Windows 侧已装 |
| 网络 | `github.com`、`registry-1.docker.io` 直连超时，需代理或国内镜像 |

### 三条设计原则

1. **所有依赖国际网络的环节推到云端或提前缓存。**
   本地日常开发不依赖代理：Gitee 直连 push、Docker 走阿里云加速器、
   镜像构建交给云 CI、服务器从阿里云 ACR 拉取。

2. **代码放在 WSL 的 ext4 里，不放 `/mnt/e`。**
   `web/node_modules` 有几万个小文件，跨 9p 文件系统读写会慢一个数量级。

3. **凭据永不入库。**
   仓库里只有 `.env.example`（占位符）；真实值放本机 `.env`（已 gitignore）、
   CI 的 Secrets、服务器本地的 `.env`。

### 为什么不用 Docker Desktop（决策记录）

选择在 WSL 内安装 **Docker Engine** 而非沿用 Docker Desktop：

- 环境与生产 Linux 服务器一致，运维经验可直接迁移；
- 不经过 Windows 侧的端口转发与虚拟磁盘层，卷挂载性能更好；
- 代价：需手动关掉 Docker Desktop 的 WSL 集成，否则两套 Docker 打架。

---

## 阶段 0：补齐 Git 与 WSL

> 全部在**管理员 PowerShell**中执行。建议本阶段临时打开代理。

### 0.1 探测 WSL 版本

```powershell
wsl --version
wsl --status
wsl -l -v
```

- `wsl --version` 报"命令行选项无效" → 是 Windows 内置旧版，先 `wsl --update`
- 能正常输出版本号 → 直接下一步

### 0.2 安装 WSL 内核 + Ubuntu 24.04

```powershell
wsl --install -d Ubuntu-24.04 --location E:\WSL\Ubuntu-24.04
```

- `--location` 需要 WSL 2.4.4+；不支持则先装默认位置，再迁移：
  ```powershell
  wsl --shutdown
  wsl --manage Ubuntu-24.04 --move E:\WSL\Ubuntu-24.04
  ```
- 该命令会自动启用「虚拟机平台」和「适用于 Linux 的 Windows 子系统」，**完成后需重启电脑**

### 0.3 安装 Git

```powershell
winget install --id Git.Git -e --source winget --accept-package-agreements --accept-source-agreements
```

装完**关闭并重开终端**（刷新 PATH），验证 `git --version`。

### 0.4 重启后初始化 Ubuntu

重启后 Ubuntu 会自动弹出窗口，要求设置 Linux 用户名与密码（输入密码时不回显，正常）。

```powershell
wsl -l -v                                  # 期望：Ubuntu-24.04  Running  2
wsl -d Ubuntu-24.04 -- uname -a
wsl -d Ubuntu-24.04 -- cat /etc/os-release | head -2
```

### 0.5 关闭 Docker Desktop 的 WSL 集成

Docker Desktop → Settings → Resources → **WSL Integration**：

- 关闭 *Enable integration with my default WSL distro*
- 关闭列表内所有发行版开关
- 顺带在 General 里取消 *Start Docker Desktop when you sign in*

> 暂不卸载 Docker Desktop，留作退路；阶段 2 跑通后再决定。

### 0.6（可选但推荐）限制 WSL 资源占用

WSL2 默认最多占用宿主机一半内存。新建 `C:\Users\<你的用户名>\.wslconfig`：

```ini
[wsl2]
memory=8GB
processors=4
swap=4GB
localhostForwarding=true

[experimental]
autoMemoryReclaim=gradual
sparseVhd=true
```

改完执行 `wsl --shutdown` 生效。

---

## 阶段 1：WSL Ubuntu 基础环境

> 以下命令在 **Ubuntu 终端**内执行（`wsl -d Ubuntu-24.04` 进入）。

### 1.1 换用阿里云 apt 源

Ubuntu 24.04 使用 DEB822 格式（`/etc/apt/sources.list.d/ubuntu.sources`）：

```bash
sudo cp /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list.d/ubuntu.sources.bak
sudo sed -i \
  -e 's|http://archive.ubuntu.com/ubuntu|https://mirrors.aliyun.com/ubuntu|g' \
  -e 's|http://security.ubuntu.com/ubuntu|https://mirrors.aliyun.com/ubuntu|g' \
  /etc/apt/sources.list.d/ubuntu.sources
sudo apt update && sudo apt upgrade -y
```

### 1.2 确认 systemd 已启用

Docker Engine 依赖 systemd 管理服务：

```bash
systemctl --version 2>/dev/null && echo "systemd OK" || echo "systemd NOT enabled"
```

若未启用，写入 `/etc/wsl.conf`：

```bash
sudo tee /etc/wsl.conf >/dev/null <<'EOF'
[boot]
systemd=true
EOF
```

然后在 **Windows** 侧执行 `wsl --shutdown`，重新进入 Ubuntu。

### 1.3 安装 Docker Engine（阿里云镜像源）

```bash
sudo apt install -y ca-certificates curl gnupg git make

sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://mirrors.aliyun.com/docker-ce/linux/ubuntu/gpg \
  | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg

echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
https://mirrors.aliyun.com/docker-ce/linux/ubuntu \
$(. /etc/os-release && echo $VERSION_CODENAME) stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io \
                    docker-buildx-plugin docker-compose-plugin

sudo systemctl enable --now docker
```

### 1.4 配置镜像加速器（关键步骤）

> **背景**：国内直连 `registry-1.docker.io` 会超时，而项目需要
> `mysql:8.4`、`node:20-alpine`、`golang:1.24-alpine`、`alpine:3.20` 四个基础镜像。
> 2024 年后大量公共加速器（中科大、网易等）已停止服务，
> **目前最可靠的是阿里云个人专属加速器**。

获取地址：登录阿里云 → 容器镜像服务 ACR → 管理控制台 → 镜像工具 → 镜像加速器，
形如 `https://<你的ID>.mirror.aliyuncs.com`。

```bash
sudo mkdir -p /etc/docker
sudo tee /etc/docker/daemon.json >/dev/null <<'EOF'
{
  "registry-mirrors": ["https://<你的ID>.mirror.aliyuncs.com"],
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" }
}
EOF

sudo systemctl daemon-reload
sudo systemctl restart docker
docker info | grep -A2 "Registry Mirrors"
```

`log-opts` 限制日志体积，避免长期运行把 WSL 虚拟磁盘撑满。

### 1.5 免 sudo 使用 docker

```bash
sudo usermod -aG docker $USER
newgrp docker          # 或退出重进 WSL
docker ps              # 不应再报 permission denied
```

### 1.6 验证

```bash
docker version
docker compose version
docker run --rm hello-world
```

三条全通过，阶段 1 完成。若 `hello-world` 拉取失败，说明加速器未生效，
回到 1.4 检查 `docker info` 里的 Registry Mirrors 输出。

---

## 阶段 2：用 Docker Compose 编排整套服务

### 2.1 现有配置的四个问题

勘察 `deploy/` 后确认的问题，本阶段一并修正：

**问题 1：`docker-compose.yml` 里只有 MySQL，没有 server 服务。**
必须手工 `docker build` + `docker run` 才能起站点，不符合"一条命令起全站"。

**问题 2：密码硬编码在 compose 里。**
`MYSQL_ROOT_PASSWORD: xiangqi-root`、`MYSQL_PASSWORD: xiangqi` 直接写死在
版本控制中，既无法按环境区分，也违反凭据管理原则。

**问题 3（隐蔽，会导致启动失败）：非 root 账号无法建库。**
`server/cmd/server/main.go` 默认以 `create=true` 调用 `store.Open`，
其 `ensureDatabase` 会执行 `CREATE DATABASE IF NOT EXISTS`。
而 compose 用 `MYSQL_USER` 创建的普通账号**没有 CREATE DATABASE 权限**，
即使库已存在也会报 `Access denied`。

> 解法有两条：给 server 用 root（不推荐），或加 `-no-create-db` 参数
> 由 MySQL 容器的 `MYSQL_DATABASE` 负责建库（推荐）。
> 下面的配置采用后者。

**问题 4（构建隐患，已修复）：缺少 `.dockerignore`。**

构建上下文是仓库根（`deploy/Dockerfile` 用 `context: ..` 调用），
实测上下文体积达到 **535.9 MB / 7718 个文件**：

| 目录 | 体积 |
|---|---|
| `.cache`（Go 构建缓存 + npm 缓存） | 373.4 MB |
| `server/.cache` | 112.6 MB |
| `web/node_modules` | 38.5 MB |
| `bin` | 10.4 MB |

两个后果：

1. 每次构建都要把这 536 MB 传给 daemon，其中绝大部分是垃圾数据。
2. 更严重：宿主机的 `web/node_modules` 装的是 **Windows 平台专用包**
   （`@esbuild/win32-x64`、`@rollup/rollup-win32-x64-gnu`、`-msvc`）。
   Dockerfile 里的 `COPY web/ ./`（在 `npm install` **之后**执行）
   会把它带进 Linux 容器，覆盖容器内刚装好的 Linux 版依赖，
   可能让 esbuild / rollup 的原生二进制无法执行，
   导致构建失败或产出行为异常的镜像。

> 已在**仓库根**新增 `.dockerignore` 修复，原因写在文件注释里。
> 注意它必须放在仓库根，不能放 `deploy/`——因为构建上下文是仓库根。
>
> 顺带一提：`web/package-lock.json` 存在，Dockerfile 里的
> `npm install` 可考虑改为 `npm ci`，以获得严格按锁文件安装的可重现构建。

### 2.2 凭据分层模型

```
deploy/.env.example    →  提交入库，只有占位符
deploy/.env            →  本机真实值，被 .gitignore 忽略，永不提交
Gitee / GitHub Secrets →  CI 构建时注入
服务器上的 deploy/.env  →  生产运行时注入，权限 600
```

### 2.3 目标文件结构

```
chinese-chess/
├── .dockerignore                 # 已新增：收窄构建上下文，防止平台依赖污染
└── deploy/
    ├── .env.example              # 已新增：配置模板，可提交
    ├── .env                      # 本机真实值，被 .gitignore 忽略
    ├── Dockerfile                # 已有，无需修改
    ├── docker-compose.yml        # 已改写：mysql + server，全变量化
    └── docker-compose.prod.yml   # 已新增：生产覆盖层，用 !reset 关端口
```

> **`.env` 必须放在 `deploy/`，不能放仓库根。**
> Compose 从「项目目录」加载 `.env`，而项目目录默认就是 compose 文件
> 所在目录。放到仓库根的话，`cd deploy && docker compose up` 读不到它，
> 变量会落到默认值或直接报缺失。
>
> **已实测确认**：清空同名进程环境变量后放入 `deploy/.env`，
> `docker compose config` 仍取到了文件里的值；网络名也正确变为
> `xiangqi_default`（项目名由顶层 `name: xiangqi` 显式指定，
> 否则会按目录名推导成无关的 `deploy_default`）。

变量缺失时由 `${VAR:?错误提示}` 立即报错，避免静默使用错误配置。

### 2.4 `deploy/docker-compose.yml`（开发版）

```yaml
# 本地开发编排：一条命令起全站，配置全部来自仓库根的 .env
#
#   cd deploy && docker compose up -d --build
#
# 变量缺失时立即报错，避免用错配置静默启动。
services:
  mysql:
    image: mysql:8.4
    container_name: xiangqi-mysql
    restart: unless-stopped
    command:
      - --character-set-server=utf8mb4
      - --collation-server=utf8mb4_unicode_ci
    environment:
      MYSQL_ROOT_PASSWORD: ${MYSQL_ROOT_PASSWORD:?缺少 MYSQL_ROOT_PASSWORD}
      MYSQL_DATABASE: ${MYSQL_DATABASE:-xiangqi}
      MYSQL_USER: ${MYSQL_USER:-xiangqi}
      MYSQL_PASSWORD: ${MYSQL_PASSWORD:?缺少 MYSQL_PASSWORD}
    ports:
      # 只绑本机，供 Windows 侧的 IDE 连 127.0.0.1:3306 查数据
      - "${MYSQL_BIND:-127.0.0.1}:${MYSQL_PORT:-3306}:3306"
    volumes:
      - mysql-data:/var/lib/mysql
    healthcheck:
      test: ["CMD", "mysqladmin", "ping", "-h", "127.0.0.1",
             "-uroot", "-p${MYSQL_ROOT_PASSWORD}"]
      interval: 5s
      timeout: 5s
      retries: 30
      start_period: 30s

  server:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
    image: ${XIANGQI_IMAGE:-xiangqi:dev}
    container_name: xiangqi-server
    restart: unless-stopped
    # 库由 MySQL 容器的 MYSQL_DATABASE 创建，应用侧不再尝试建库
    command: ["-no-create-db"]
    environment:
      XIANGQI_ENV: ${XIANGQI_ENV:-development}
      XIANGQI_ADDR: ":8080"
      XIANGQI_PUBLIC_DIR: /app/web/dist
      XIANGQI_DB_HOST: mysql
      XIANGQI_DB_PORT: "3306"
      XIANGQI_DB_NAME: ${MYSQL_DATABASE:-xiangqi}
      XIANGQI_DB_USER: ${MYSQL_USER:-xiangqi}
      XIANGQI_DB_PASSWORD: ${MYSQL_PASSWORD:?缺少 MYSQL_PASSWORD}
      XIANGQI_JWT_SECRET: ${XIANGQI_JWT_SECRET:?缺少 XIANGQI_JWT_SECRET}
      XIANGQI_ALLOWED_ORIGINS: ${XIANGQI_ALLOWED_ORIGINS:-}
      XIANGQI_TURN_TIMEOUT: ${XIANGQI_TURN_TIMEOUT:-0}
      XIANGQI_GAME_TIMEOUT: ${XIANGQI_GAME_TIMEOUT:-0}
    depends_on:
      mysql:
        condition: service_healthy
    ports:
      - "${XIANGQI_PORT:-8080}:8080"
    init: true

volumes:
  mysql-data:
```

### 2.5 `deploy/.env.example`（入库）

> 文件已创建，内容与下方一致。首次使用在同一目录内执行
> `cp .env.example .env`，再填写真实值。

```dotenv
# 复制为 .env 后填写真实值：cp .env.example .env
# .env 已在 .gitignore 中，切勿提交。

# ---- MySQL ----
MYSQL_ROOT_PASSWORD=change-me-root
MYSQL_DATABASE=xiangqi
MYSQL_USER=xiangqi
MYSQL_PASSWORD=change-me-app
# 仅本机可见；设成 0.0.0.0 才能被局域网访问
MYSQL_BIND=127.0.0.1
MYSQL_PORT=3306

# ---- 应用 ----
XIANGQI_PORT=8080
XIANGQI_ENV=development
# 生产环境必须更换，至少 16 字节：openssl rand -hex 32
XIANGQI_JWT_SECRET=dev-insecure-secret-change-me
# 留空表示仅允许同源；填域名用逗号分隔
XIANGQI_ALLOWED_ORIGINS=

# ---- 时钟（0 = 不限时）----
XIANGQI_TURN_TIMEOUT=0
XIANGQI_GAME_TIMEOUT=0

# ---- 镜像 ----
XIANGQI_IMAGE=xiangqi:dev
```

### 2.6 `deploy/docker-compose.prod.yml`（生产覆盖层）

> **⚠️ 本节是修正版，请勿写成 `ports: []`。**
>
> Compose 合并多个文件时，**列表类型（`ports`、`volumes`、`secrets`、`configs`）
> 默认是「追加」而不是「替换」**
> （依据 [Compose 合并规范](https://compose-spec.github.io/compose-spec/13-merge.html)）。
> 所以写 `ports: []` 根本清不掉基础文件里的端口——空列表追加等于什么都没做，
> 3306 照样暴露。这是 Compose 最经典的踩坑点
> （Docker 官方与 [Zulip 部署文档](https://zulip.readthedocs.io/projects/docker/en/latest/how-to/compose-ports.html)
> 都专门记录过）。
>
> 清除继承值必须用特殊 YAML 标签，均要求 **Compose v2.24.4+**：
>
> | 标签 | 作用 | 示例 |
> |---|---|---|
> | `!reset` | 移除该属性，恢复为类型默认值或 `null` | `ports: !reset []` |
> | `!override` | 完全替换该属性，绕过合并规则 | `ports: !override ["8443:443"]` |
>
> 另外注意：基础文件已把 MySQL 端口绑在 `127.0.0.1`，**默认就不对外**，
> 所以生产环境其实不必靠覆盖层来"关端口"——这属于"默认安全"设计。
> 下面的写法只是把意图显式化，避免日后有人改了默认值。

```yaml
# 生产覆盖层：镜像从阿里云 ACR 拉取，且不对外暴露数据库端口
#
#   docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
services:
  mysql:
    # 用 !reset 移除基础文件里的端口声明（官方规范原例就是 !reset []）
    ports: !reset []

  server:
    # 用 !reset 移除继承来的 build 段，只拉取 CI 产出的镜像
    build: !reset null
    image: ${XIANGQI_IMAGE:?生产环境必须指定镜像地址}
    environment:
      XIANGQI_ENV: production
```

> 补充：`!reset` 在 `environment` 的键被多个文件共同贡献时存在已知缺陷
> （[docker/compose#11816](https://github.com/docker/compose/issues/11816)）。
> 本文只用它清除 `ports` 与 `build`，不受该缺陷影响。

### 2.7 启动与验证

```bash
cd ~/projects/chinese-chess/deploy   # .env 必须与 compose 文件同目录
cp .env.example .env
# 用真实随机值替换 JWT 占位密钥
sed -i "s|^XIANGQI_JWT_SECRET=.*|XIANGQI_JWT_SECRET=$(openssl rand -hex 32)|" .env

docker compose up -d --build
docker compose ps                    # 期望两个服务都是 healthy
docker compose logs -f server
```

验证清单：

```bash
curl -s http://127.0.0.1:8080/api/health
curl -s "http://127.0.0.1:8080/api/ai/move?level=2"
```

浏览器打开 <http://127.0.0.1:8080> 应看到棋盘页面。

### 2.8 从 Windows 侧访问

WSL2 自带 localhost 端口转发，Windows 浏览器直接访问 `http://127.0.0.1:8080` 即可。
GoLand 的 Database 工具连 `127.0.0.1:3306`（用户 `xiangqi`）即可查库。

---

## 常见坑速查

| 现象 | 原因 | 处理 |
|---|---|---|
| `docker pull` 超时 | 未配加速器 | 检查 `docker info` 的 Registry Mirrors |
| 加速器返回 **403 Forbidden** | 阿里云已限制**非阿里云主机**调用其镜像加速器 | 换公共源（见下方实测）；阿里云 ECS 上不受此限 |
| manifest 拉得到、**层下载**失败 | 该加速器的对象存储对你不通 | 换源。实测 `docker.m.daocloud.io` 有此问题 |
| push 报 `unknown manifest class for application/vnd.oci.empty.v1+json` | Docker 29 启用 containerd 镜像存储后，BuildKit 默认附带 provenance/SBOM 证明，产出的 manifest 含该 OCI 媒体类型；阿里云 ACR 不认识 | `docker build` 加 `--provenance=false --sbom=false`（`scripts/deploy.sh` 已内置，实测通过） |
| 服务器上拉 `mysql:8.4` 超时 | 服务器没配镜像加速器 | **本地与服务器要用不同的加速器**：同一个阿里云地址在本地 WSL 返回 403、在阿里云 ECS 上返回 200（实测）。两边分别配置 |
| WSL 里 `go`/`node` 只在交互式终端可用 | PATH 写在 `~/.bashrc`，而它开头有"非交互式就 return"的保护，IDE 用非交互方式调用时读不到 | 软链到 `/usr/local/bin`（`sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go`），或写进 `/etc/profile.d/` |
| 在 WSL 里敲 `docker`/`node` 调到了 Windows 版 | `appendWindowsPath` 把整个 Windows PATH 追加进了 Linux PATH | `/etc/wsl.conf` 加 `[interop] appendWindowsPath = false`，`wsl --shutdown` 后生效 |
| `docker: command not found` | 装到了 Windows 的 Docker Desktop | WSL 内用 `/usr/bin/docker`，检查 PATH 是否混入 `/mnt/c/...` |
| `systemctl` 不可用 | WSL 未启用 systemd | 配 `/etc/wsl.conf` 的 `[boot] systemd=true` 后 `wsl --shutdown` |
| server 报 `Access denied ... CREATE DATABASE` | 普通账号无建库权限 | compose 里加 `command: ["-no-create-db"]` |
| server 起不来，日志显示 ping 失败 | MySQL 未就绪 | 依赖 `condition: service_healthy`，勿删 healthcheck |
| `npm install` 极慢 | 代码放在 `/mnt/e` | 迁移到 WSL ext4 的 `~/projects` |
| 磁盘被 WSL 吃满 | docker 日志与镜像堆积 | `daemon.json` 限日志 + `docker system prune` |

---

## 附：镜像加速器实测记录

国内直连 `registry-1.docker.io` 必然超时，必须配加速器。以下是逐项实测结果：

| 加速器 | `/v2/` 状态 | 实际拉取 |
|---|---|---|
| `eup3bnyu.mirror.aliyuncs.com` | **403** | ❌ 阿里云已限制非阿里云主机 |
| `docker.1ms.run` | 401 | ✅ 能拉通，但实测 **78 KB/s**（拉 alpine 3.7 MB 用了 47 秒） |
| `hub.rat.dev` | 302 → `docker.1ms.run` | ⚠️ **同一后端**，换它没有意义 |
| `docker.m.daocloud.io` | 401 | ❌ manifest 能拿，层下载被拒 |
| `docker.nju.edu.cn` | 403 | ─ |
| `docker.mirrors.sjtug.sjtu.edu.cn` | SSL 错误 | ─ |
| `dockerpull.org`、`docker.unsee.tech` | 超时 | ─ |
| `mirror.ccs.tencentyun.com` | DNS 解析失败 | 仅腾讯云内网可用 |

**判读技巧**：registry 的 `/v2/` 返回 **401 是正常的**（要求 token，Docker 客户端会自动处理）；
**403 才是真拒绝**；**000 表示根本连不上**。用一条命令就能快速筛：

```bash
curl -sS -o /dev/null -w "%{http_code}\n" --max-time 8 https://<加速器地址>/v2/
```

**提速建议**：公共加速器带宽共享、限速严重。本地若有代理，让 Docker daemon 走代理直连
官方源通常快得多：

```ini
# /etc/systemd/system/docker.service.d/http-proxy.conf
[Service]
Environment="HTTP_PROXY=http://172.29.0.1:7890"
Environment="HTTPS_PROXY=http://172.29.0.1:7890"
Environment="NO_PROXY=localhost,127.0.0.1"
```

```bash
sudo systemctl daemon-reload && sudo systemctl restart docker
```

三个必须注意的点：

1. **同时移除 `daemon.json` 里的 `registry-mirrors`**，否则 Docker 仍优先走慢速加速器；
2. `172.29.0.1` 是 WSL2 的网关（用 `ip route show default` 查），**WSL 重启后可能变化**；
3. 代理软件必须开启**"允许局域网连接"**（Allow LAN），否则 WSL 连不上它。

> 镜像只需拉一次，之后构建会复用本地缓存。`docker system df` 可查看
> Build Cache 占用（`node`/`golang` 基础镜像的层约 785 MB）——**别急着清**，
> 清了下次构建要重新下载。

---

## 进度

- [x] **阶段 0 完成**：Git 2.56 + WSL2 Ubuntu 24.04（基路径 `E:\WSL\Ubuntu-24.04`，systemd 已启用）
- [x] **阶段 1 完成**：Docker Engine + Compose 插件（一键脚本 `scripts/setup-wsl.sh`）
- [x] **阶段 2 完成**：`docker compose up -d --build` 起站成功
  - `xiangqi-server`、`xiangqi-mysql` 均 healthy
  - `/api/health` → `{"database":"ok",...}`；首页 / SPA / 规则 / 大厅 / AI 五个接口全 200
  - 最终镜像 `xiangqi:dev` 仅 **30.5 MB**（多阶段构建的收益）
  - MySQL 按设计只绑 `127.0.0.1:3306`，外网不可达
- [x] 阶段 3：配置外部化（已并入阶段 2）
- [ ] 阶段 4：IDE 实时调试（**进行中**）—— 见 [ide-debug-guide.md](ide-debug-guide.md)
- [ ] 阶段 5：Git 初始化与 Gitee 托管 —— 见 [ci-cd-guide.md](ci-cd-guide.md)
- [ ] 阶段 6：CI —— Gitee 镜像到 GitHub 并跑测试构建
- [ ] 阶段 7：CD —— 镜像推 ACR、多服务器滚动部署
- [ ] 阶段 8：服务器首装、回滚与密钥轮换

> 阶段 5～8 的配置（工作流、服务器清单、Secrets 清单）均已就绪，
> 待真实仓库与服务器信息填入即可启用。
