# xiangqi-web — 中国象棋在线对战前端

Vue 3（`<script setup>` + Composition API）+ Vite + Vue Router 实现的单页应用。
所有象棋规则、合法性判断、中文记谱与胜负判定都来自 Go 后端；前端只负责呈现 `room`
快照并通过 WebSocket 发送着法。

接口约定以 [`../docs/frontend-contract.md`](../docs/frontend-contract.md) 为准。

## 安装

后端默认监听 `http://127.0.0.1:8080`。依赖通过 HTTPS 镜像安装，并使用工作区内的
npm 缓存：

```powershell
$ws = '<仓库根目录>'
Set-Location "$ws\web"
$env:npm_config_registry = 'https://registry.npmmirror.com'
$env:npm_config_cache    = "$ws\.cache\npm"
$env:npm_config_fund     = 'false'
$env:npm_config_audit    = 'false'
$env:npm_config_update_notifier = 'false'
npm ci
```

> **受限 shell / 沙箱**：`npm install` 的 rebuild 阶段会以管道（pipe）方式启动依赖的
> 生命周期脚本，在禁止管道 stdio 的沙箱中会得到 `spawn EPERM`。此时改用
> `npm install --ignore-scripts` 即可 —— 本项目的依赖不需要本地编译，
> esbuild / rollup 的平台二进制随对应可选包一起下载。

## 开发、构建与预览

```powershell
npm run dev      # 开发服务器，默认 http://127.0.0.1:5173
npm run build    # 产物输出到 web/dist
npm run preview  # 本地预览 dist
npm test         # define 替换逻辑的单元测试
```

三个命令都通过 `scripts/vite.mjs` 调用 Vite，它只在必要时改写行为：

| 环境 | 行为 |
|---|---|
| esbuild 可用（普通机器） | 完全标准的 Vite：由 Vite 自己的加载器读取 `vite.config.js`，esbuild 负责转换与压缩 |
| esbuild 不可用（禁止管道 stdio 的沙箱） | `vite.config.js` 被原生 ESM 导入后交给 Vite（`configFile: false`），关闭 esbuild 转换与压缩、改由 `neutralizeDefinesPlugin` 完成 define 替换、开发模式下跳过依赖预打包。全部工作在进程内由 Rollup + `@vue/compiler-sfc` 完成 |

两种路径共用同一份 `vite.config.js`，因此 `base`、`outDir`、Vue 插件与开发代理只有一处
配置。唯一可见差异是：沙箱路径下的构建产物**不做压缩**（文件名仍然带哈希）。

Vite 开发服务器把 `/api` 与 `/ws`（`ws: true`）代理到 `http://127.0.0.1:8080`。
先启动 Go 后端，再启动前端。

`vite.config.js` 中 `base: '/'`、`build.outDir: 'dist'`，与后端 `XIANGQI_PUBLIC_DIR`
指向 `web/dist` 的静态托管方式一致；后端会把未知路径回落到 `index.html`，所以前端路由
可以直接刷新。

## 目录结构

```
web/
├── index.html              入口 HTML
├── vite.config.js          构建与开发代理配置（唯一配置来源）
├── scripts/
│   ├── vite.mjs            dev / build / preview 入口与 define 替换实现
│   └── defines.test.mjs    上述 define 替换的单元测试（npm test）
└── src/
    ├── main.js             应用启动：挂载、恢复会话、连接 WebSocket
    ├── App.vue             外壳：导航、连接状态、Toast 宿主
    ├── router.js           路由表
    ├── api.js              REST 封装（JWT、错误解包、每个接口一个函数）
    ├── ws.js               WebSocket 客户端（2 秒自动重连 + 重连后 sync）
    ├── store.js            reactive 共享状态 + 实时消息归并
    ├── format.js           中文文案与时间格式化
    ├── styles.css          全局样式
    ├── components/
    │   ├── XiangqiBoard.vue  SVG 棋盘（9 列 × 10 行）
    │   ├── GamePanel.vue     双方信息、剩余时间与操作按钮
    │   ├── MoveList.vue      中文记谱列表（红黑分列）
    │   └── ToastHost.vue     全局提示
    └── views/
        ├── LoginView.vue       登录 / 注册（同页切换）
        ├── LobbyView.vue       大厅：在线列表、等待房间、快速匹配、人机对战
        ├── GameView.vue        对局室：棋盘、面板、棋谱、聊天
        ├── HistoryView.vue     棋谱列表 + 只读复盘
        ├── LeaderboardView.vue 排行榜
        └── ProfileView.vue     个人资料与近期对局
```

## 路由

| 路径 | 页面 |
|---|---|
| `/login` | 登录 / 注册 |
| `/lobby` | 大厅（在线列表、等待房间、快速匹配、人机对战） |
| `/game/:id?` | 对局室（`id` 可省略：先 `join`，收到快照后跳转） |
| `/history/:id?` | 棋谱列表；带 `id` 直接打开该局的复盘棋盘 |
| `/leaderboard` | 排行榜 |
| `/profile/:id?` | 个人资料（无 `id` 表示自己，需要登录） |

## 棋盘方向

契约规定 FEN 第 0 排是黑方底线、第 9 排是红方底线，且「第 0 排在最上」。因此屏幕行号
等于 FEN 横排序号：

```
screenRow = fenRow        // 不是 9 - fenRow
```

以初始局面为例（`XiangqiBoard.vue` 实测，`CELL = 60`、`PAD = 34`、viewBox `0 0 548 608`）：

| 格子 | FEN 排 | 列 | 坐标 | 位置 |
|---|---|---|---|---|
| `e9` 红帅 `K`→帅 | 9 | 4 | x=274, y=574 | **底部正中** |
| `e0` 黑将 `k`→将 | 0 | 4 | x=274, y=34 | 顶部正中 |

红方朝行号减小的方向前进，即视觉上向上，与「红方在下」一致。

## 说明

- 前端不实现任何合法性判断：可落点完全来自服务端 `room.legalFrom`。
- 胜负横幅以 `room.result`（`status === "finished"` 时出现）为准，`game_over` 帧只作为
  即时通知；这样重连或漏帧后横幅依然正确。旧的 `room.outcome/reason/winnerSide` 作为兜底。
- `room.id` 是**房间号**，与 `games` 表里的 `game.id` 不是同一个编号，两者不可混用。
- WebSocket 断开后每 2 秒重连，重连成功立即发送 `sync` 以取回权威快照。
- 每个服务端 `error` 帧都会以 Toast 形式展示，不会被静默丢弃。
- 协议没有「离开房间」消息，因此离开对局室只是本地导航；顶部会出现「返回对局」入口。
- 复盘用 `GET /api/games/{id}/moves`：`moves[i].fen` 是第 `i+1` 着**之后**的局面，
  因此第 0 个局面是开局 FEN；该接口的着法只带 `uci`（如 `h7e7`），不含 `from/to`，
  格子对由 `uci` 解析（见 `src/format.js` 的 `moveSquares`）。
