<script setup>
// Lobby: live roster, waiting rooms, quick match and human-vs-computer setup.
//
// 房间列表分成两个分类（tab）：等待中（openRooms，可以入座）与对战中
// （playingRooms，只能观战）。观战不占座位，所以走 WebSocket 的 `watch` 消息，
// 而不是会占座的 `join`。

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api.js'
import { protocol, ws } from '../ws.js'
import { state, joinRoom, watchRoom, toast, connectRealtime, loadLobby } from '../store.js'
import { AI_LEVELS, fmtDateTime, presenceLabel, sideLabel, statusLabel } from '../format.js'

const router = useRouter()

/** 分类页签的键，模板用它判断当前显示哪一类房间。 */
const TAB_OPEN = 'open'
const TAB_PLAYING = 'playing'

const joining = ref(false)
/** 已发出观战请求，正在等服务端的房间快照，收到快照后才跳转对局室。 */
const watching = ref(false)
const activeTab = ref(TAB_OPEN)
const aiLevel = ref(2)
const aiSide = ref('red')
const health = ref(null)

const signedIn = computed(() => !!state.user)
const openRooms = computed(() => state.lobby.openRooms || [])
/** 对局中的房间；旧后端不返回该字段，按空数组处理。 */
const playingRooms = computed(() => state.lobby.playingRooms || [])

/** 页签上的数量取自列表长度，避免和后端的计数口径不一致。 */
const tabs = computed(() => [
  { key: TAB_OPEN, label: '等待中', count: openRooms.value.length },
  { key: TAB_PLAYING, label: '对战中', count: playingRooms.value.length }
])

const online = computed(() => state.presence.online || [])
/** The room this visitor is currently seated in, if any. */
const currentRoom = computed(() => state.room)
/** 观众：快照里 youAre 为空字符串，说明没有座位，只是在看棋。 */
const spectating = computed(() => !!currentRoom.value && !currentRoom.value.youAre)
const inWaitingRoom = computed(() => !!currentRoom.value && currentRoom.value.status !== 'finished')
/**
 * A game in progress pins the player to its room: taking another seat would
 * forfeit it, so the lobby refuses to start one until they leave on purpose.
 */
const inGame = computed(() => !!currentRoom.value && currentRoom.value.status === 'playing')
const busyHint = computed(() => (inGame.value ? '对局进行中，请先在房间里离开' : ''))
/**
 * 观战会把共享的 room 快照换成被观看的房间，所以只要还坐在一个未结束的房间里
 * 就必须先离开，否则会丢掉自己的座位状态。
 */
const spectateDisabled = computed(() => watching.value || inWaitingRoom.value)
const spectateHint = computed(() =>
  inWaitingRoom.value ? '你已经在房间里了，请先离开再加入观战' : '进入观战（不占座位）'
)
/** 顶部主按钮文案：已在房间/观战中时是「回去」，否则是开始匹配。 */
const quickLabel = computed(() => {
  if (inWaitingRoom.value) return spectating.value ? '回到观战' : '返回我的房间'
  return joining.value ? '匹配中…' : '快速匹配'
})

// A join request only navigates once the authoritative room snapshot arrives.
watch(
  () => (state.room ? state.room.id : 0),
  (id) => {
    if (id && joining.value) {
      joining.value = false
      router.push(`/game/${id}`)
    }
  }
)

/**
 * 观战请求的回执。这里监听 `room` 帧，而不是复用上面的 id 监听：重复观战同一个
 * 房间时 state.room.id 不变、id 监听不会触发，而快照帧一定会到。
 */
let offRoom = null
let offError = null

function onRoomFrame(room) {
  if (!room || !watching.value) return
  watching.value = false
  // ?watch=1 告诉对局室这是观战：刷新观战页时不会误占空位。
  router.push(`/game/${room.id}?watch=1`)
}

/** 观战被拒绝（例如房间刚好结束）时解除等待，避免之后的快照误触发跳转。 */
function onErrorFrame() {
  watching.value = false
}

let poll = null

async function refreshLobby() {
  try {
    // loadLobby 负责归一化 playingRooms（缺失时按空数组），兼容旧后端。
    await loadLobby()
  } catch {
    /* the header badge already reports the connection state */
  }
}

