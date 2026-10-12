# CI/CD 篇：从 Gitee 到多服务器自动发布

> 承接 [wsl-deploy-guide.md](wsl-deploy-guide.md)（阶段 0～2：本地环境）。
> 本篇覆盖**阶段 5～8**：代码托管、镜像构建、多服务器发布、运维。
>
> 阶段 3（配置外部化）已在阶段 2 一并落地；阶段 4（IDE 调试）单独成篇。

---

## 0. 架构总览与设计原则

### 为什么是这套组合

你的机器上 `github.com` 与 `registry-1.docker.io` 直连超时，而代理"只在要用时才开"。
所以架构的核心目标是：**把一切依赖国际网络的环节都推到云端，本地开发不需要代理。**

```
本地 WSL                          Gitee 服务器              GitHub 云端            阿里云
────────                          ───────────              ───────────            ──────
git push  ──── 国内直连 ────────►  Gitee 仓库
                                    │
                                    │ 仓库镜像管理（Gitee 服务器去推，本地不参与）
                                    ▼
                                  GitHub 镜像仓库
                                    │
                                    │ push 事件触发
                                    ▼
                                  GitHub Actions ──── 构建镜像 ────►  ACR 镜像仓库
                                    │                                    │
                                    │ SSH 部署（Actions 出网）             │
                                    ▼                                    │
                                  服务器 1 / 2 / N  ◄──── docker pull ────┘
```

每一段的网络归属：

| 环节 | 谁在执行 | 需要本地代理吗 |
|---|---|---|
| `git push` 到 Gitee | 你的 WSL | 不需要（国内直连） |
| Gitee → GitHub 镜像 | Gitee 服务器 | 不需要 |
| Actions 拉代码、跑测试 | GitHub runner | 不需要 |
| 构建镜像、推 ACR | GitHub runner | 不需要 |
| 服务器拉镜像 | 你的服务器 | 不需要（ACR 国内） |

结论：**本地唯一的网络要求是能 push 到 Gitee。**

### 三条设计原则

1. **镜像只构建一次，所有服务器拉同一个 tag。**
   多机版本绝对一致，不存在"这台编译出来的和那台不一样"。

2. **配置的唯一事实来源是仓库 Secrets。**
   服务器上的 `deploy/.env` 由 CI 生成，不手工维护，改密码只改一处。

3. **服务器上不放源码。**
   只有编排文件和 `.env`，镜像从 ACR 拉。

---

## 阶段 5：Git 仓库初始化与 Gitee 托管

### 5.1 本地初始化

```bash
cd ~/projects/chinese-chess

# 确认 .gitignore 已覆盖敏感文件（本项目已配好）
grep -nE '^\.env|^deploy/\.env' .gitignore

git init -b main
git add .
git status --short        # 关键：确认 deploy/.env 不在列表里！
```

> **这一步必须停下来检查**：如果 `git status` 里出现 `.env`，说明
> `.gitignore` 没生效，先把真实凭据撤出来。提交一个密码到远端仓库，
> 即使随后删除，历史里也永远留着。

```bash
git -c user.name="你的名字" -c user.email="你的邮箱" commit -m "初始提交：象棋对战平台"
```

### 5.2 Gitee 建仓并推送

在 Gitee 网页端新建仓库（**建议私有**），然后：

```bash
git remote add origin git@gitee.com:<你的账号>/chinese-chess.git
git push -u origin main
```

用 SSH 而非 HTTPS：免去每次输密码，也方便后续自动化。
若还没配 Gitee 的 SSH 公钥，在 Gitee「设置 → SSH 公钥」里添加 `~/.ssh/id_ed25519.pub`。

### 5.3 分支策略

单人学习项目，保持简单：

| 分支 | 用途 | 触发什么 |
|---|---|---|
| `main` | 稳定可发布 | CI + 打 tag 时发布 |
| `dev` | 日常开发 | CI |
| `feature/*` | 临时特性 | 提 PR 时 CI |

日常在 `dev` 上开发，稳定后合并到 `main`。

---

## 阶段 6：Gitee → GitHub 镜像

### 6.1 为什么需要这一步

