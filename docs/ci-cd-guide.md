# 部署与发布篇：从本地代码到多台服务器

> 承接 [wsl-deploy-guide.md](wsl-deploy-guide.md)（阶段 0～2：本地环境）。
> 本篇覆盖**阶段 5～8**：Git 托管、镜像构建推送、多服务器部署、回滚与密钥轮换。

---

## 0. 整体架构

```
本地 WSL                        Gitee             阿里云 ACR            服务器
────────                        ─────             ──────────            ──────
改代码 ──► git push ──────────► 仓库

bash scripts/deploy.sh v1.0.0
   ├─ docker build ───────────────────────────► 推送镜像
   └─ ssh 逐台部署 ─────────────────────────────────────────────────► compose pull
                                                                      compose up -d
                                                                      健康检查
```

一条命令完成：**构建 → 推送私有仓库 → 逐台滚动更新 → 健康检查**。

### 三条设计原则

1. **镜像只构建一次**，所有服务器拉同一个 tag —— 多机版本绝对一致，不存在"这台编译出来的和那台不一样"。
2. **运行时镜像全部来自自己的 ACR** —— 不依赖 Docker Hub，也不依赖任何公共加速器的缓存策略。
3. **凭据分层** —— 仓库里只有模板；真实值在本地 `deploy/.env.deploy` 和服务器 `deploy/.env`，两者都被 gitignore。

---

## 阶段 5：Git 仓库与 Gitee 托管

### 5.1 准备 SSH 密钥

Git 操作在 **WSL 里**执行（代码在 WSL 的 ext4 上，跨文件系统操作 NTFS 上的仓库又慢又容易出权限问题）。如果 Windows 侧已配过 Gitee，直接复用：

```bash
mkdir -p ~/.ssh && chmod 700 ~/.ssh
cp /mnt/c/Users/<你的用户名>/.ssh/id_ed25519     ~/.ssh/
cp /mnt/c/Users/<你的用户名>/.ssh/id_ed25519.pub ~/.ssh/

# 关键：NTFS 挂载出来的权限是 777，SSH 会拒绝，必须收紧
chmod 600 ~/.ssh/id_ed25519
chmod 644 ~/.ssh/id_ed25519.pub

# 验证
ssh -o StrictHostKeyChecking=accept-new -T git@gitee.com
```

### 5.2 初始化与提交

```bash
cd ~/projects/chinese-chess
git config --global user.name  "你的名字"
git config --global user.email "你的邮箱"

git init -b main
git add .
git commit -m "初始提交"
```

### 5.3 ⚠️ 提交前的安全检查（最关键的一步）

`git status --short` 对**未跟踪的目录只显示目录名**，`?? deploy/` 并不会展开里面的文件。所以必须专门验证：

```bash
# ① 精确检查 .env 是否被忽略（有输出=已忽略）
git check-ignore -v deploy/.env

# ② 展开全部未跟踪文件，扫一遍敏感关键词
git status --short --untracked-files=all | grep -iE "\.env|secret|password|credential|\.key$|\.pem$"
```

期望结果：`deploy/.env.example`（模板）出现，`deploy/.env`（真实凭据）**绝不出现**。

> **一旦密码进了 git 历史，即使随后删除，也能被 clone 的人翻出来。** 这一步比后面所有步骤都重要。
>
> 顺带一提，`server/internal/auth/password.go` 会命中 "password" 关键词，但它是**源码**（PBKDF2 口令哈希实现），不是密码文件。

### 5.4 Gitee 建仓与推送

在 Gitee 新建仓库（**建议私有**），**不要勾选任何"初始化仓库"选项**——否则远端有初始提交，本地推送会因历史不一致被拒。

```bash
git remote add origin git@gitee.com:<你的账号>/chinese-chess.git
git push -u origin main

# 确认远程已收到
git ls-remote --heads origin
```

### 5.5 行尾策略（.gitattributes）

仓库里已有 `.gitattributes`，核心两行：

```
* text=auto eol=lf
*.ps1 text eol=crlf
```

**为什么必须要有**：Windows 版 Git 默认 `core.autocrlf=true`，检出时会把 LF 转成 CRLF。若用 Windows 侧工具（如 SourceTree）操作 WSL 里的仓库，shell 脚本会变成 CRLF，在 Linux 里执行时报：

```
/bin/bash^M: bad interpreter: No such file or directory
```