async function refreshHealth() {
  try {
    health.value = await api.health()
  } catch {
    health.value = null
  }
}

onMounted(() => {
  connectRealtime()
  refreshLobby()
  refreshHealth()
  offRoom = ws.on('room', onRoomFrame)
  offError = ws.on('error', onErrorFrame)
  poll = setInterval(() => {
    refreshLobby()
    refreshHealth()
  }, 8000)
})

onBeforeUnmount(() => {
  if (poll) clearInterval(poll)
  if (offRoom) offRoom()
  if (offError) offError()
})

function requireLogin() {
  if (signedIn.value) return true
  toast('请先登录再开始对局', 'warn')
  router.push({ name: 'login', query: { redirect: '/lobby' } })
  return false
}

function quickMatch() {
  if (!requireLogin()) return
  // Already seated somewhere? Return to that room instead of asking for a second
  // seat, which would only bounce the player back to the same one.
  if (inWaitingRoom.value) {
    router.push(`/game/${currentRoom.value.id}`)
    return
  }
  joining.value = true
  if (!joinRoom({})) joining.value = false
}

function joinOpenRoom(room) {
  if (!requireLogin()) return
  if (inGame.value) {
    toast(busyHint.value, 'warn')
    return
  }
  joining.value = true
  if (!joinRoom({ roomId: room.id, rated: room.rated })) joining.value = false
}

/**
 * spectateRoom 纯观战：只发 `watch`，不占座位，服务端回的快照里 youAre 为空字符串。
 * 跳转由 onRoomFrame 在快照到达后完成，和「加入房间」的节奏保持一致。
 */
function spectateRoom(room) {
  if (!requireLogin()) return
  if (inWaitingRoom.value) {
    toast('请先离开当前房间再加入观战', 'warn')
    return
  }
  watching.value = true
  if (!watchRoom(room.id)) watching.value = false
}

function startAI() {
  if (!requireLogin()) return
  if (inGame.value) {
    toast(busyHint.value, 'warn')
    return
  }
  joining.value = true
  if (!joinRoom({ aiLevel: aiLevel.value, side: aiSide.value, rated: false })) joining.value = false
}

/**
 * cancelMatch gives up the seat from the lobby: it dissolves a room that is
 * still waiting and forfeits one that is under way. The server acknowledges it
 * with a `left` frame, which clears the shared snapshot.
 *
 * 观众没有座位，离开只是停止观战，所以不需要「判负」确认。
 */
function cancelMatch() {
  const playing = currentRoom.value && currentRoom.value.status === 'playing'
  if (playing && !spectating.value && !window.confirm('对局尚未结束，离开将判负。确定离开房间吗？')) return
  if (!protocol.leave()) {
    toast('连接已断开，请稍后重试', 'error')
    return
  }
  joining.value = false
  watching.value = false
}

function openRoom() {
  if (currentRoom.value) router.push(`/game/${currentRoom.value.id}`)
}
</script>

