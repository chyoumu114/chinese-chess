<script setup>
// Live game room: board, player panel, move list and chat.
//
// The board is rendered from the authoritative `room` snapshot only; moves are
// sent as { from, to } and the server echoes the new position back.

import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import XiangqiBoard from '../components/XiangqiBoard.vue'
import GamePanel from '../components/GamePanel.vue'
import MoveList from '../components/MoveList.vue'
import { protocol } from '../ws.js'
import { state, joinRoom, connectRealtime, clearRoom, startFen, toast } from '../store.js'
import { findKingSquare, resultText, sideLabel } from '../format.js'

const route = useRoute()
const router = useRouter()

const chatText = ref('')
const chatBox = ref(null)
const chatInput = ref(null)

const room = computed(() => state.room)
const moves = computed(() => (room.value && room.value.moves) || [])
const youAre = computed(() => (room.value && room.value.youAre) || '')
const status = computed(() => (room.value && room.value.status) || '')

/**
 * The terminal detail of the game. `room.result` is authoritative (the contract
 * requires the banner to follow it, so a reconnect or a missed `game_over` frame
 * still shows the right outcome); the old snapshot fields are the fallback.
 *
 * NOTE: `room.id` is the room number, while the `games` table uses its own id —
 * they are different numbers and are never interchangeable.
 */
const result = computed(() => {
  if (room.value && room.value.result) return room.value.result
  if (state.result) return state.result
  if (room.value && room.value.status === 'finished') {
    return {
      outcome: room.value.outcome,
      reason: room.value.reason,
      winnerSide: room.value.winnerSide,
      fen: room.value.fen,
      moveCount: moves.value.length
    }
  }
  return null
})

const finished = computed(() => status.value === 'finished' || !!result.value)

const fen = computed(() => {
  if (result.value && result.value.fen) return result.value.fen
  if (room.value && room.value.fen) return room.value.fen
  return startFen()
})

const interactive = computed(
  () => status.value === 'playing' && !!youAre.value && room.value.turn === youAre.value
)

/**
 * The player's own side is always drawn at the bottom of the board, so a client
 * playing black sees the board turned around. While spectating (no side) the
 * standard red-at-the-bottom view is kept.
 */
const boardOrientation = computed(() => youAre.value || 'red')

const checkSquare = computed(() => {
  if (!room.value || !room.value.inCheck) return ''
  return findKingSquare(fen.value, room.value.turn)
})

/** The clock and the rating shown for a seat. */
function seatView(player) {
  if (!player) return null
  const out = { ...player }
  if (state.user && player.userId && player.userId === state.user.id) {
    out.rating = state.user.rating
  }
  return out
}

const redSeat = computed(() => seatView(room.value && room.value.red))
const blackSeat = computed(() => seatView(room.value && room.value.black))

const winnerIsMe = computed(() => {
  const detail = result.value
  if (!detail) return false
  return detail.outcome !== 'draw' && !!detail.winnerSide && detail.winnerSide === youAre.value
})

const bannerClass = computed(() => {
  const outcome = result.value && result.value.outcome
  if (outcome === 'draw') return 'banner banner-draw'
  if (!youAre.value) return 'banner banner-info'
  return winnerIsMe.value ? 'banner banner-win' : 'banner banner-lose'
})

const bannerText = computed(() => {
  const detail = result.value
  if (!detail) return ''
  return resultText(detail.outcome, detail.reason)
})

const bannerSub = computed(() => {
  const detail = result.value
  if (!detail) return ''
  if (detail.outcome === 'draw') return '和棋'
  if (!youAre.value) return `你以旁观身份观看 · ${sideLabel(detail.winnerSide)}获胜`
  return winnerIsMe.value ? '你赢了！' : '你输了，再接再厉'
})

const moveCount = computed(() => {
  const detail = result.value
  if (detail && typeof detail.moveCount === 'number') return detail.moveCount
  return moves.value.length
})

// ---- realtime wiring -------------------------------------------------------
//
// Every frame (room / move_made / game_over / error / draw_offered /
// takeback_requested / chat / opponent_left) is folded into shared state by
// installRealtime() in store.js; this view only reads that state, so nothing
// has to be unsubscribed on unmount.

onMounted(() => {
  connectRealtime()
  const id = Number(route.params.id || 0)
  // Only re-seat when we are not already looking at that exact room.
  if (id && (!state.room || state.room.id !== id)) {
    joinRoom({ roomId: id })
  }
})

watch(
  () => route.params.id,
  (raw) => {
    const id = Number(raw || 0)
    if (id && (!state.room || state.room.id !== id)) joinRoom({ roomId: id })
  }
)

watch(
  () => state.chat.length,
  async () => {
    await nextTick()
    if (chatBox.value) chatBox.value.scrollTop = chatBox.value.scrollHeight
  }
)

// ---- actions ---------------------------------------------------------------

function onMove({ from, to }) {
  protocol.move(from, to)
}

function onResign() {
  if (window.confirm('确定认输吗？')) protocol.resign()
}

function onOfferDraw() {
  protocol.offerDraw()
  toast('已提议和棋，等待对手回应', 'info')
}

function onRequestTakeback() {
  protocol.requestTakeback()
  toast('已请求悔棋，等待对手回应', 'info')
}

function onRespondDraw(accept) {
  protocol.respondDraw(accept)
  state.pending.draw = ''
}

function onRespondTakeback(accept) {
  protocol.respondTakeback(accept)
  state.pending.takeback = ''
}

function sendChat() {
  const text = chatText.value.trim()
  if (!text) return
  if (!protocol.chat(text)) {
    toast('连接已断开，聊天未发送', 'error')
    return
  }
  chatText.value = ''
}

function focusChat() {
  if (chatInput.value) chatInput.value.focus()
}