这个文件让仓库自身声明行尾规则，不依赖各人本机的 git 配置。

---

## 阶段 6：构建并推送镜像

### 6.1 准备 ACR

阿里云「容器镜像服务 ACR」→ 创建**个人版**实例（免费）→ 建命名空间（如 `example-namespace`）→ 建镜像仓库。

记下四项：

| 项 | 在哪找 | 新版个人版的样子 |
|---|---|---|
| registry 地址 | 实例概览页 | `crpi-xxxxxxxx.cn-shenzhen.personal.cr.aliyuncs.com` |
| 命名空间 | 左侧「命名空间」 | 自己起的名字 |
| 登录名 | 「镜像工具」→「访问凭证」 | 阿里云账号全名 |
| 固定密码 | 同一页面设置 | 自己设的 |

> 注意：**新版个人版 ACR 的地址不是** `registry.cn-hangzhou.aliyuncs.com` 那种老格式，而是 `crpi-*.personal.cr.aliyuncs.com`。两者别混。

### 6.2 本地部署配置

```bash
cd ~/projects/chinese-chess/deploy
cp .env.deploy.example .env.deploy
nano .env.deploy        # 填 ACR 四项凭据
chmod 600 .env.deploy
```

若服务器拉不动 Docker Hub（见 6.5），再加一行指向私有仓库的 MySQL 镜像：

```ini
MYSQL_IMAGE=crpi-xxxxxxxx.cn-shenzhen.personal.cr.aliyuncs.com/example-namespace/mysql:8.4
```

### 6.3 只构建推送（先验证这一步）

```bash
cd ~/projects/chinese-chess
bash scripts/deploy.sh v0.1.0 --no-deploy
```

**先只验证"构建 + 推送"**，不碰服务器。这一环最容易出问题（ACR 权限、命名空间、区域地址、manifest 兼容性），单独验证能快速定位。

成功后去 ACR 控制台的「镜像仓库」应能看到对应 tag。

### 6.4 为什么必须关掉 provenance

`scripts/deploy.sh` 里的构建命令带两个参数，**不是可选优化**：

```bash
docker build --provenance=false --sbom=false -f deploy/Dockerfile -t "$IMAGE_TAG" .
```

**原因**：Docker 29 默认启用 containerd 镜像存储，BuildKit 会附带 provenance（构建来源证明）和 SBOM 清单，产出的 manifest 含 `application/vnd.oci.empty.v1+json` 这个 OCI 媒体类型。**阿里云 ACR 不认识它**，推送时层全部成功、最后 manifest 被拒：

```
error from registry: unknown manifest class for application/vnd.oci.empty.v1+json
```

关掉这两个附件后推送正常（已实测）。

### 6.5 把 MySQL 镜像也放进私有仓库

服务器的 `docker-compose.prod.yml` 需要 `mysql:8.4`。若服务器拉不动 Docker Hub，就把镜像先推到自己的 ACR：

```bash
ACR=crpi-xxxxxxxx.cn-shenzhen.personal.cr.aliyuncs.com/example-namespace

docker tag mysql:8.4 $ACR/mysql:8.4
docker push $ACR/mysql:8.4          # 1.12 GB，较慢，一次性
```

然后在 `.env.deploy` 里设 `MYSQL_IMAGE`（见 6.2），`deploy.sh` 会自动把它注入服务器端 `.env`。

**这是一次性投资**：之后所有服务器都从私有仓库高速拉取。

---

## 阶段 7：多服务器部署

### 7.1 服务器清单

```bash
cd ~/projects/chinese-chess/deploy
cp servers.example.json servers.json
nano servers.json
```

```json
[
  { "name": "prod-1", "host": "1.2.3.4", "port": 22,
    "user": "root", "app_dir": "/opt/xiangqi" }
]
```

**不含密钥，可以入库**。加第二台服务器就是往数组里再加一项，无需改脚本。

> 注意这里面有公网 IP。仓库保持私有即可；若日后转公开，先把它移出或加进 `.gitignore`。

### 7.2 服务器首次准备

**① 生成部署专用密钥**（不要复用 Gitee 那把，职责分离）：

```bash
ssh-keygen -t ed25519 -C "xiangqi-deploy" -f ~/.ssh/xiangqi_deploy -N ""
ssh-copy-id -i ~/.ssh/xiangqi_deploy.pub root@你的服务器

# 验证免密
ssh -i ~/.ssh/xiangqi_deploy root@你的服务器 'echo 登录成功'
```

