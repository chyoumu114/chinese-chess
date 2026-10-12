# 中国象棋在线对战平台

一个完整可运行的中国象棋（象棋 / xiangqi）在线对战网站，后端 Go，前端 Vue 3，
数据存储 MySQL，实时对战走 WebSocket。

## 功能

**规则引擎（纯 Go 标准库实现，无第三方依赖）**

- 完整的象棋走子规则：车、马（蹩马腿）、炮（隔子吃）、相/象（塞象眼、不过河）、
  仕/士（九宫内斜走）、帅/将（九宫内直走 + 白脸将）、兵/卒（过河后可横走）
- 合法性判定：不能走出被将军的局面、不能走出将帅照面、被牵制的子不能离线
- 胜负判定：将死、困毙（象棋中困毙判负）、吃将、认输、超时、和棋
- 标准中文记谱：`炮二平五`、`马八进七`、`兵五进一`、`前车平八`
- 标准 FEN 读写（红方在下、红先行）
- 着法列表与悔棋（记录每一步的子力、被吃子、计数器，可精确回退）

**服务端**

- 账号体系：注册 / 登录，密码用 PBKDF2-HMAC-SHA256（21 万次迭代）加盐存储，
  会话令牌是自行签名的 HS256 JWT
- 对局大厅：快速匹配、加入指定房间、等待列表、在线名单
- 实时对战：WebSocket 服务端推送，服务端是唯一权威，每次推送完整局面 + 合法落点
- 对局时钟：**步时与局时默认都无限**（超时判负机制仍在，运营方可用环境变量开启限时）
- 人机对战：内置 Alpha-Beta 搜索 AI（简单 / 普通 / 困难三档）
- 提和 / 悔棋 / 认输 / 聊天
- 棋谱持久化：每步落子、局面、记谱、耗时入库，支持回放
- Elo 式积分与战绩统计、排行榜

**前端（Vue 3 + Vite）**

- SVG 棋盘（含楚河汉界、九宫斜线、炮兵位十字标）
- 棋盘朝向跟随自己执的一方：执黑时棋盘上下调转，黑方显示在下方（协议坐标不变，
  只在渲染与点击两处做映射）
- 点击选子、高亮合法落点、落子、着法列表、聊天、结果横幅
- 大厅、对局室、棋谱回放、排行榜、个人资料
- WebSocket 断线自动重连

## 目录结构

```
chinese-chess/
├── server/                       Go 后端
│   ├── cmd/server/               程序入口与数据库适配
│   ├── internal/game/            象棋规则引擎（棋盘、着法生成、合法性、记谱）
│   ├── internal/gamehall/        在线对局状态机（房间、座位、时钟、结果）
│   ├── internal/ai/              Alpha-Beta 搜索 AI
│   ├── internal/ws/              WebSocket 协议与 Hub
│   ├── internal/api/             REST 接口与中间件
│   ├── internal/auth/            口令哈希与令牌签发
│   ├── internal/store/           MySQL 连接、建表、查询
│   └── internal/config/          环境变量配置
├── web/                          Vue 3 前端
├── deploy/                       docker-compose 与 Dockerfile
├── scripts/build.ps1             构建 / 测试 / 运行脚本
└── docs/frontend-contract.md     前后端接口契约
```

## 快速开始

### 1. 用 Docker Compose 起 MySQL（或整套服务）

```bash
cd deploy
cp .env.example .env          # 首次使用，填写真实值

docker compose up -d mysql    # 只起数据库，后端在 IDE / go run 里跑（开发调试）
docker compose up -d --build  # 连后端一起起，一条命令跑通整站
```

凭据全部来自 `deploy/.env`（已被 `.gitignore` 忽略，切勿提交），
变量缺失时 Compose 会立即报错，不会用错配置静默启动。
生产环境叠加覆盖层，从 ACR 拉镜像且不暴露数据库端口：