Gitee 仓库上跑不了 GitHub Actions。你的诉求是"用 Actions 发布"，而代码
托管又想留在国内（push 快、不用代理）。镜像同步同时满足两者——
而且同步动作由 **Gitee 的服务器**发起，你的本地完全不参与。

### 6.2 准备 GitHub 私人令牌（PAT）

1. 在 GitHub 建一个**空的私有仓库**（名字与 Gitee 上一致，不要勾选任何初始化文件）
2. 访问 GitHub → Settings → Developer settings → Personal access tokens
3. 新建 **classic token**，勾选权限：
   - `repo`（必填，否则镜像不可用）
   - `admin:repo_hook`（勾选后 Gitee 才能自动生成 webhook 实现自动同步）
4. 复制令牌（只显示一次）

### 6.3 在 Gitee 配置镜像

Gitee 仓库 →「管理」→「仓库镜像管理」→「添加镜像」：

| 项 | 填什么 |
|---|---|
| 镜像方向 | **Push**（把 Gitee 推到 GitHub） |
| 镜像仓库 | 选择你的 GitHub 仓库（首次需先绑定 GitHub 账号） |
| 私人令牌 | 上一步的 GitHub PAT |
| 自动同步 | 勾选（否则要手动点「更新」） |

> 参考：[Gitee 官方说明](https://blog.gitee.com/2026/01/23/github-gitee-mirror-sync-one-commit-two-repos/)。

### 6.4 限制（务必知道）

| 限制 | 影响 | 应对 |
|---|---|---|
| 最短触发间隔 5 分钟 | 连续推送不会每次都同步 | 合并提交，别频繁小步推送等同步 |
| 会**覆盖**目标仓库的分支与标签 | GitHub 上的改动会被冲掉 | 永远只在 Gitee 侧改代码 |
| 不支持 Git-LFS | 大文件会失败 | 本项目无 LFS 需求 |
| 超过 30 分钟视为超时 | 超大仓库不可用 | 本项目体积很小 |

**验证方式**：在 Gitee 推一个提交，等约 1 分钟，去 GitHub 看 commit 是否出现。
出现了，就说明主线打通了。

---

## 阶段 7：CI —— 自动跑测试

### 7.1 已落地的文件

[.github/workflows/ci.yml](../.github/workflows/ci.yml)

两个并行 job：

| Job | 内容 | 备注 |
|---|---|---|
| `server` | `go vet ./...` + `go test ./... -count=1` | Go 版本从 `server/go.mod` 读，不需要手工同步 |
| `web` | `npm ci` + `npm test` + `npm run build` | 用 `npm ci` 严格按锁文件安装 |

几个刻意的选择：

- **不需要 MySQL service 容器**：`internal/store` 的集成测试在拿不到数据库时
  会自动跳过，CI 因此更轻更快。
- **`concurrency` 取消旧任务**：同一分支连续推送时，自动取消上一次未跑完的运行。
- **`cache-dependency-path` 用仓库根相对路径**：它不受 job 内
  `working-directory` 影响，写错会让缓存静默失效。

### 7.2 触发方式

- push 到 `main` / `dev`
- 向 `main` 提 PR
- 手动触发（Actions 页面 → CI → Run workflow）

镜像同步过来的 commit 同样会触发，这是整条链路的关键一环。

---

## 阶段 8：CD —— 构建镜像并发布到多台服务器

### 8.1 准备阿里云 ACR

1. 开通「容器镜像服务 ACR」，选**个人版**（免费）
2. 创建命名空间，例如 `xiangqi`
3. 创建镜像仓库，例如 `xiangqi`，类型选**私有**
4. 设置**固定密码**（访问凭证 → 固定密码），记下：
   - 登录名（通常是阿里云账号全名）
   - 固定密码

三个值会分别放进 Secrets：`ACR_REGISTRY`、`ACR_NAMESPACE`、`ACR_USERNAME`、`ACR_PASSWORD`。

> 同一个控制台里还有「镜像加速器」，就是阶段 1 需要的
> `https://<你的ID>.mirror.aliyuncs.com`，顺手一起取走。

### 8.2 服务器清单

复制模板并填写真实地址：

```bash
cp deploy/servers.example.json deploy/servers.json
```

```json
[
  { "name": "prod-1", "host": "你的IP", "port": 22,
    "user": "root", "app_dir": "/opt/xiangqi" }
]
```

这个文件**不含密钥**，可以提交入库，用版本控制记录有哪几台机器。

> 为什么服务器清单放文件、而不是放 Secret：
> GitHub Actions 的 `secrets` 上下文在 `jobs.<job_id>.strategy` 中并不可靠，
> `secrets[...]` 动态索引也有争议用法。而 `needs` 输出在 `strategy` 里是
> 明确可用的，所以工作流先由一个 `prepare` job 读取 JSON 并输出，
> 再用 `fromJSON(needs.prepare.outputs.servers)` 构造 matrix。

### 8.3 Secrets 与 Variables 清单

**Settings → Secrets and variables → Actions**

Secrets（加密，不可回读）：

| 名称 | 用途 | 建议生成方式 |
|---|---|---|
| `ACR_REGISTRY` | ACR 地址 | `registry.cn-hangzhou.aliyuncs.com` |
| `ACR_NAMESPACE` | ACR 命名空间 | 你自己创建的 |
| `ACR_USERNAME` | ACR 登录名 | 阿里云控制台查看 |
| `ACR_PASSWORD` | ACR 固定密码 | 阿里云控制台设置 |
| `DEPLOY_SSH_KEY` | 部署用私钥全文 | 见 8.4 |
| `MYSQL_ROOT_PASSWORD` | MySQL root 密码 | `openssl rand -hex 32` |
| `MYSQL_PASSWORD` | 应用账号密码 | `openssl rand -hex 32` |
| `XIANGQI_JWT_SECRET` | JWT 签名密钥 | `openssl rand -hex 32` |

> **强烈建议全部用 `openssl rand -hex 32` 生成**：结果只含 `0-9a-f`，
> 写进 `.env` 时不需要任何转义，能绕开引号、`$`、换行引起的各种诡异问题。

Variables（明文，可回读）：

| 名称 | 用途 | 示例 |
|---|---|---|
| `XIANGQI_ALLOWED_ORIGINS` | 允许的 WebSocket 来源 | `https://chess.example.com` |

留空表示只允许同源，适合直接用 IP 访问的场景。

### 8.4 生成部署密钥

**在本地生成，不要用你日常登录服务器的那把密钥**——部署密钥泄露时只吊销它：

```bash
ssh-keygen -t ed25519 -C "xiangqi-deploy" -f ~/.ssh/xiangqi_deploy -N ""
```

把**公钥**装到每台服务器：

```bash
ssh-copy-id -i ~/.ssh/xiangqi_deploy.pub root@你的服务器IP
```

把**私钥全文**（含首尾两行）填入 GitHub Secret `DEPLOY_SSH_KEY`：

```bash
cat ~/.ssh/xiangqi_deploy
```

### 8.5 发布工作流解析

[.github/workflows/release.yml](../.github/workflows/release.yml) 分三段：

**① `prepare`** —— 读 `deploy/servers.json`，压成单行 JSON 作为 job 输出。
文件不存在时只警告并跳过部署，不会让整个流程失败。

**② `build`** —— 登录 ACR → Buildx → 构建并推送。
镜像 tag 规则：打 tag 触发用 `<tag>-<短sha>`，手动触发可用自定义值。
用 `cache-from/to: type=gha` 复用构建缓存，重复构建明显变快。

**③ `deploy`** —— matrix 逐台部署，`max-parallel: 1` 串行更新，
`fail-fast: false` 让某台失败不影响其余。每台执行：

1. 配好 SSH 私钥并预取主机指纹（之后指纹变化会拒绝连接）
2. 在 runner 本地按 Secrets 生成 `.env`（用 env 中转，避免密码里特殊字符被二次解释）
3. scp 编排文件与 `.env` 到服务器的 `app_dir/deploy/`
4. 远端 `docker login` → `compose pull` → `compose up -d` → `image prune`
5. 远端轮询 `/api/health`，最多等 60 秒

### 8.6 服务器首次准备

每台服务器（Ubuntu 22.04 / 24.04）执行一次：

```bash
# Docker Engine：与本地 WSL 完全相同的安装方式（见 wsl-deploy-guide.md 阶段 1）
# 装好后确认：
docker version && docker compose version

# 部署目录
sudo mkdir -p /opt/xiangqi/deploy

# 开放服务端口（8080 对外；3306 不要开）
sudo ufw allow 8080/tcp
```

**首次部署建议手工跑一次**，确认链路通了再交给 CI：

```bash
cd /opt/xiangqi/deploy
# 手工放一份 .env（可从 CI 生成的格式抄，或本地填好 scp 上来）
docker compose -f docker-compose.yml -f docker-compose.prod.yml pull
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
curl -s http://127.0.0.1:8080/api/health
```

### 8.7 发布

```bash
git tag v1.0.0
git push origin v1.0.0
```

等 Gitee 镜像同步（约 1 分钟）→ GitHub Actions 自动构建推送 → 逐台部署。

---

## 运维

### 回滚

因为每次发布的镜像都有独立 tag，回滚就是换 tag 重启：

```bash
cd /opt/xiangqi/deploy
sed -i 's|^XIANGQI_IMAGE=.*|XIANGQI_IMAGE=registry.cn-hangzhou.aliyuncs.com/命名空间/xiangqi:v0.9.0|' .env
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

> 注意：`sed` 改的是服务器本地 `.env`，下次 CI 部署会覆盖回 Secrets 里的值。
> 要永久回滚，应该在 GitHub 上重新对旧 commit 打 tag 走一遍流水线。

### 密钥轮换

| 密钥 | 轮换步骤 |
|---|---|
| MySQL / JWT | 改 GitHub Secrets → 重新触发 Release。注意 JWT 换key会让所有登录态失效 |
| 部署 SSH 密钥 | 服务器换 `authorized_keys` → 改 `DEPLOY_SSH_KEY` |
| ACR 密码 | 控制台重置 → 改 `ACR_PASSWORD` |

### 排查速查

| 现象 | 排查方向 |
|---|---|
| GitHub 上没出现新 commit | Gitee 镜像是否配置成功；是否在 5 分钟冷却期内 |
| Actions 没触发 | 分支是否是 `main`/`dev`；镜像同步是否成功 |
| 构建失败在 `npm ci` | 本地改了 `package.json` 却没提交 `package-lock.json` |
| `docker pull` 在服务器失败 | ACR 凭证、镜像 tag 是否存在 |
| 部署后健康检查超时 | 服务器上 `docker compose logs server`；多半是连不上 MySQL |
| `Access denied ... CREATE DATABASE` | 覆盖层没生效，`-no-create-db` 丢了 |

---

## Secrets 与配置文件对照总表

同一个配置项在三个地方的对应关系（这是"配置外部化"最容易搞混的地方）：

| 配置项 | `deploy/.env.example` | GitHub Secrets | 服务器 `deploy/.env` |
|---|---|---|---|
| MySQL root 密码 | `MYSQL_ROOT_PASSWORD` | `MYSQL_ROOT_PASSWORD` | 由 CI 生成 |
| 应用数据库密码 | `MYSQL_PASSWORD` | `MYSQL_PASSWORD` | 由 CI 生成 |
| JWT 密钥 | `XIANGQI_JWT_SECRET` | `XIANGQI_JWT_SECRET` | 由 CI 生成 |
| 允许来源 | `XIANGQI_ALLOWED_ORIGINS` | Variables 同名 | 由 CI 生成 |
| 镜像地址 | `XIANGQI_IMAGE` | 由 build job 产出 | 由 CI 生成 |
| ACR 凭证 | 不出现 | `ACR_*` 四个 | 不写进 .env |

规律：**Secrets 是唯一事实来源，服务器 `.env` 是它的产物，`.env.example` 只是模板。**

---

## 进度

- [x] 阶段 5 步骤就绪（待执行：git init、Gitee 建仓、首次推送）
- [x] 阶段 6 步骤就绪（待执行：GitHub PAT、Gitee 镜像配置）
- [x] 阶段 7 CI 已落地：`.github/workflows/ci.yml`
- [x] 阶段 8 CD 已落地：`.github/workflows/release.yml` + `deploy/servers.example.json`
- [x] 阶段 3：配置外部化（已并入阶段 2）
- [x] 阶段 4：IDE 实时调试方案 —— 见 [ide-debug-guide.md](ide-debug-guide.md)