**② 装 Docker Engine**（阿里云源，与本地一致）：

```bash
sudo apt install -y ca-certificates curl gnupg
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

**③ 配镜像加速器**（**服务器上才用阿里云加速器**，见附 A）：

```bash
cat > /etc/docker/daemon.json <<'EOF'
{
  "registry-mirrors": ["https://<你的ID>.mirror.aliyuncs.com"],
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" }
}
EOF

systemctl daemon-reload && systemctl restart docker
docker info | grep -A2 "Registry Mirrors"
```

**④ 开放端口**：服务器防火墙**和云厂商安全组**都要放行 8080。

```bash
sudo ufw allow 8080/tcp        # 若用 ufw
# 云控制台安全组入方向加一条：TCP 8080，授权对象 0.0.0.0/0
```

**3306 不要对外开放**。`docker-compose.prod.yml` 用 `!override` 把数据库端口
**写死绑在 `127.0.0.1`**，只在宿主机回环上；安全组层面也别开。
要从外面连库，走 SSH 隧道，见 8.4。

### 7.3 完整部署

```bash
cd ~/projects/chinese-chess
bash scripts/deploy.sh v1.0.0
```

### 7.4 脚本做了什么

| 步骤 | 内容 |
|---|---|
| 1/5 | 检查环境，**校验 docker 是 WSL 原生版**（指向 `/mnt/c/...` 会直接拦下） |
| 2/5 | 登录 ACR |
| 3/5 | 构建镜像（带 `--provenance=false --sbom=false`） |
| 4/5 | 推送镜像 |
| 5/5 | 逐台：建目录 → scp 编排文件 → 生成服务器 `.env` → 远端 `docker login` + `compose pull/up` → 健康检查（最多 60 秒） |

**几个刻意的设计**：

- **服务器端 `.env` 自动生成**：从 `deploy/.env.prod`（没有则回退 `.env`）派生，但**强制覆盖** `XIANGQI_IMAGE`（指向本次推送的镜像）和 `XIANGQI_ENV=production`，避免忘了改镜像地址而部署了旧版本。
- **逐台串行 + 失败不中断**：某台挂了不影响其余，最后汇总"成功 N 台，失败 M 台"，有失败则退出码非零。
- **`--server <name>`** 只部署指定一台；**`--no-deploy`** 只构建推送。

---

## 阶段 8：运维

### 8.1 回滚

每个版本都有独立 tag，回滚就是换 tag 重启：

```bash
cd /opt/xiangqi/deploy
sed -i 's|^XIANGQI_IMAGE=.*|XIANGQI_IMAGE=<ACR地址>/<命名空间>/xiangqi:v0.9.0|' .env
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

> 注意：这是改**服务器本地** `.env`，下次 CI/脚本部署会覆盖回新版本。要永久回滚，应该对旧 commit 重新打 tag 走一遍部署流程。

### 8.2 密钥轮换

| 密钥 | 轮换步骤 |
|---|---|
| MySQL / JWT | 改本地 `.env`（或 `.env.prod`）→ 重新部署。**换 JWT 会让所有登录态失效** |
| 部署 SSH 密钥 | 服务器换 `authorized_keys` → 本地换 `~/.ssh/xiangqi_deploy` |
| ACR 密码 | 控制台重置 → 改 `deploy/.env.deploy` |

### 8.3 排查速查

| 现象 | 排查方向 |
|---|---|
| `deploy.sh` 报找不到 SSH 密钥，路径形如 `/.ssh/xxx` | `.env.deploy` 里的 `~` 丢了，改成绝对路径 |
| push 报 `unknown manifest class ... oci.empty.v1+json` | 构建缺 `--provenance=false --sbom=false` |
| 服务器 `mysql:8.4` 拉取超时 | 服务器没配加速器，或加速器缺该镜像 → 用 `MYSQL_IMAGE` 指向私有仓库 |
| 健康检查超时但容器已起来 | 云厂商**安全组**没放行 8080 |
| 容器起不来，日志显示 ping 失败 | MySQL 未就绪；检查 `depends_on: service_healthy` 是否还在 |
| 想确认线上到底跑的是哪个版本 | 见附 A 末尾的"如何验证部署真的生效" |

---

### 8.4 用 Navicat 连生产数据库（走 SSH 隧道）

