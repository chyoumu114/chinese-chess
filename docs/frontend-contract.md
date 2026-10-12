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

`GET /api/lobby` → `{ "openRooms": [ { "id": 1, "hostName": "alice", "hostSide": "red", "rated": true, "createdAt": "..." } ], "playing": 2 }`

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
| `join` | `{ "roomId"?: number, "aiLevel"?: 1\|2\|3, "side"?: "red"\|"black", "rated"?: boolean }` | 入座。无 `roomId` 时自动匹配：有等待中的房间就加入，否则新建。带 `aiLevel` 时为单人对电脑。重复 `join` 会回到原有座位。 |
| `move` | `{ "from": "b7", "to": "e7" }` | 走棋。 |
| `resign` | 无 | 认输。 |
| `offer_draw` | 无 | 提议和棋。 |
| `respond_draw` | `{ "accept": true\|false }` | 回应和棋提议。 |
| `request_takeback` | 无 | 请求悔棋。 |
| `respond_takeback` | `{ "accept": true\|false }` | 回应悔棋请求。 |
| `chat` | `{ "text": "..." }` | 发送聊天（最多 200 字）。 |
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
| `opponent_left` | `{ "roomId": 1, "name": "bob" }` |
| `presence` | `{ "online": [ { "userId": 1, "name": "alice", "roomId": 1, "status": "online"\|"playing"\|"waiting" } ], "rooms": 2 }` |
| `pong` | 无 |

### `room` 快照（关键结构）

```json
{
  "id": 1,
  "status": "waiting | playing | finished",
  "fen": "当前局面 FEN",
  "turn": "red | black",
  "inCheck": false,
  "outcome": "ongoing | red_win | black_win | draw",
  "reason": "checkmate",
  "winnerSide": "red",
  "rated": true,
  "red":   { "userId": 1, "name": "alice", "side": "red",   "online": true,  "isAI": false, "msLeft": 300000, "moveCount": 20 },
  "black": { "userId": 0, "name": "电脑（2）", "side": "black", "online": true, "isAI": true, "aiLevel": 2, "msLeft": 280000, "moveCount": 20 },
  "moves": [ { "ply": 1, "side": "red", "from": "b7", "to": "e7", "notation": "炮八平五", "check": false, "msSpent": 1200 } ],
  "legalFrom": { "a9": ["a8", "a7"], "b7": ["a7", "c7", "..."] },
  "youAre": "red",
  "result": { "outcome": "red_win", "reason": "checkmate", "winnerSide": "red", "fen": "...", "moveCount": 42 },
  "drawOfferedBy": "red",
  "takebackRequestedBy": ""
}
```

**要点：**
- `legalFrom` 只在 `status === "playing"` 时提供。键是起点格子名，值是合法终点数组。
  棋盘高亮**必须**以此为准，不要在前端实现规则。
- `youAre` 是后端按连接填入的，告诉这个客户端执哪一方。它同时决定棋盘的显示朝向
  （见 §一「棋盘朝向」）。
- `msLeft` 为 `0` 表示该座位**不限时**（当前默认：步时与局时都无限），界面应显示成
  「不限」而不是 `00:00`。只有运营方配置了限时才可能拿到非 0 值。
- `moves` 是完整着法列表（含中文记谱），刷新/重连后可直接重建棋谱面板。
- `fen` 每次都是完整局面，前端可只依赖它渲染棋盘，不需要自己推演。
- `result` 只在 `status === "finished"` 时出现，与 `game_over` 消息内容一致。
  **前端渲染胜负横幅应以 `room.result` 为准**，这样重连或漏帧也能正确显示。
- 注意 `room.id` 是**对局房间号**，`games` 表里的 `game.id` 是另一个编号，
  两者不相等；不要混用。

### 错误码

`login_required`（未登录）、`room_not_found`、`room_full`、`not_seated`、`not_your_turn`、
`illegal_move`、`no_piece`、`wrong_side`、`finished`、`not_started`、`nothing_pending`、
`bad_frame`、`bad_payload`、`bad_move`、`unknown_type`、`internal`。

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
   - 大厅：在线列表、等待中的房间、快速匹配、人机对战（三档难度、可选执红/执黑）
   - 对局室：棋盘（按 `youAre` 决定朝向）、双方信息与剩余时间（不限时时显示「不限」）、
     着法列表（中文记谱，红黑分列）、认输/提和/悔棋/聊天按钮
   - 棋谱与排行榜：历史对局列表、点击进入回放（只读棋盘 + 着法列表）
   - 个人资料：战绩与近期对局
6. **健壮性**：WebSocket 断线后按 2 秒间隔自动重连，重连成功立即发 `sync`。
   收到的每个 `error` 都要有可见提示（不要静默丢弃）。
7. **语言**：界面文案用简体中文。
