# 中国象棋在线对战 — 前后端接口契约

本文档是前端与后端之间的唯一接口约定。前端**不得**重复实现象棋规则：所有合法性判断、
走法列表、中文记谱、胜负判定都由后端提供。

## 一、棋盘坐标

- 棋盘 9 列 × 10 行，列 `a`–`i`（从左到右），行 `0`–`9`（从下到上）。
- 格子名格式：`<列字母><行数字>`，例如 `e0`、`b7`、`a9`。
- **FEN 的第 1 横排是第 0 行，属于黑方底线**。因此：
  - 黑方占第 0–4 行，九宫为第 0–2 行；
  - 红方占第 5–9 行，九宫为第 7–9 行；
  - 红方朝**行号减小**的方向前进（视觉上向上）。
- 初始局面 FEN：
  `rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1`

### 棋子字符（FEN 字母 → 显示汉字）

| 红（大写） | 汉字 | 黑（小写） | 汉字 |
|---|---|---|---|
| `K` | 帅 | `k` | 将 |
| `A` | 仕 | `a` | 士 |
| `B` | 相 | `b` | 象 |
| `N` | 马 | `n` | 马 |
| `R` | 车 | `r` | 车 |
| `C` | 炮 | `c` | 炮 |
| `P` | 兵 | `p` | 卒 |

> 注意：红黑双方的「马/车/炮」汉字相同，靠**颜色**区分（红方棋子用红色字，黑方用黑色字）。

### FEN 解析示例

前端渲染棋盘时按 FEN 逐横排解析：数字表示连续空位数，字母表示棋子。
解析结果索引 `row * 9 + col`，其中 `row` 就是 FEN 横排序号（第 0 排在最上）。

```
第 0 排（黑方底线） → row 0
第 9 排（红方底线） → row 9
```

### 棋盘朝向（显示层，不影响协议）

上面这套坐标是**协议坐标**，服务端永远用它下发 `fen`、`legalFrom`、`from`/`to`、
`checkSquare`。但**显示方向不属于协议**：对局室里，客户端执哪一方，就把哪一方画在
屏幕下方（`youAre === "black"` 时把棋盘上下调转，让黑方在下方）。因此前端必须做到：

- `legalFrom`、`lastMove`、`checkSquare` 里的格子名先按自己的朝向映射成屏幕坐标再绘制；
- 点击屏幕格子时反向映射回格子名，再放进 `move` 帧发出；
- 除了这两处映射，其它任何地方都不得假设方向（否则调转后会走错子）。

复盘、旁观（没有 `youAre`）时保持默认的红方在下。

## 二、REST 接口

所有响应均为 JSON。错误响应统一为：

```json
{ "error": { "code": "illegal_move", "message": "不能这样走" } }
```

`GET /api/health` → `{ "status": "ok", "database": "ok", "rooms": 3, "serverAt": 1700000000000 }`

`POST /api/auth/register`，请求 `{ "username": "...", "password": "...", "nickname": "..." }`
→ `201 { "token": "...", "expiresAt": "2024-01-01T00:00:00Z", "user": { ... } }`

`POST /api/auth/login`，请求 `{ "username": "...", "password": "..." }` → 同上（`200`）

`GET /api/auth/me`（需登录）→ `{ "user": {...}, "stats": { "wins": 0, "losses": 0, "draws": 0, "total": 0 } }`

User 对象：
```json
{ "id": 1, "username": "alice", "nickname": "爱丽丝", "rating": 1200,
  "wins": 3, "losses": 1, "draws": 0, "createdAt": "...", "lastSeenAt": "..." }
```

`GET /api/leaderboard?limit=50` → `{ "users": [ User, ... ] }`

`GET /api/lobby` → 等待中的房间 + 对局中的房间（后者可以进去观战）：

```json
{
  "openRooms": [ { "id": 1, "hostName": "alice", "hostSide": "red", "rated": true, "createdAt": "..." } ],
  "playingRooms": [ { "id": 2, "redName": "alice", "blackName": "bob", "moveCount": 17,
                      "rated": true, "spectators": 1, "createdAt": "..." } ],
  "playing": 2
}
```

`playingRooms` 只列出 `status === "playing"` 的房间。`blackName` / `redName` 可能是空字符串：
一方中途离开后房间仍保留那个空位。`spectators` 是当前观战人数。

`GET /api/users/{id}` → `{ "user": User, "stats": {...}, "games": [ Game, ... ] }`

