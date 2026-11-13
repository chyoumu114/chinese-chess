# 部署指南

从一台干净的 Linux 服务器走到 `https://<你的域名>` 可访问。已经部署过的环境，直接看 [§7 日常更新](#7-日常更新与回滚)。

> 更深入的背景材料：[wsl-deploy-guide.md](wsl-deploy-guide.md)（本地 WSL 环境与镜像细节）、
> [ci-cd-guide.md](ci-cd-guide.md)（CI/CD 与阶段化发布）、[ide-debug-guide.md](ide-debug-guide.md)（本机断点调试）。

---

## 0. 架构与前置条件

```
浏览器
  │  https
  ▼
宿主机 nginx  :443 / :80        ← Let's Encrypt 证书
  │  proxy_pass 127.0.0.1:8080（含 WebSocket 透传）
  ▼
docker: xiangqi-server :8080 ──network──▶ xiangqi-mysql :3306（不发布端口）
                                              └─ volume: xiangqi_mysql-data
```

**前置条件**

| 项 | 说明 |
|---|---|
| 云服务器 | Ubuntu 22.04/24.04；本项目在 1.7 GiB 内存上跑得动（MySQL 已调优，见 §6） |
| 域名 | 需要一个域名，且 DNS 的 `A` 记录指向服务器 IP |
| **ICP 备案** | 服务器在**中国大陆**时，未备案的域名访问 `80/443` 会被云厂商在**网络层**拦掉（返回备案提示页）。这跟端口、反代、HTTPS 都无关，必须备案。境外服务器无此要求 |
| 安全组 / 防火墙 | 入方向放行 `22`、`80`、`443`。**`8080` 不要放行**——服务只监听回环，对外只走 nginx |
| 本地环境 | WSL 或 Linux：`git`、`docker`、`node`、`go`（版本见 `server/go.mod` 与 `deploy/Dockerfile`） |

---

## 1. 服务器初始化（一次性）

### 1.1 Docker 与 Compose

```bash
sudo apt install -y ca-certificates curl gnupg
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://mirrors.aliyun.com/docker-ce/linux/ubuntu/gpg \
  | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg

echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
https://mirrors.aliyun.com/docker-ce/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io \
                    docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker
```

建议给 Docker 配上镜像加速器（境内拉 Docker Hub 很慢）：

```bash
sudo mkdir -p /etc/docker
sudo tee /etc/docker/daemon.json >/dev/null <<'EOF'
{
  "registry-mirrors": ["https://docker.1ms.run"],
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" }
}
EOF
sudo systemctl restart docker
```

### 1.2 nginx 与 certbot

```bash
sudo apt install -y nginx certbot python3-certbot-nginx
```

页面反代配置在仓库里（`deploy/nginx/<你的域名>`），`scripts/deploy.sh` 每次部署都会同步过去并 `nginx -t && reload`，**不需要手工维护**。首次签发证书见 §5。

### 1.3 部署专用密钥

```bash
ssh-keygen -t ed25519 -C "xiangqi-deploy" -f ~/.ssh/xiangqi_deploy -N ""
ssh-copy-id -i ~/.ssh/xiangqi_deploy.pub root@<服务器IP>

# 验证免密
ssh -i ~/.ssh/xiangqi_deploy root@<服务器IP> 'echo ok'
```

---

## 2. 本地准备配置（全部不入库）

仓库里只提交 `*.example` 模板，真实配置一律本地保存：

| 文件 | 作用 | 从哪来 |
|---|---|---|
| `deploy/.env` | MySQL 口令、JWT 密钥、端口、Origin 白名单 | `cp deploy/.env.example deploy/.env` |
| `deploy/.env.deploy` | ACR 凭据、部署私钥路径 | `cp deploy/.env.deploy.example deploy/.env.deploy` |
| `deploy/servers.json` | 服务器清单（含公网 IP） | `cp deploy/servers.example.json deploy/servers.json` |

这三个都在 `.gitignore` 里（`deploy/.env.*` 通配 + `deploy/servers.json`），**不会进入版本库**。

### 2.1 `deploy/.env`

```bash
cp deploy/.env.example deploy/.env
cd deploy
# 三个密钥都建议用 openssl 生成，避免特殊字符转义问题
sed -i "s|^MYSQL_ROOT_PASSWORD=.*|MYSQL_ROOT_PASSWORD=$(openssl rand -hex 32)|" .env
sed -i "s|^MYSQL_PASSWORD=.*|MYSQL_PASSWORD=$(openssl rand -hex 32)|" .env
sed -i "s|^XIANGQI_JWT_SECRET=.*|XIANGQI_JWT_SECRET=$(openssl rand -hex 32)|" .env
chmod 600 .env
```

关键项：

| 变量 | 说明 |
|---|---|
| `MYSQL_ROOT_PASSWORD` / `MYSQL_PASSWORD` | MySQL 容器初始化用；换掉后要同步重建数据库或改密 |
| `XIANGQI_JWT_SECRET` | 会话签名密钥，`production` 下拒绝占位值；**换了会让所有登录态失效** |
| `XIANGQI_PORT` | 宿主机回环端口，默认 `8080`（生产层会强制绑 `127.0.0.1`） |
| `XIANGQI_ALLOWED_ORIGINS` | 允许开 WebSocket 的来源，**线上必须填自己的域名**（留空等于放行任意站点，见 §10） |

### 2.2 `deploy/.env.deploy`

```bash
cp deploy/.env.deploy.example deploy/.env.deploy
chmod 600 deploy/.env.deploy
```

| 变量 | 说明 |
|---|---|
| `ACR_REGISTRY` / `ACR_NAMESPACE` | 阿里云 ACR 个人版地址与命名空间 |
| `ACR_USERNAME` / `ACR_PASSWORD` | ACR 登录凭据（控制台里设置固定密码） |
| `DEPLOY_SSH_KEY` | 部署私钥**绝对路径**，如 `/home/you/.ssh/xiangqi_deploy`（写 `~` 也行，脚本会展开） |

### 2.3 `deploy/servers.json`

```json
[
  { "name": "prod-1", "host": "<服务器IP>", "port": 22,
    "user": "root", "app_dir": "/opt/xiangqi" }
]
```

多台服务器就是往数组里多加一项，`deploy.sh` 会逐台串行更新、失败不中断。

---

## 3. 第一次发布

```bash
cd ~/projects/chinese-chess
bash scripts/deploy.sh v0.1.0
```

它依次做五件事：

1. 检查 docker / 私钥 / python3；
2. `docker login` ACR；
3. 本地构建镜像（多阶段：Node 构建前端 → Go 编译二进制 → Alpine 运行）；
4. `docker push` 到 ACR；
5. 逐台服务器：`mkdir` → `scp` 编排文件 + nginx 配置 → 生成服务器端 `.env` → `docker compose pull/up -d` → 健康检查（最多 60 秒）。

**服务器端 `.env` 是自动生成的**：从 `deploy/.env.prod`（没有就用 `deploy/.env`）派生，但会强制覆盖 `XIANGQI_IMAGE`（指向本次 tag）和 `XIANGQI_ENV=production`，避免忘了改镜像地址而部署了旧版本。

只想构建推送、暂不部署：`bash scripts/deploy.sh v0.1.0 --no-deploy`。
只更新某一台：`bash scripts/deploy.sh v0.1.0 --server prod-1`。

> 构建注意：`deploy/Dockerfile` 里的 `GOSUMDB` **必须保留默认值**。镜像自带 `GOTOOLCHAIN=go1.27.3`，
> 首次构建要下载该工具链；设成 `off` 会让下载被拒（`verifying module: checksum database disabled`）。

---

## 4. 服务器目录结构

```
/opt/xiangqi/deploy/
├── docker-compose.yml          # 由 deploy.sh 分发
├── docker-compose.prod.yml     # 生产覆盖层：只拉镜像、8080 绑回环
├── .env                        # 自动生成，chmod 600
└── /etc/nginx/sites-available/<你的域名>   # 站点反代（deploy.sh 同步）
```

数据在 Docker 卷 `xiangqi_mysql-data`，`docker compose down`（不加 `-v`）不会丢。

---

## 5. nginx 与 HTTPS

`deploy/nginx/<你的域名>` 里已经写好了：80/443 监听、`proxy_pass 127.0.0.1:8080`、
以及 **WebSocket 透传**（`Upgrade` / `Connection` 头 + 长 `proxy_read_timeout`）。
这一层缺了 `Upgrade` 头，表现是「大厅能打开、进对局连不上」。

首次签发证书（需要 80 端口可达）：

```bash
sudo certbot --nginx -d <你的域名> \
  --non-interactive --agree-tos --register-unsafely-without-email --redirect
```

- `--redirect` 会把 80 自动跳 443；
- certbot 会修改站点文件（追加 443 段），所以仓库里的副本**要包含 443 段**，否则下次部署覆盖后会掉 HTTPS；
- 续期由 certbot 的 systemd timer 自动完成，`systemctl list-timers | grep certbot` 可查。

---

## 6. 内存调优（小机器必看）

`mysql:8.4` 的默认值是按大机器给的，1.7 GiB 的机器上它常驻约 **450 MiB**。仓库的 compose 已经收紧了：

```yaml
      - --performance-schema=OFF        # 常驻 150–200MB，数据只有几 MB 时纯浪费
      - --innodb-buffer-pool-size=64M
      - --innodb-log-buffer-size=16M
      - --max-connections=50
```

效果：约 **476 MiB → 152 MiB**。代价见下方「监控」——`performance_schema` 关掉后，按 SQL 指纹的慢查询排行就没了，需要慢查询日志兜底：

```sql
SET GLOBAL slow_query_log = ON;
SET GLOBAL long_query_time = 1;
```

建议同时加 swap（1–2 GiB），否则内存触顶时会被 OOM 直接杀进程：

```bash
sudo fallocate -l 2G /swapfile && sudo chmod 600 /swapfile
sudo mkswap /swapfile && sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

监控三件事：`Max_used_connections`（>40 要调 `max_connections`）、
`Innodb_buffer_pool_reads / read_requests`（命中率 <95% 要调大 pool）、库大小（>40 MB 调大 pool）。

---

## 7. 日常更新与回滚

### 更新

```bash
git pull
bash scripts/deploy.sh v0.2.4      # 版本号递增
```

### 回滚

镜像按 tag 独立，回滚就是换 tag 重启：

```bash
cd /opt/xiangqi/deploy
sed -i 's|^XIANGQI_IMAGE=.*|XIANGQI_IMAGE=<ACR地址>/<命名空间>/xiangqi:v0.2.3|' .env
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

> 这是改**服务器本地** `.env`，下次部署会被覆盖回新版本。要永久回滚，对旧 commit 重新打 tag 走一遍部署。

### 临时下线（省内存）

```bash
# 1) 站点切维护页（可选，避免 502）：把站点文件换成 return 503 的版本后 reload
# 2) 停容器，保留数据卷
cd /opt/xiangqi/deploy && docker compose -f docker-compose.yml -f docker-compose.prod.yml down
# 3) 恢复
cd /opt/xiangqi/deploy && docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

---

## 8. 备份与恢复

```bash
# 备份（在服务器上）
docker exec xiangqi-mysql sh -c \
  'exec mysqldump -uroot -p"$MYSQL_ROOT_PASSWORD" --single-transaction --routines xiangqi' \
  | gzip > ~/xiangqi-$(date +%Y%m%d).sql.gz

# 恢复
gunzip -c ~/xiangqi-20260101.sql.gz | \
  docker exec -i xiangqi-mysql sh -c 'exec mysql -uroot -p"$MYSQL_ROOT_PASSWORD" xiangqi'
```

---

## 9. GitHub Actions 自动发布（可选）

仓库已带 `.github/workflows/ci.yml`（每次 push 跑测试）与 `release.yml`（打 tag 自动构建 + 部署）。

在 GitHub 仓库的 **Settings → Secrets and variables → Actions** 配齐：

| Secret | 用途 |
|---|---|
| `ACR_REGISTRY` / `ACR_NAMESPACE` / `ACR_USERNAME` / `ACR_PASSWORD` | 登录并推送 ACR |
| `DEPLOY_SSH_KEY` | 部署私钥全文（`cat ~/.ssh/xiangqi_deploy`） |
| `MYSQL_ROOT_PASSWORD` / `MYSQL_PASSWORD` / `XIANGQI_JWT_SECRET` | 生成服务器端 `.env` |

可选变量 `XIANGQI_ALLOWED_ORIGINS` 放在 **Variables** 里（它不是密钥）。

之后发布就是：

```bash
git tag v0.2.4 && git push origin v0.2.4
```

> CI 里 `ci.yml` 用 `go-version-file: server/go.mod` 自动跟随 Go 版本，
> `release.yml` 的服务器清单读 `deploy/servers.json`——那个文件**不在仓库里**，
> 所以 Actions 部署会跳过（只构建镜像）；用 Actions 部署的话要把它放进 Secrets 或改用变量。

---

## 10. 常见问题

| 现象 | 排查方向 |
|---|---|
| **域名访问被拦，返回「Non-compliance ICP Filing」** | 服务器在大陆且域名未备案。这是网络层拦截，换端口/反代/HTTPS 都没用 |
| 页面能打开，进对局连不上（WebSocket 失败） | nginx 缺 `Upgrade`/`Connection` 透传；本仓库配置已包含 |
| 浏览器控制台 `login_required`，刷新后正常 | 登录后没有重建 WebSocket。已在 `store.applySession()` 里 `ws.reconnect()` 修复；若回退请检查 |
| `GOSUMDB=off` 导致构建失败 `verifying module: checksum database disabled` | 见 §3 末尾：必须保留 sumdb |
| 拉基础镜像卡住（`node:22.23.4-alpine` 等） | 配镜像加速器，或 `docker pull docker.1ms.run/library/<image>` 后 `docker tag` 成本地名 |
| MySQL 内存占用 450MB+ | 见 §6；`performance_schema` 是大头 |
| 8080 从公网能访问 | 生产层应绑 `127.0.0.1`；检查 `docker-compose.prod.yml` 的 `ports: !override` 是否生效 |
| 健康检查超时但容器已起 | 云厂商**安全组**没放行 8080（仅本机调试时需要）或容器还在 MySQL 健康等待中 |

---

## 11. 本地开发

```bash
# 只起数据库，后端在 IDE / go run 里跑
cd deploy && docker compose up -d mysql

# 后端（注意凭据：见 docs/ide-debug-guide.md 2.6）
cd server && go test ./... && go run ./cmd/server -store memory

# 前端
cd web && npm install && npm run dev     # http://localhost:5173
```

本地内存模式（`-store memory`）不落库，重启即清空；要复用账号就接上 §2.1 的 MySQL。

---

## 12. 限流与安全响应头

应用自身已带以下防护，无需在 nginx 重复实现（但可以叠加）：

| 项 | 行为 |
|---|---|
| 登录/注册 | 每 IP 20 次/分钟、突发 10，超出返回 429 |
| AI 试探接口 | 每 IP 20 次/分钟、突发 4；全局 4 个并发槽位，满则 503 `engine_busy` |
| 其余 API / WS | 每 IP 180 次/分钟、突发 60 |
| 安全响应头 | CSP、X-Frame-Options、X-Content-Type-Options、Referrer-Policy、Permissions-Policy；HTTPS 下追加 HSTS |
| WebSocket Origin | 配置了 `XIANGQI_ALLOWED_ORIGINS` 时按名单；留空则只允许同源（开发环境额外允许 localhost） |
| 断线判负 | 对局中掉线超过 60 秒判负，宽限期内重连不受影响 |

**`XIANGQI_RATE_LIMIT=off`** 会关闭全部令牌桶，只应在 e2e 与压测时使用：
那些场景从同一个 IP 发起全部请求，正常限流会把它们误判成攻击。