```bash
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

或使用本机已有的 MySQL。服务端默认会**自动建库建表**，无需手工导入 SQL；
compose 编排里显式传了 `-no-create-db`，因为容器使用的普通 MySQL 账号没有
建库权限——建库交给 MySQL 容器的 `MYSQL_DATABASE` 完成，应用只负责建表。

### 2. 配置（可选）

全部配置都有默认值，不设置也能直接跑。默认值：`root:root@127.0.0.1:3306/xiangqi`。

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `XIANGQI_ADDR` | `:8080` | 监听地址 |
| `XIANGQI_PUBLIC_DIR` | `web/dist` | 前端构建产物目录 |
| `XIANGQI_ENV` | `development` | `production` 时强制要求设置 JWT 密钥 |
| `XIANGQI_DB_HOST` | `127.0.0.1` | MySQL 主机 |
| `XIANGQI_DB_PORT` | `3306` | MySQL 端口 |
| `XIANGQI_DB_USER` | `root` | 用户名 |
| `XIANGQI_DB_PASSWORD` | `root` | 密码 |
| `XIANGQI_DB_NAME` | `xiangqi` | 数据库名 |
| `XIANGQI_JWT_SECRET` | 开发用占位值 | **生产环境必须修改**（至少 16 字节） |
| `XIANGQI_TOKEN_TTL` | `168h` | 登录有效期 |
| `XIANGQI_TURN_TIMEOUT` | `0` | 每步限时，`0`（默认）表示**不限时** |
| `XIANGQI_GAME_TIMEOUT` | `0` | 每方总时限，`0`（默认）表示**不限时** |
| `XIANGQI_ALLOWED_ORIGINS` | 空（允许同源） | 允许的 WebSocket 来源，逗号分隔 |

### 3. 构建与运行

本机没有 HTTPS 出口，因此 npm 与 Go 模块代理都需要走 HTTP，且缓存必须放在
工作区内（默认缓存目录在工作区之外，会被文件沙箱拒绝）。`scripts/build.ps1`
已经把这些环境变量都设好了。

> 脚本调用的写法：本机可能只有 Windows PowerShell（`powershell.exe`），
> 且默认执行策略禁止运行未签名脚本，所以请用：
>
> ```powershell
> powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1 -Task all
> ```

```powershell
# 全部构建并跑测试
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1 -Task all

# 只跑测试
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1 -Task test

# 构建前端
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1 -Task web

# 直接启动（开发用）
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1 -Task run
```

手工等价命令：

```powershell
$ws = (Get-Location).Path
$env:GOCACHE="$ws\.cache\go-build"; $env:GOMODCACHE="$ws\.cache\go-mod"
$env:GOPROXY='http://goproxy.cn'; $env:GOSUMDB='off'; $env:GOTOOLCHAIN='local'

$env:npm_config_registry='http://registry.npmmirror.com'
$env:npm_config_cache="$ws\.cache\npm"

cd web;   npm install --ignore-scripts; npm run build; cd ..
cd server; go test ./... -count=1; go run ./cmd/server
```

> `--ignore-scripts` 是必须的：npm 自身的 rebuild 步骤会以管道方式启动子进程，
> 在当前沙箱下会被拒绝（`spawn EPERM`）。本项目没有需要本地编译的依赖
> （esbuild / rollup 的平台二进制随可选包一起安装）。
>
> `npm run build` 实际调用 `web/scripts/vite.mjs`：它先探测 esbuild 是否可用，
> 可用就走完全标准的 Vite 构建（并压缩），不可用则回退到进程内
> Rollup + `@vue/compiler-sfc`（产物不压缩）。两条路径共用同一个
> `vite.config.js`，所以在普通机器上行为和标准 Vite 完全一致。

打开 <http://127.0.0.1:8080> 即可。

### 4. 前端开发模式

```powershell
cd web; npm run dev
```

Vite 会把 `/api` 与 `/ws` 代理到 `http://127.0.0.1:8080`，所以先启动后端。

## 只跑后端（不构建前端）

`XIANGQI_PUBLIC_DIR` 指向不存在的目录时，静态托管会被跳过，REST 与 WebSocket
接口仍然完全可用：

```powershell
$env:XIANGQI_PUBLIC_DIR=''
cd server; go run ./cmd/server
```

## 用 Docker 部署

```bash
# 在仓库根目录执行（构建上下文需要同时包含 web 与 server）
docker build -f deploy/Dockerfile -t xiangqi:latest .
docker run --rm -p 8080:8080 \
  -e XIANGQI_DB_HOST=host.docker.internal \
  -e XIANGQI_JWT_SECRET='换成你自己的足够长的随机字符串' \
  xiangqi:latest
```

镜像分三段构建：Node 构建前端 → Go 构建静态二进制 → Alpine 运行时，
最终镜像以非 root 用户运行，并带有 `/api/health` 健康检查。

## 接口

- REST 与 WebSocket 的完整约定见 [docs/frontend-contract.md](docs/frontend-contract.md)
- 接口自检：`GET /api/health`
- 无需前端也能试玩 AI：`GET /api/ai/move?level=2`