`GET /api/games?limit=20&offset=0&mine=1&userId=3&status=finished` → `{ "games": [ Game, ... ] }`

Game 对象：
```json
{ "id": 7, "redUserId": 1, "blackUserId": 2, "redName": "alice", "blackName": "bob",
  "status": "finished", "outcome": "red_win", "reason": "checkmate", "winnerSide": "red",
  "fen": "...", "moveCount": 42, "redMsLeft": 300000, "blackMsLeft": 120000,
  "rated": true, "aiLevel": 0, "createdAt": "...", "startedAt": "...", "finishedAt": "..." }
```
`status` ∈ `waiting` | `playing` | `finished`；`outcome` ∈ `ongoing` | `red_win` | `black_win` | `draw`。

`reason` 取值：`checkmate`、`stalemate`、`king_capture`、`resign`、`timeout`、`draw_agreed`、
`sixty_move`、`abandoned`，以及三个由**三次重复局面**触发的值：

| reason | 含义 |
|---|---|
| `perpetual_check` | 单方长将：局面在来回重复，且该方**连续将军 5 次**，判该方负 |
| `perpetual_chase` | 单方长捉：局面在来回重复，且该方**连续 5 步都在追捉**对方一个无根的车/马/炮，判该方负 |
| `repetition` | 双方都不变（或都长将、都长捉）：同一局面第三次出现时判和 |

> 判负要求**连续 5 次**，并且必须**同时在重复**——一路将军直到将死是允许的。
> 门槛取 5 而不是 3 是刻意的：两三个回合的重复在中局很常见（兑子、抢先手），
> 直接判负会误杀正常纠缠。
>
> 有一方仍在长将 / 长捉（次数还没数满）时**不判和**，否则会在数到 5 之前被「和棋」
> 提前截断，这条规则就永远走不到。\n>
> 人机对战时电脑也会避开重复：搜索里只要局面第二次出现就按和棋打分，不会再把自己
> 走进长将判负。

`GET /api/games/{id}` → `{ "game": Game }`

`GET /api/games/{id}/moves` → `{ "game": Game, "moves": [ Move, ... ] }`，Move：
```json
{ "ply": 1, "side": "red", "uci": "h7e7", "notation": "炮二平五", "fen": "...", "check": false, "msSpent": 1200, "playedAt": "..." }
```

`GET /api/rules` → 棋盘几何、棋子表、初始 FEN、时钟毫秒数：
```json
{ "rows": 10, "cols": 9, "startFen": "...",
  "pieces": [ { "key": "red_rook", "side": "red", "type": "rook", "fen": "R", "glyph": "车" }, ... ],
  "turnClock": 0, "gameClock": 0, "notes": [ "红方先行", ... ] }
```
`turnClock` / `gameClock` 为 `0` 表示该项**不限时**（当前默认值即为 0：步时与局时都无限）。
只有运营方显式配置了 `XIANGQI_TURN_TIMEOUT` / `XIANGQI_GAME_TIMEOUT` 时才会是非 0 值。

`GET /api/ai/move?fen=<FEN>&level=1|2|3&budgetMs=800`
→ 有棋可走时 `{ "fen": "...", "from": "b2", "to": "e2", "notation": "炮八平五", "level": "normal" }`
→ 无棋可走时 `{ "fen": "...", "move": null, "gameOver": true, "reason": "checkmate" }`

## 三、WebSocket 协议

连接：`GET /ws`（`ws://host/ws`）。浏览器无法在握手时设置请求头，因此令牌可放在查询参数：
`/ws?token=<JWT>`。未登录也可以连接，但后端会拒绝入座。

所有帧都是 JSON 文本，外层信封统一为：

```json
{ "type": "<消息类型>", "data": { ... } }
```

### 客户端 → 服务端