生产库只绑在服务器回环上（见 `docker-compose.prod.yml`），所以：
**不要去安全组放行 3306**，正确姿势是把 sshd 当跳板。

Navicat 里建连接，两个页面填完即可：

| 页面 | 字段 | 值 |
|---|---|---|
| 常规 | 主机 / 端口 | `127.0.0.1` / `3306`（**从服务器视角**解析，出口就是回环） |
| 常规 | 用户名 / 密码 | `root` / `deploy/.env` 里的 `MYSQL_ROOT_PASSWORD` |
| SSH | 主机 / 端口 | `<服务器 IP>` / `22` |
| SSH | 用户名 | `root` |
| SSH | 验证方法 | 公钥 → 选 `xiangqi_deploy` 私钥 |

> Navicat 是 Windows 程序，而部署私钥在 WSL 里（`~/.ssh/xiangqi_deploy`），
> 需要先放一份到 Windows 侧：
>
> ```powershell
> Copy-Item \\wsl.localhost\Ubuntu-24.04\home\administrator\.ssh\xiangqi_deploy `
>   "$env:USERPROFILE\.ssh\xiangqi_deploy"
> ```
>
> 老版本 Navicat 只认 `.pem` / `.ppk`，那就用 PuTTYgen 把 OpenSSH 私钥
> 转成 `.ppk` 再选。

**命令行等价物**（不想在 Navicat 里配密钥时用这个）：

```bash
# WSL 里执行，保持前台运行
ssh -N -L 13306:127.0.0.1:3306 -i ~/.ssh/xiangqi_deploy root@<服务器 IP>
```

然后 Navicat 直连 `127.0.0.1:13306`（不勾 SSH 隧道）。

**root 账号的说明**：MySQL 官方镜像建的是 `root@'%'`，而不只是
`root@localhost`，所以从 docker-proxy 转发进来（源 IP 是网桥网关）的连接
能通过。核对一下：

```bash
docker exec xiangqi-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" \
  -e "SELECT user, host FROM mysql.user"
```

万一没有 `root@'%'`，补一个（在容器里执行）：

```sql
CREATE USER IF NOT EXISTS 'root'@'%' IDENTIFIED BY '<MYSQL_ROOT_PASSWORD>';
GRANT ALL PRIVILEGES ON *.* TO 'root'@'%' WITH GRANT OPTION;
FLUSH PRIVILEGES;
```

> **安全提醒**：`root@'%'` + SSH 隧道等价于「拿到这台服务器的 SSH 就等于
> 拿到整个数据库」。日常查数据建议用业务账号 `xiangqi`（只有 `xiangqi.*`），
> 只有改表结构、看 `performance_schema` 时才用 root。
> 无论如何，3306 都不要出现在安全组的入方向规则里。

> 改了 `docker-compose.prod.yml` 的端口后要重新部署（或服务器上手动
> `docker compose ... up -d`）才会重建 mysql 容器；数据在 `mysql-data`
> 卷里，重建不丢。

---

## 附 A：踩坑记录（均为实测）

| 问题 | 根因 | 解法 |
|---|---|---|
| 本地拉 `mysql:8.4` 超时 | 未配镜像加速器 | 配 `docker.1ms.run`（本地可用的公共源） |
| 阿里云加速器返回 **403** | 阿里云限制**非阿里云主机**调用 | 本地换公共源；**服务器（ECS）上不受此限** |
| 服务器拉 `alpine`/`mysql` 返回 **404** | 加速器端点活着，但恰好不代理这两个镜像 | 把镜像推进自己的 ACR，用 `MYSQL_IMAGE` 指向它 |
| push 报 `unknown manifest class` | Docker 29 + containerd 存储，BuildKit 默认附加 provenance/SBOM | `--provenance=false --sbom=false` |
| `~/.ssh/xxx` 变成 `/.ssh/xxx` | `.env` 里的 `~` 丢失或带引号未展开 | 用绝对路径；脚本也已补上展开逻辑 |
| `docker` 命令指向 `\\wsl.localhost\...` 之外的 Windows 版 | WSL 的 `appendWindowsPath` 把 Windows PATH 追加进来 | 配 `/etc/wsl.conf` 的 `[interop] appendWindowsPath = false` |
| WSL 里 `go` 只在交互式终端可用 | PATH 写在 `~/.bashrc`，非交互 shell 会提前 return | 软链到 `/usr/local/bin`，或写 `/etc/profile.d/` |
| 两份代码副本漂移 | Windows 与 WSL 各一份 | 以 WSL 那份为准，用 git 统一；改完及时同步 |

### 如何验证部署真的生效

不要只看"页面能打开"。用**可区分的标记**验证端到端：

1. 改一行**会体现在接口响应里**的代码（例如 `handlers.go` 里 health 的 `dbStatus` 初值）
2. `bash scripts/deploy.sh v0.1.1`
3. `curl http://<服务器IP>:8080/api/health`，确认返回了新标记