<template>
  <div class="lobby">
    <div class="lobby-head card">
      <div>
        <h1>对局大厅</h1>
        <p class="muted">
          在线 {{ online.length }} 人 · 进行中 {{ state.presence.rooms || state.lobby.playing || 0 }} 局
          <template v-if="health"> · 服务器 {{ health.status === 'ok' ? '正常' : '异常' }}（{{ health.database === 'ok' ? '数据库正常' : health.database }}）</template>
        </p>
      </div>
      <div class="btn-row">
        <button class="btn btn-primary" :disabled="joining" @click="quickMatch">
          {{ quickLabel }}
        </button>
        <button class="btn" @click="refreshLobby">刷新</button>
      </div>
    </div>

    <div v-if="currentRoom" class="card in-room-card">
      <div>
        <strong v-if="spectating">正在观看第 {{ currentRoom.id }} 号房间</strong>
        <strong v-else>你正在第 {{ currentRoom.id }} 号房间</strong>
        <span class="badge" :class="currentRoom.status === 'playing' ? 'badge-red' : 'badge-amber'">
          {{ statusLabel(currentRoom.status) }}
        </span>
        <p class="muted">
          <template v-if="spectating">观战中，离开即可停止观战，不影响双方对局。</template>
          <template v-else-if="currentRoom.status === 'waiting'">正在等待对手加入，可以随时取消匹配。</template>
          <template v-else-if="currentRoom.status === 'ready'">双方入座，去对局室点「准备」即可开始。</template>
          <template v-else-if="currentRoom.status === 'playing'">对局进行中。</template>
          <template v-else>对局已结束，可回对局室点「准备」换边再来一局。</template>
        </p>
      </div>
      <div class="btn-row">
        <button class="btn btn-primary btn-sm" @click="openRoom">{{ spectating ? '回到观战' : '返回对局' }}</button>
        <button v-if="currentRoom.status !== 'finished'" class="btn btn-sm btn-danger" @click="cancelMatch">
          {{ spectating ? '停止观战' : currentRoom.status === 'waiting' ? '取消匹配' : '离开房间' }}
        </button>
        <button v-else class="btn btn-sm" @click="cancelMatch">离开房间</button>
      </div>
    </div>

    <div class="grid-2">
      <section class="card">
        <div class="card-head">
          <h2>对局房间</h2>
          <div class="seg" role="tablist" aria-label="房间分类">
            <button
              v-for="tab in tabs"
              :key="tab.key"
              type="button"
              role="tab"
              class="seg-btn"
              :class="{ active: activeTab === tab.key }"
              :aria-selected="activeTab === tab.key"
              @click="activeTab = tab.key"
            >
              {{ tab.label }}<span class="tab-count">{{ tab.count }}</span>
            </button>
          </div>
        </div>

        <!-- 等待中：与改造前完全一致（点击加入、快速匹配）。 -->
        <template v-if="activeTab === TAB_OPEN">
          <div v-if="openRooms.length" class="table-scroll">
            <table class="table">
              <thead>
                <tr>
                  <th>房主</th>
                  <th>执子</th>
                  <th>计分</th>
                  <th>创建时间</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="room in openRooms" :key="room.id">
                  <td>{{ room.hostName || '（匿名）' }}</td>
                  <td>
                    <span class="badge" :class="room.hostSide === 'red' ? 'badge-red' : 'badge-black'">
                      {{ sideLabel(room.hostSide) }}
                    </span>
                  </td>
                  <td>{{ room.rated ? '计分' : '不计分' }}</td>
                  <td class="muted">{{ fmtDateTime(room.createdAt) }}</td>
                  <td>
                    <button
                      class="btn btn-sm"
                      :disabled="joining || inGame"
                      :title="busyHint || '加入这个房间'"
                      @click="joinOpenRoom(room)"
                    >加入</button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <p v-else class="empty">暂时没有等待中的房间，试试「快速匹配」。</p>
        </template>

        <!-- 对战中：任何人都可以进去观战，观战不占座位。 -->
        <template v-else>
          <div v-if="playingRooms.length" class="table-scroll">
            <table class="table">
              <thead>
                <tr>
                  <th>对阵</th>
                  <th class="num">步数</th>
                  <th>计分</th>
                  <th class="num">观战</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="room in playingRooms" :key="room.id">
                  <td>
                    <span class="side-tag">
                      <span class="badge badge-red">红</span>
                      {{ room.redName || '（空位）' }}
                    </span>
                    <span class="vs">vs</span>
                    <span class="side-tag">
                      <span class="badge badge-black">黑</span>
                      {{ room.blackName || '（空位）' }}
                    </span>
                  </td>
                  <td class="num">{{ room.moveCount || 0 }}</td>
                  <td>{{ room.rated ? '计分' : '不计分' }}</td>
                  <td class="num">{{ room.spectators || 0 }}</td>
                  <td>
                    <button
                      class="btn btn-sm"
                      :disabled="spectateDisabled"
                      :title="spectateHint"
                      @click="spectateRoom(room)"
                    >{{ watching ? '进入中…' : '观战' }}</button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <p v-else class="empty">当前没有进行中的对局。</p>
        </template>
      </section>

      <section class="card">
        <div class="card-head">
          <h2>在线棋友</h2>
          <span class="muted">每 10 秒刷新</span>
        </div>

        <table v-if="online.length" class="table">
          <thead>
            <tr>
              <th>昵称</th>
              <th>状态</th>
              <th>房间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(p, i) in online" :key="`${p.userId}-${i}`">
              <td>{{ p.name || '（匿名）' }}</td>
              <td>
                <span class="badge" :class="p.status === 'playing' ? 'badge-red' : p.status === 'waiting' ? 'badge-amber' : 'badge-green'">
                  {{ presenceLabel(p.status) }}
                </span>
              </td>
              <td class="muted">{{ p.roomId ? `#${p.roomId}` : '—' }}</td>
            </tr>
          </tbody>
        </table>

        <p v-else class="empty">暂无其他在线用户。</p>
      </section>
    </div>

    <section class="card">
      <div class="card-head">
        <h2>人机对战</h2>
        <span class="muted">不计入等级分</span>
      </div>

      <div class="ai-form">
        <div class="ai-group">
          <span class="ai-label">难度</span>
          <div class="seg">
            <button
              v-for="lvl in AI_LEVELS"
              :key="lvl.value"
              type="button"
              class="seg-btn"
              :class="{ active: aiLevel === lvl.value }"
              @click="aiLevel = lvl.value"
            >{{ lvl.label }}</button>
          </div>
        </div>

        <div class="ai-group">
          <span class="ai-label">执子</span>
          <div class="seg">
            <button
              type="button"
              class="seg-btn"
              :class="{ active: aiSide === 'red' }"
              @click="aiSide = 'red'"
            >执红（先行）</button>
            <button
              type="button"
              class="seg-btn"
              :class="{ active: aiSide === 'black' }"
              @click="aiSide = 'black'"
            >执黑（后行）</button>
          </div>
        </div>

        <button
          class="btn btn-primary"
          :disabled="joining || inGame"
          :title="busyHint || '开始一局人机对战'"
          @click="startAI"
        >开始人机对战</button>
      </div>

      <p v-if="!signedIn" class="muted ai-hint">登录后即可开始人机对战。</p>
      <p v-else-if="inGame" class="muted ai-hint">{{ busyHint }}。</p>
    </section>
  </div>