| type | data | 说明 |
|---|---|---|
| `join` | `{ "roomId"?: number, "aiLevel"?: 1\|2\|3, "side"?: "red"\|"black", "rated"?: boolean }` | 入座。无 `roomId` 时自动匹配：有等待中的房间就加入，否则新建。带 `aiLevel` 时为单人对电脑。重复 `join` 会回到原有座位（已结束的房间除外：无 `roomId` 的 `join` 会离开旧房间并重新匹配）。 |
| `ready` | 无 | 准备。双方都 `ready` 后对局才开始；对局结束后双方再次 `ready` 即在**同一房间**换边开始下一局。人机对局中电脑永远算已准备。 |
| `leave` | 无 | 离开房间。等待中离开会解散房间（取消匹配）；对局进行中离开判负（`reason: "abandoned"`）；离开最后一个人会删除房间。 |
| `move` | `{ "from": "b7", "to": "e7" }` | 走棋。被将军时必须应将，其它着法一律被拒。 |
| `resign` | 无 | 认输。 |
| `offer_draw` | 无 | 提议和棋。 |
| `respond_draw` | `{ "accept": true\|false }` | 回应和棋提议。 |
| `request_takeback` | 无 | 请求悔棋。 |

> **人机对局中电脑一律同意**：`offer_draw` 立即判和（`reason: draw_agreed`，随后下发
> `game_over`）；`request_takeback` 立即撤销**两个半回合**（电脑的应招 + 玩家自己的着法，
> 回到玩家落子之前）并下发 `takeback_applied`。之所以撤两步：电脑早就应过招了，只撤一步
> 等于让它把同一着再走一遍。真人之间仍然是「请求 → 对手回应」的两次往返。
| `respond_takeback` | `{ "accept": true\|false }` | 回应悔棋请求。 |
| `chat` | `{ "text": "..." }` | 发送聊天（最多 200 字）。 |
| `watch` | `{ "roomId": 2 }` | **只观战**：订阅一个房间的更新，不占座位，对局进行中也能用。服务端回 `watching` 与一份 `room` 快照，其中 `youAre` 为空。已坐在另一个未结束房间里时会被拒（`already_seated`）。退出观战用 `leave`，不会判负。 |
| `sync` | 无 | 请求重发当前房间快照。 |
| `ping` | 无 | 心跳。 |

### 服务端 → 客户端

| type | data |
|---|---|
| `welcome` | `{ "userId": 1, "name": "alice", "guest": false, "serverAt": 1700000000000 }` |
| `room` | **权威房间快照**，见下 |
| `move_made` | `{ "roomId": 1, "from": "b7", "to": "e7", "notation": "炮八平五", "side": "red", "check": false, "byAI": false }` |
| `game_over` | `{ "outcome": "red_win", "reason": "checkmate", "winnerSide": "red", "fen": "...", "moveCount": 42 }` |
| `error` | `{ "code": "illegal_move", "message": "不能这样走" }` |
| `chat` | `{ "userId": 1, "name": "alice", "side": "red", "text": "...", "at": 1700000000000 }` |
| `draw_offered` | `{ "roomId": 1, "by": "red", "name": "alice" }` |
| `draw_declined` | `{ "roomId": 1, "by": "bob" }` |
| `takeback_requested` | `{ "roomId": 1, "by": "red", "name": "alice" }` |
| `takeback_applied` | `{ "roomId": 1 }` |
| `takeback_declined` | `{ "roomId": 1, "by": "bob" }` |
| `opponent_left` | `{ "roomId": 1, "name": "bob" }`，对方主动离开时额外带 `"left": true` |
| `left` | `{ "roomId": 1, "dissolved": true }`（回应 `leave`；`dissolved` 表示房间已被解散） |
| `presence` | `{ "online": [ { "userId": 1, "name": "alice", "roomId": 1, "status": "online"\|"playing"\|"waiting" } ], "rooms": 2 }` |

`status` 取**房间的真实状态**，不是数连接数：`playing` = 该用户所在房间正在对局；`waiting` =
已入座但还没开局（房间处于 `waiting` 或 `ready`）；`online` = 没有座位（包括只看不下的观众）。
**同一房间里两个人的状态必须一致**。
| `watching` | `{ "roomId": 2, "spectating": true }`（回应 `watch`，随后会下发 `room` 快照） |
| `pong` | 无 |

### 房间生命周期

一个房间会连续承载多局，状态机只有一条回路：

```
waiting ──第二位玩家入座──▶ ready ──双方都 ready──▶ playing ──终局──▶ finished
   ▲                          │                                     │
   └────────一方离开───────────┘                                     │
   ▲                                                                │
   └────────双方再次 ready（同一房间，红黑互换）───────────────────────┘
```

- `waiting`：只有一个座位有人（或双方都已离开后重开）。此时可 `leave` 解散房间。
- `ready`：两个座位都有人，但**必须双方都发 `ready` 才会开始**。`red.ready` / `black.ready`
  让客户端显示“已准备”。