进一步确认"镜像里真的是新代码"，可以把二进制从镜像里取出来比对：

```bash
docker create --name tmpimg <镜像地址>
docker cp tmpimg:/app/xiangqi /tmp/xiangqi-bin
docker rm tmpimg

# 与旧版本比 md5，或直接搜特征字节
md5sum /tmp/xiangqi-bin
grep -c -a "你的标记" /tmp/xiangqi-bin
```

这比任何静态检查都能说明问题。

---

## 附 B：接入 Gitee Go（可选）

目标是让 `git push` 自动触发构建部署，省掉手工执行 `deploy.sh`。

### Gitee Go 的能力边界（核对官方文档得到）

| 插件 | 官方支持 | 本项目能用吗 |
|---|---|---|
| `build@golang` | Go **≤ 1.16** | ❌ 项目要求 1.24 |
| `build@nodejs` | Node **≤ 15** | ❌ Vite 5 需要 18+ |
| `ut@maven` / `ut@gradle` | 仅 Java | ❌ 语言不匹配 |
| **`build@docker`** | 构建镜像并推送远程仓库 | ✅ **可用** |
| `shell@agent` | 在主机组执行脚本 | ✅ 可用（部署） |
| `deploy@agent` | 下载制品到主机组并执行脚本 | ✅ 可用 |

**结论**：Gitee Go 的官方编译插件版本偏旧（文档停留在 2021-2022 年，明显偏 Java 生态），**不能用来编译本项目**。但它能用 `build@docker` 构建镜像、用 `shell@agent` 执行部署脚本——而本项目的 `deploy/Dockerfile` 已经是三段构建，Go/Node 版本都定死在镜像里，不依赖宿主环境，正好适配。

### 配置要点

- 配置文件位置：仓库根目录的 **`.workflow/`** 文件夹
- **开通前提**：Gitee 账号需**绑定手机号**
- **免费额度**：单仓库 200 分钟永久 + 每月 500 分钟
- 额度省着用的话，让**构建部署只在打 tag 时触发**，日常 push 不跑

### 建议的推进顺序

**先用 `deploy.sh` 把链路跑通并验证过，再接 Gitee Go**。理由是自动化平台会掩盖问题：一旦失败，你分不清是脚本错了还是平台配置错了。而 `deploy.sh` 是"可被任何平台调用"的一层——Gitee Go、GitHub Actions 都只是换个触发者。

---

## 附 C：配置对照总表

| 配置项 | 模板文件（入库） | 本地真实值 | 服务器真实值 |
|---|---|---|---|
| MySQL root 密码 | `deploy/.env.example` | `deploy/.env` | 由部署脚本生成 |
| 应用数据库密码 | 同上 | 同上 | 由部署脚本生成 |
| JWT 密钥 | 同上 | 同上 | 由部署脚本生成 |
| 镜像地址 | 同上（`XIANGQI_IMAGE`） | 本地用构建出的镜像 | 部署脚本强制覆盖为本次 tag |
| ACR 凭据 | `deploy/.env.deploy.example` | `deploy/.env.deploy` | 不落盘（临时 `docker login`） |
| 服务器清单 | `deploy/servers.example.json` | `deploy/servers.json` | 不需要 |
| 部署 SSH 密钥 | 无 | `~/.ssh/xiangqi_deploy` | 公钥在 `authorized_keys` |

**规律**：模板入库、真实值本地留一份、服务器上的由脚本生成。改密码只改本地一处，重新部署即生效。

---

## 进度

- [x] 阶段 5：Git 仓库 + Gitee 托管
- [x] 阶段 6：镜像构建与推送（`scripts/deploy.sh`）
- [x] 阶段 7：多服务器部署（已在一台阿里云 ECS 上验证）
- [ ] 阶段 8：回滚与密钥轮换（文档就绪，未实际演练）
- [ ] 可选：接入 Gitee Go 实现 push 自动部署