</template>

<style scoped>
.lobby {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.lobby-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  flex-wrap: wrap;
}

.lobby-head h1 {
  margin-bottom: 2px;
}

.lobby-head p {
  margin: 0;
}

/* 窄屏时分类页签另起一行，不挤压标题。 */
.card-head {
  flex-wrap: wrap;
}

/* 表格在窄屏下横向滚动，避免撑破卡片。 */
.table-scroll {
  overflow-x: auto;
}

.tab-count {
  display: inline-block;
  margin-left: 6px;
  min-width: 18px;
  padding: 0 5px;
  border-radius: 999px;
  background: #efe5d3;
  color: var(--text-muted);
  font-size: 0.74rem;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  text-align: center;
}

.seg-btn.active .tab-count {
  background: var(--accent);
  color: #fff;
}

.side-tag {
  white-space: nowrap;
}

.vs {
  padding: 0 8px;
  color: var(--text-muted);
  font-size: 0.82rem;
}

.in-room-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  flex-wrap: wrap;
  background: #fffdf5;
  border-color: #e8cf94;
}

.in-room-card p {
  margin: 4px 0 0;
}

.in-room-card .badge {
  margin-left: 8px;
}

.ai-form {
  display: flex;
  align-items: flex-end;
  gap: 18px;
  flex-wrap: wrap;
}

.ai-group {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.ai-label {
  font-size: 0.85rem;
  color: var(--text-muted);
}

.seg {
  display: inline-flex;
  background: #f6efe2;
  border-radius: 9px;
  padding: 3px;
  gap: 3px;
}

.seg-btn {
  border: none;
  background: transparent;
  font-family: inherit;
  font-size: 0.9rem;
  padding: 6px 12px;
  border-radius: 7px;
  cursor: pointer;
  color: var(--text-muted);
}

.seg-btn.active {
  background: #fffdf8;
  color: var(--text);
  font-weight: 600;
  box-shadow: 0 1px 3px rgba(31, 41, 51, 0.12);
}

.ai-hint {
  margin-bottom: 0;
}
</style>
