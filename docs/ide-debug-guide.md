# IDE 实时调试篇（阶段 4）

> 承接 [wsl-deploy-guide.md](wsl-deploy-guide.md)（本地环境）与
> [ci-cd-guide.md](ci-cd-guide.md)（发布链路）。
> 本篇解决一个问题：**怎么在 Windows 的 IDE 里改代码，立刻在 WSL 的 Linux 环境里跑起来并断点调试。**

---

## 0. 结论先行

| 方案 | 代码位置 | 谁执行编译/调试 | 适用 |
|---|---|---|---|
| **A（推荐）** | WSL ext4 | WSL 内的 Go + dlv | 想贴近生产、构建快，GoLand 或 VS Code 都行 |
| B | Windows NTFS | GoLand 把源码上传到 WSL 再跑 | 想保留 Windows 侧文件，但会多一层上传同步 |
| C | WSL ext4 | WSL | 前端为主、或偏好 VS Code |

**推荐 A**：把代码放在 WSL 的 ext4 里，用 GoLand 通过
`\\wsl.localhost\Ubuntu-24.04\...` 打开。GoLand 2021.3 起就支持这种用法，
2024.1 之后调试体验已经稳定。

---

## 1. 先纠正一个常见误解

**"跨文件系统慢"到底慢在哪？**

慢的不是"Windows 的 IDE 去读 WSL 的文件"，而是**"用 WSL 的工具去处理位于 Windows 盘上的文件"**：

| 组合 | 速度 | 原因 |
|---|---|---|
| WSL 里的 `go build` 处理 `/home/...`（ext4） | 快 | 本地文件系统 |
| WSL 里的 `go build` 处理 `/mnt/e/...`（NTFS） | **慢** | 每次 I/O 都跨 9p/virtiofs 边界 |
| Windows 的 GoLand 索引 `\\wsl.localhost\...` | 可接受 | 只读文件建索引，JetBrains 已优化 |
| Webpack/Vite 处理 `/mnt/e/...` 的 node_modules | **极慢** | 几万个小文件逐个跨边界 |

所以正确的组合是：**文件放 WSL，执行也在 WSL**。
IDE 只是"隔着边界读文件做索引"，这一步的开销远小于"每个文件 I/O 都跨界"。