- `playing`：`legalFrom` 才会下发；被将军的一方只能走解将的着法。
- 一方在对局中 `leave`（判负结束）后房间会留一个空位：`join { "roomId": N }` 可以补位入座，
  空位填满后房间回到「等双方 `ready`」的循环。**满座**的 `finished` 房间仍然拒绝加入。
- `finished`：`result` 出现。双方**不会**自动离开房间，可以再次 `ready` 在同一房间换边再战
  （`resetLocked` 会让红黑座位互换、棋盘重置，并为新对局另建一条 `games` 记录）。
  对手已经离开时，`ready` 会把房间重新放回大厅等待新对手。

### `room` 快照（关键结构）

```json
{
  "id": 1,
  "status": "waiting | ready | playing | finished",
  "fen": "当前局面 FEN",
  "turn": "red | black",
  "inCheck": false,
  "outcome": "ongoing | red_win | black_win | draw",
  "reason": "checkmate",
  "winnerSide": "red",
  "rated": true,
  "red":   { "userId": 1, "name": "alice", "side": "red",   "online": true,  "isAI": false, "ready": true, "msLeft": 300000, "moveCount": 20 },
  "black": { "userId": 0, "name": "电脑（2）", "side": "black", "online": true, "isAI": true, "aiLevel": 2, "ready": true, "msLeft": 280000, "moveCount": 20 },
  "moves": [ { "ply": 1, "side": "red", "from": "b7", "to": "e7", "notation": "炮八平五", "check": false, "msSpent": 1200 } ],
  "legalFrom": { "a9": ["a8", "a7"], "b7": ["a7", "c7", "..."] },
  "youAre": "red",
  "result": { "outcome": "red_win", "reason": "checkmate", "winnerSide": "red", "fen": "...", "moveCount": 42 },
  "drawOfferedBy": "red",
  "takebackRequestedBy": "",
  "spectators": 1
}
```

**要点：**
- `legalFrom` 只在 `status === "playing"` 时提供。键是起点格子名，值是合法终点数组。
  棋盘高亮**必须**以此为准，不要在前端实现规则。被将军时这里只会出现解将的着法
  （吃掉将军的子、垫将、走将），前端据此提示“必须应将”即可。
- `ready`（每个座位一个）表示该方是否已点「准备」。电脑座位永远为 `true`。
  只有 `status === "ready"` 或 `"finished"` 时才有意义。
- `youAre` 是后端按连接填入的，告诉这个客户端执哪一方。它同时决定棋盘的显示朝向
  （见 §一「棋盘朝向」）。换边重开后同一个连接的 `youAre` 会反转。
- `msLeft` 为 `0` 表示该座位**不限时**（当前默认：步时与局时都无限），界面应显示成
  「不限」而不是 `00:00`。只有运营方配置了限时才可能拿到非 0 值。
- `moves` 是完整着法列表（含中文记谱），刷新/重连后可直接重建棋谱面板。
- `fen` 每次都是完整局面，前端可只依赖它渲染棋盘，不需要自己推演。
- `result` 只在 `status === "finished"` 时出现，与 `game_over` 消息内容一致。
  **前端渲染胜负横幅应以 `room.result` 为准**，这样重连或漏帧也能正确显示。
  对手中途离开时 `reason` 为 `abandoned`。
- 注意 `room.id` 是**对局房间号**，`games` 表里的 `game.id` 是另一个编号，
  两者不相等；不要混用。同一个 `room.id` 下的多局对局在 `games` 表里是多条记录。

### 错误码

`login_required`（未登录）、`room_not_found`、`room_full`、`already_seated`（已坐在别处，不能观战）、
`not_seated`、`not_your_turn`、
`illegal_move`、`no_piece`、`wrong_side`、`finished`、`not_started`、`game_in_progress`、
`nothing_pending`、`bad_frame`、`bad_payload`、`bad_move`、`unknown_type`、`internal`。

## 四、前端实现要求

1. **技术栈**：Vue 3（`<script setup>` + Composition API）+ Vite + Vue Router。
   不引入状态管理库，用 `reactive`/`ref` 组合式模块即可。
2. **构建产物**：`web/dist`，由 Go 服务器以静态文件方式托管（`XIANGQI_PUBLIC_DIR`）。
   `vite.config.js` 中 `base: '/'`，`build.outDir: 'dist'`。