function isMine(line) {
  if (!line) return false
  if (state.user && line.userId && state.user.id) return line.userId === state.user.id
  return false
}

function backToLobby() {
  router.push('/lobby')
}

function playAgain() {
  joinRoom({})
}

function leaveRoom() {
  clearRoom()
  router.push('/lobby')
}
</script>

<template>
  <div v-if="!room" class="card not-in-room">
    <h2>尚未入座</h2>
    <p class="muted">你当前不在任何对局中。请到大厅选择房间、快速匹配或开始人机对战。</p>
    <div class="btn-row">
      <button class="btn btn-primary" @click="backToLobby">前往大厅</button>
    </div>
  </div>

  <div v-else class="game-layout">
    <div class="side-column board-column">
      <div v-if="finished" :class="bannerClass">
        <div class="banner-main">{{ bannerText }}</div>
        <div class="banner-sub">{{ bannerSub }} · 共 {{ moveCount }} 着</div>
      </div>

      <div v-else-if="status === 'waiting'" class="banner banner-info">
        等待对手加入…把房间号告诉朋友，或在大厅等待自动匹配
      </div>

      <div v-else-if="room.inCheck" class="banner banner-lose check-banner">
        {{ sideLabel(room.turn) }}被将军！
      </div>

      <div class="board-card card">
        <div class="board-head">
          <span class="muted">
            房间 #{{ room.id }} · {{ room.rated ? '计分对局' : '友谊对局' }}
          </span>
          <span v-if="youAre" class="badge" :class="youAre === 'red' ? 'badge-red' : 'badge-black'">
            你执{{ sideLabel(youAre) }}
          </span>
          <span v-else class="badge">旁观</span>
        </div>

        <XiangqiBoard
          :fen="fen"
          :legal-from="(room && room.legalFrom) || {}"
          :you-are="youAre"
          :turn="room.turn"
          :interactive="interactive"
          :last-move="state.lastMove"
          :check-square="checkSquare"
          :orientation="boardOrientation"
          @move="onMove"
        />

        <p class="board-foot muted">
          <template v-if="finished">对局已结束，棋盘为最终局面。</template>
          <template v-else-if="status === 'waiting'">等待对手入座后即可走棋。</template>
          <template v-else-if="interactive">轮到你了：点选己方棋子，再点绿色落点。</template>
          <template v-else>等待{{ sideLabel(room.turn) }}走棋…</template>
        </p>
      </div>
    </div>

    <div class="side-column">
      <div class="card">
        <GamePanel
          :red="redSeat"
          :black="blackSeat"
          :turn="room.turn"
          :you-are="youAre"
          :status="status"
          :draw-offered-by="state.pending.draw || room.drawOfferedBy || ''"
          :takeback-requested-by="state.pending.takeback || room.takebackRequestedBy || ''"
          @resign="onResign"
          @offer-draw="onOfferDraw"
          @request-takeback="onRequestTakeback"
          @respond-draw="onRespondDraw"
          @respond-takeback="onRespondTakeback"
          @chat="focusChat"
        />
      </div>

      <div class="card">
        <MoveList :moves="moves" />
      </div>

      <div class="card chat-card">
        <div class="card-head">
          <h3>聊天</h3>
          <span class="muted">最多 200 字</span>
        </div>

        <div ref="chatBox" class="chat-box">
          <p v-if="!state.chat.length" class="empty">还没有人说话。</p>
          <div
            v-for="(line, i) in state.chat"
            :key="i"
            class="chat-line"
            :class="{ mine: isMine(line), red: line.side === 'red', black: line.side === 'black' }"
          >
            <span class="chat-who">{{ line.name || '匿名' }}</span>
            <span class="chat-text">{{ line.text }}</span>
          </div>
        </div>

        <form class="chat-form" @submit.prevent="sendChat">
          <input
            ref="chatInput"
            v-model="chatText"
            type="text"
            maxlength="200"
            placeholder="说点什么…"
            :disabled="!youAre"
          />
          <button class="btn btn-sm btn-primary" type="submit" :disabled="!youAre || !chatText.trim()">
            发送
          </button>
        </form>
      </div>

      <div class="card">
        <div class="btn-row">
          <button v-if="finished" class="btn btn-primary" @click="playAgain">再来一局</button>
          <button class="btn" @click="backToLobby">返回大厅</button>
          <button v-if="finished" class="btn" @click="leaveRoom">离开房间</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.not-in-room {
  max-width: 520px;
  margin: 40px auto;
  text-align: center;
}

.board-column {
  gap: 10px;
}

.board-card {
  padding: 12px;
}

.board-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}

.board-foot {
  margin: 10px 2px 0;
  text-align: center;
}

.banner {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.banner-main {
  font-size: 1.18rem;
}

.banner-sub {
  font-size: 0.86rem;
  font-weight: 400;
  opacity: 0.85;
}

.check-banner {
  font-size: 1rem;
}

.chat-card {
  display: flex;
  flex-direction: column;
}

.chat-box {
  border: 1px solid var(--border-soft);
  border-radius: 8px;
  padding: 8px;
  height: 180px;
  overflow-y: auto;
  background: #fffdf8;
  margin-bottom: 8px;
}

.chat-line {
  font-size: 0.88rem;
  margin-bottom: 5px;
  word-break: break-word;
}

.chat-who {
  font-weight: 600;
  margin-right: 6px;
}

.chat-line.red .chat-who {
  color: #b3261e;
}

.chat-line.black .chat-who {
  color: #2b3a45;
}

.chat-line.mine .chat-text {
  background: #eef4fd;
  border-radius: 4px;
  padding: 0 4px;
}

.chat-form {
  display: flex;
  gap: 8px;
}

.chat-form input {
  flex: 1;
}
</style>