## 测试

一条命令跑完整验收（**不需要 MySQL**）：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\verify.ps1
```

它会检查仓库结构、执行 `go build` / `go vet` / `go test ./...`、
校验前端构建产物（含中文界面文案与棋子汉字是否在打包后仍然完整）、
用一个随机空闲端口以内存存储启动服务，最后跑一遍真实的双人对局验收。
退出码 `0` 表示全部通过。

只跑单元测试：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1 -Task test
```

测试覆盖：

- `internal/game` — 规则引擎：开局 44 个合法着法、蹩马腿、塞象眼、隔子吃、
  白脸将、九宫限制、将死、困毙、中文记谱、悔棋，以及一条关键不变式——
  **`Play` 接受的着法与着法生成器给出的列表完全一致**（穷举全部 90×89 个格子对校验）。
- `internal/gamehall` — 对局状态机：匹配、走子、认输、超时、提和、悔棋、
  人机座位、积分结算、在线状态、快照中的合法着法，以及**不限时（两个时钟都为 0）
  的对局永远不会超时判负**（默认配置的回归测试）。
- `internal/ws` — WebSocket Hub：**会话与 Hub 之间不存在锁顺序死锁**（回归测试）、
  房间快照按连接携带 `youAre`、人机座位恰好被认领一次。
- `internal/api` — HTTP 层：健康检查、注册校验、令牌中间件（缺失/伪造/异密钥签名）、
  登录不泄露口令、畸形请求、CORS 预检、`/api/*` 统一 JSON 404，
  以及**中间件不得破坏 WebSocket 升级**（依赖 `http.Hijacker`，回归测试）。
- `internal/auth` — 口令哈希（随机盐、自描述格式、Unicode 口令）与令牌
  （往返、篡改、过期、异密钥）。
- `internal/store` — MySQL 集成测试。**没有可用的 MySQL 时会自动跳过**，
  可用 `XIANGQI_DB_HOST` / `XIANGQI_DB_USER` / `XIANGQI_DB_PASSWORD` 指向一个实例来启用。

### 端到端验收

`internal/gamehall`、`internal/ws`、`internal/api` 之外，还有一个真实
端到端验收程序，会启动两个 WebSocket 客户端互相对弈：

```powershell
# 一个终端：用内存存储启动服务（无需 MySQL）
cd server; go run ./cmd/server -store memory

# 另一个终端：跑验收
cd server; go run ./cmd/e2e -base http://127.0.0.1:8080
```

它会依次验证：健康检查、规则表、注册、登录与错误口令、两人入座同一房间、
服务端下发 44 个合法着法、非法着法被拒、双方各自收到每一步、认输产生
`game_over`、对局与全部着法（含中文记谱）落库、胜者积分上涨、
AI 接口返回合法着法、人机对局中电脑会应着。

### 不装 MySQL 也能跑

`-store memory` 使用进程内存储，整个站点（含 WebSocket 对战、人机对战、棋谱）
都能完整运行，只是重启后数据清空。适合演示与验收；生产请使用默认的 MySQL。

前端构建产物位于 `web/dist`，由 Go 服务器以静态文件方式托管，
并带单页应用回退（`/lobby`、`/game/1` 这类路由刷新后仍返回 `index.html`）。
如果 `web/dist` 不存在，服务端会跳过静态托管，只提供接口 —— 此时
`internal/api` 与 `internal/ws` 的功能完全不受影响。

## 接口自检

```powershell
# 健康检查
curl http://127.0.0.1:8080/api/health

# 不开前端也能试玩 AI（返回一步合法着法）
curl "http://127.0.0.1:8080/api/ai/move?level=2"
```

## 设计要点

- **服务端权威**：前端不实现任何象棋规则。服务端每次推送完整 FEN、轮次和
  `legalFrom`（起点 → 合法终点），前端只负责渲染与点击。
- **单一状态机**：所有对局变更都在 `gamehall.Session` 的互斥锁内完成，
  WebSocket 层只是传输；变更后通过回调推送快照。
- **持久化解耦**：`gamehall` 通过 `Recorder` 接口写库，`store` 通过
  `Persister` 接口建对局行，两边互不 import，避免循环依赖。
- **标准库优先**：规则引擎、口令哈希、JWT 全部用标准库实现，
  第三方依赖只有 MySQL 驱动和 WebSocket 库。