3. **开发代理**：`vite.config.js` 把 `/api` 与 `/ws` 代理到 `http://127.0.0.1:8080`
   （`/ws` 需要 `ws: true`）。
4. **棋盘**：用 SVG 绘制。9 列 10 行，画出横线、竖线、九宫斜线、
   "楚河 汉界" 文字、炮位与兵位的定位小十字。棋子为圆形，红字红边、黑字黑边。
   点击己方棋子时用 `legalFrom` 高亮可落点；点击高亮落点即发 `move`。
   **显示朝向**：客户端执黑时把棋盘上下调转，让黑方在屏幕下方（映射规则见 §一）。
   协议坐标与屏幕坐标之间只允许存在一处映射（本仓库是 `XiangqiBoard.vue` 里的
   `viewRow()` 与其反函数 `squareAt()`），落子前必须把屏幕格子换回 FEN 格子名。
5. **必须实现的页面**：
   - 登录/注册（同一页切换）
   - 大厅：在线列表、等待中的房间、快速匹配、人机对战（三档难度、可选执红/执黑）；
     已入座时必须能看到自己所在的房间并能取消匹配（发 `leave`）
   - 对局室：棋盘（按 `youAre` 决定朝向）、双方信息与剩余时间（不限时时显示「不限」）、
     着法列表（中文记谱，红黑分列）、认输/提和/悔棋/聊天按钮，以及：
     - `status === "ready"` 时显示「准备」按钮，并显示双方谁已准备；
     - `status === "finished"` 时显示「准备（换边再来一局）」，点后发 `ready`，
       同一房间换边重开，**不要新建房间**；
     - 「离开房间 / 解散房间」按钮（等待中即取消匹配，对局中会判负，需二次确认）；
     - 被将军（`inCheck`）时给出显眼提示：只能走 `legalFrom` 里的解将着法。
   - 棋谱与排行榜：历史对局列表、点击进入回放（只读棋盘 + 着法列表）
   - 个人资料：战绩与近期对局
6. **健壮性**：WebSocket 断线后按 2 秒间隔自动重连，重连成功立即发 `sync`。
   收到的每个 `error` 都要有可见提示（不要静默丢弃）。
   提和 / 悔棋这类需要对手答复的请求必须足够显眼：收到对方的请求用**模态弹窗**
   让玩家立刻看到并作答（本仓库是 `components/GamePrompt.vue`，挂在应用外壳上，
   在任何页面都能弹出）；自己发出的请求要留下常驻提示，不能只靠几秒就消失的 toast。
7. **棋盘交互细节**：棋盘区域要 `user-select: none`、`-webkit-user-select: none`、
   `-webkit-touch-callout: none`，并在容器上阻止 `contextmenu` —— 手机上长按棋盘不该弹出
   右键菜单，也不该选中棋子文字而触发「复制」。
8. **落子反馈**：收到走子（`move_made` 或 `room` 变化）时播放落子音效；页面处于后台标签页时，
   用标签页标题与**红色图标**闪烁提醒玩家，回到前台立即停止。音效可以直接用 Web Audio 合成，
   不必引入音频资源。
9. **语言**：界面文案用简体中文。

## 限流与容量

服务端对匿名入口做了令牌桶限流，命中后返回 HTTP **429**，响应体形如：

```json
{ "error": { "code": "rate_limited", "message": "请求过于频繁，请稍后再试" } }
```

| 入口 | 速率 | 突发 | 说明 |
|---|---|---|---|
| `POST /api/auth/register`、`POST /api/auth/login` | 20 次/分钟 | 10 | 按客户端 IP 计 |
| `GET /api/ai/move` | 20 次/分钟 | 4 | 另受全局 4 个并发槽位限制，槽位满时返回 **503** `engine_busy` |
| 其余 `/api/*` 与 `/ws` | 180 次/分钟 | 60 | 粗粒度兜底 |

客户端 IP 取自反向代理的 `X-Real-IP`，**仅当请求来自回环地址时才信任**（生产 8080 只绑
127.0.0.1），因此直连客户端无法伪造。

`GET /api/ai/move` 的 `budgetMs` 上限为 800ms：客户端只能请求更短的思考时间，
更长的值会被服务端截断。前端正常对局走 WebSocket，不经过该入口。

自动化测试或压测（同一 IP 打全流程）可设置 `XIANGQI_RATE_LIMIT=off` 关闭限流。