有团队实测后把项目整体从 Windows 迁到 WSL，结论是构建与测试
"体感快了近一倍"，并在 GoLand 2024.1.3 上验证了
GOPATH 识别、SDK 安装、测试执行、**断点调试与步进**全部正常
（[Future Architect 实测记录](https://future-architect.github.io/articles/20240712a/)）。

> 历史上确实有过 `\\wsl$` 路径下断点不工作的问题
> （JetBrains YouTrack GO-12517），已在 2024.1 修复。

---

## 2. 方案 A：GoLand + 代码在 WSL（推荐）

### 2.1 前置条件

阶段 0、1 已完成：WSL 里有 Ubuntu，装了 Docker Engine，代码已放到
`~/projects/chinese-chess`（WSL 内的 ext4 路径，**不是** `/mnt/e/...`）。

**WSL 里需要 Go 工具链和 dlv 调试器**（两者默认都没有，必须先装）：

```bash
# 1) 装 Go —— 用阿里云镜像（官方 go.dev 在境内不通，实测阿里云可用）
wget -q --show-progress \
  https://mirrors.aliyun.com/golang/go1.26.5.linux-amd64.tar.gz -O /tmp/go.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf /tmp/go.tar.gz

# 2) 把 Go 加进 PATH（写进 ~/.bashrc 永久生效）
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
source ~/.bashrc

# 3) 走国内模块代理（proxy.golang.org 在境内不可达）
go env -w GOPROXY=https://goproxy.cn,direct
go env -w GOSUMDB=off

go version    # 期望 go1.26.5 linux/amd64

# 4) 装 delve —— GoLand 的调试后端，必须装在 WSL 侧
go install github.com/go-delve/delve/cmd/dlv@latest
which dlv     # 期望 /home/<你的用户名>/go/bin/dlv
```

> **版本怎么选**：阿里云镜像提供 1.24 ~ 1.27 全系列，可用
> `curl -s https://mirrors.aliyun.com/golang/ | grep -o 'go1[0-9.]*\.linux-amd64\.tar\.gz' | sort -uV | tail`
> 查看。项目 `server/go.mod` 声明 `go 1.24`，装 1.24 以上都可以；
> 建议与 Windows 侧保持一致，减少"本地能编译、WSL 不能"的差异。
>
> `golang.google.cn` 也可用（返回 302 跳转）；清华源已 404、中科大 403，别用。

#### 为什么 WSL 要装 Go，而生产服务器不用装？

这是两件互不相干的事，容易混淆：

| | 装在哪 | 存在时机 | 生产服务器需要吗 |
|---|---|---|---|
| **构建用 Go** | `golang:1.24-alpine` **镜像内部** | 仅 `docker build` 期间 | ❌ 不需要 |
| **调试用 Go** | WSL 的 `/usr/local/go` | 开发者本地，长期存在 | ❌ 不需要 |

`deploy/Dockerfile` 是**三段构建**，最终镜像 `FROM alpine:3.20`，
只从前两段把编译产物 `COPY` 过来。实测最终镜像内部：

```
无 go 工具链 / 无 node / 无 npm / 无 gcc
/app/xiangqi        7.1 MB           Go 编译出的静态二进制
/app/web/dist/      assets + index.html   前端构建产物
系统                Alpine Linux
运行用户            uid=10001(app)，非 root
镜像体积            30.5 MB
```

所以**服务器上只需要 Docker Engine + Compose 插件**，其余一概不装。
这正是容器化的核心价值：把构建的复杂度留在开发侧，生产侧保持极简。
`docker system df` 里那 785 MB 的 Build Cache 是构建阶段的中间层，
只存在于你本地，不会进入镜像，也不会出现在服务器上。

### 2.2 用 UNC 路径打开项目

GoLand → **File | Open**，路径填：

```
\\wsl.localhost\Ubuntu-24.04\home\<你的用户名>\projects\chinese-chess
```

> 老版本 WSL 用 `\\wsl$\Ubuntu-24.04\...`，新版本统一为
> `\\wsl.localhost\`。发行版名字以 `wsl -l -v` 的输出为准。

打开后 GoLand 会把这个项目识别为 WSL 项目。此时：

- **运行代码时默认就在 WSL 里执行**（GoLand 把 WSL 视作 local machine）
- **Git 会自动切换到 WSL 里的 git**（Windows 上没装 git 也没关系）

### 2.3 配置 WSL 的 Go 工具链

**Settings → Go → GOROOT → Add SDK → Local**，在目录选择框里可以选到
`\\wsl.localhost\Ubuntu-24.04\` 下的路径：

- 若用 tarball 安装：选 `\\wsl.localhost\Ubuntu-24.04\usr\local\go`
- 也可点 **Download** 让 IDE 自己装到 `\\wsl.localhost\Ubuntu-24.04\home\<你>\sdk`

选好后 GoLand 会读出 `go version`，确认版本正确。

GOPATH 一并设为 WSL 侧路径，例如
`\\wsl.localhost\Ubuntu-24.04\home\<你>\go`。

### 2.4 让终端也在 WSL 里

否则你在 IDE 里敲 `go`、`npm` 可能用到 Windows 的版本：

**Settings → Tools → Terminal → Shell path** 填 `wsl.exe`，
或把 Application Settings 里的 Shell 指向：

```
wsl.exe -d Ubuntu-24.04 --cd "~"
```

验证：打开 IDE 内置终端，`uname -a` 应输出 Linux，`which go` 应是 `/usr/local/go/bin/go`。

### 2.5 防火墙（**调试连不上多半是这里**）

GoLand 的调试器（delve）跑在 WSL 里，Windows 侧的 IDE 需要反向连回它。
Windows 防火墙默认会拦掉这条入站连接，表现为**断点不生效、或调试会话直接失败**。

**管理员 PowerShell**执行（只需一次）：

```powershell
# 1) 确认 WSL 的虚拟网卡名字，通常是 vEthernet (WSL) 或 vEthernet (WSL2)
Get-NetAdapter

# 2) 放行该网卡的入站连接（把 InterfaceAlias 换成上一步看到的名字）
New-NetFirewallRule -DisplayName "WSL" -Direction Inbound `
  -InterfaceAlias "vEthernet (WSL)" -Action Allow

# 3) 清掉 GoLand 之前被自动创建的拦截规则
Get-NetFirewallProfile -Name Public | Get-NetFirewallRule |
  Where-Object DisplayName -ILike "GoLand*" | Disable-NetFirewallRule
```

之后第一次启动调试时如果弹出 Windows 防火墙对话框，**勾选 Public networks** 再允许。

### 2.6 后端断点调试

1. Run | Edit Configurations → **+** → Go Build
2. 配置：
   - **Run kind**: `Package`
   - **Package path**: `xiangqi/cmd/server`（模块名取自 `server/go.mod`）
   - **Working directory**: `\\wsl.localhost\Ubuntu-24.04\home\<你>\projects\chinese-chess\server`
   - **Environment**：按需填 `XIANGQI_DB_HOST=127.0.0.1` 等，或直接用默认值
3. 点 Debug（虫子图标），在 `internal/game/moves.go` 之类的地方下断点验证能停下

> 数据库先用 compose 起起来（只起 MySQL 即可）：
> `cd ~/projects/chinese-chess/deploy && docker compose up -d mysql`
>
> 后端在 WSL 里跑、MySQL 在 WSL 的容器里跑，`127.0.0.1:3306` 直通，无需额外配置。

### 2.7 前端实时调试

前端**不走** GoLand 的调试器，用它自己的热更新：

```bash
# WSL 终端里
cd ~/projects/chinese-chess/web
npm install          # 首次
npm run dev
```

Vite 监听 5173，WSL2 会自动把端口转发到 Windows。
Windows 浏览器直接打开 <http://127.0.0.1:5173> 即可，改代码即时刷新。

`vite.config.js` 里已把 `/api` 与 `/ws` 代理到 `127.0.0.1:8080`，
所以后端要么用 GoLand 跑着，要么用 compose 起容器。

> 想调试浏览器端 JS，在浏览器里按 F12 就够了——Vite 提供 sourcemap，
> 断点直接打在原始 `.vue` 文件上。

---

## 3. 方案 B：代码留 Windows，用 Run Target 推到 WSL

适合"不想动现有 `E:\WORK\project\chinese-chess`"的场景。

**Run | Manage Targets → Add Target → WSL**，然后：

- 选择 Linux 发行版（能自动探测就直接 Next）
- **Go Executable**：WSL 里的 go，如 `/usr/local/go/bin/go`
- **GOPATH**：如 `/home/<你>/go`
- **Additional settings**：
  - *Project sources directory*：源码上传到目标机的目录
  - *Compiled executables directory*：编译产物目录

> **这里就是你在问的"同步"**：GoLand 会把源码文件、工作目录文件、编译产物
> 和 delve 一起上传到 WSL。不填这两个目录的话，IDE 每次会在随机目录里建。

**代价**：每次运行都要上传一遍源码。项目小的时候无感，
但 `web/node_modules` 那种体量不适合走这条路——所以前端建议仍在 WSL 里直接跑。

---

## 4. 方案 C：VS Code + Remote-WSL

偏好 VS Code 的话，这是微软官方方案，前后端体验最统一：

1. Windows 装 VS Code + 扩展 **WSL**（Remote Development 套件）
2. **在 WSL 终端里**进入项目目录执行 `code .`
   —— 必须从 WSL 里启动，这样 VS Code 才会以 WSL 为后端
3. 装扩展：**Go**（`golang.go`）

`.vscode/launch.json`（本地文件即可，`.vscode/` 已被 gitignore）：

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "调试 server",
      "type": "go",
      "request": "launch",
      "mode": "auto",
      "program": "${workspaceFolder}/server/cmd/server",
      "env": { "XIANGQI_DB_HOST": "127.0.0.1" }
    }
  ]
}
```

VS Code 的调试器同样跑在 WSL 侧，**不需要额外的防火墙配置**——
这是它比 GoLand 省事的一点。

---

## 5. 三种方案的取舍速查

| 维度 | A：GoLand + WSL 代码 | B：GoLand + Windows 代码 | C：VS Code + WSL |
|---|---|---|---|
| 构建速度 | 快 | Go 命令走 WSL 处理 NTFS，慢 | 快 |
| 断点调试 | 需配防火墙 | 需配防火墙 + 上传 | 开箱可用 |
| 前端热更新 | WSL 里跑，快 | 同上（推荐 WSL 里跑） | 快 |
| 首次配置成本 | 中 | 中 | 低 |
| 贴近生产 | 高 | 中 | 高 |
| 适合 | 你现在的 GoLand 习惯 | 不想迁文件 | 想要最省事 |

---

## 6. 常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| 断点不停，或提示无法连接 | Windows 防火墙拦了 delve 入站 | 见 2.5，`New-NetFirewallRule` 放行 |
| IDE 终端里 `go` 是 Windows 版 | Terminal Shell path 未指向 WSL | 见 2.4 |
| `npm install` 奇慢 | 代码在 `/mnt/e` | 迁到 `~/projects` |
| GoLand 索引一直转 | 首次索引 WSL 文件系统较慢 | 等它跑完；后续增量索引很快 |
| 改了代码但容器里没变 | 容器用的是镜像里的旧代码 | 开发用 `go run`；容器只在发布时重建 |
| 浏览器打不开 5173 | Vite 只监听容器/子网 | 确认在 WSL 里跑，WSL2 会自动转发 localhost |

---

## 进度

- [x] 阶段 4 三套方案与配置步骤就绪
- [ ] 待你选定一套并实测（决定因素是：继续用 GoLand 还是转 VS Code）
- [ ] 阶段 5～8 待执行（见 [ci-cd-guide.md](ci-cd-guide.md)）
