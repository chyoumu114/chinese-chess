<script setup>
// Live game room: board, player panel, move list and chat.
//
// The board is rendered from the authoritative `room` snapshot only; moves are
// sent as { from, to } and the server echoes the new position back.
//
// Room lifecycle as this view sees it:
//   waiting  — one seat free; the player can dissolve the room and leave.
//   ready    — both seats taken; the game starts when both sides press 准备.
//   playing  — pieces move; 认输 / 提和 / 悔棋 / 离开房间 are available.
//   finished — the result is shown and both players may press 准备 to start the
//              next game in the SAME room with the colours swapped.

import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import XiangqiBoard from '../components/XiangqiBoard.vue'
import GamePanel from '../components/GamePanel.vue'
import MoveList from '../components/MoveList.vue'
import { protocol } from '../ws.js'
import { state, joinRoom, connectRealtime, startFen, toast } from '../store.js'
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

// ---- 准备 (ready) ----------------------------------------------------------

/** This player's seat, and the opponent's, from the authoritative snapshot. */
const mySeat = computed(() => (youAre.value ? (room.value && room.value[youAre.value]) || null : null))
const otherSide = computed(() => (youAre.value === 'red' ? 'black' : youAre.value === 'black' ? 'red' : ''))
const otherSeat = computed(() => (otherSide.value ? (room.value && room.value[otherSide.value]) || null : null))

/** A seat is ready only once the server says so; the computer is always ready. */
const youReady = computed(() => !!(mySeat.value && mySeat.value.ready))
const otherReady = computed(() => !!(otherSeat.value && otherSeat.value.ready))

/** Readiness per side, for the pre-game banner. */
const redReady = computed(() => !!(room.value && room.value.red && room.value.red.ready))
const blackReady = computed(() => !!(room.value && room.value.black && room.value.black.ready))

/** 准备 is meaningful before a game starts and after one has finished. */
const canReady = computed(() => !!youAre.value && (status.value === 'ready' || finished.value))

/**
 * A finished room whose opponent left cannot start anything: pressing 准备 there
 * reopens the room for a new opponent instead (the server decides, this only
 * explains it to the player).
 */
const opponentGone = computed(() => finished.value && !!otherSeat.value && !otherSeat.value.userId && !otherSeat.value.isAI)

const readyHint = computed(() => {
  if (youReady.value && !otherReady.value) {
    return otherSeat.value && otherSeat.value.name
      ? `已准备，等待 ${otherSeat.value.name} 准备…`
      : '已准备，正在等待新对手加入…'
  }
  if (otherReady.value && !youReady.value) return '对手已准备，等你点「准备」'
  return '双方都点「准备」后立即开始'
})

const readyLabel = computed(() => {
  if (youReady.value) return '已准备'
  if (opponentGone.value) return '准备并等待新对手'
  return finished.value ? '准备 · 换边再来一局' : '准备开始'
})

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

/** True when this client is the side that must answer a check. */
const iAmChecked = computed(() => !!room.value && room.value.inCheck && room.value.turn === youAre.value)

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
  if (detail.reason === 'abandoned') return winnerIsMe.value ? '对手离开房间，你获胜' : '你离开了对局，判负'
  return winnerIsMe.value ? '你赢了！' : '你输了，再接再厉'
})

const moveCount = computed(() => {
  const detail = result.value
  if (detail && typeof detail.moveCount === 'number') return detail.moveCount
  return moves.value.length
})

/** The draw offer this client made and is still waiting on. */
const myPendingDraw = computed(() => !!room.value && room.value.drawOfferedBy === youAre.value && !!youAre.value)
const myPendingTakeback = computed(
  () => !!room.value && room.value.takebackRequestedBy === youAre.value && !!youAre.value
)

/** True while the modal dialog is answering a request from the opponent. */
const incomingRequest = computed(
  () =>
    !!youAre.value &&
    ((!!state.pending.draw && state.pending.draw !== youAre.value) ||
      (!!state.pending.takeback && state.pending.takeback !== youAre.value))
)

// ---- realtime wiring -------------------------------------------------------
//
// Every frame (room / move_made / game_over / error / draw_offered /
// takeback_requested / chat / opponent_left / left) is folded into shared state
// by installRealtime() in store.js; this view only reads that state, so nothing
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

function onReady() {
  if (youReady.value) return
  protocol.ready()
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

/**
 * Leaving is always available: it cancels a quick match, and it forfeits a game
 * in progress (which is the only way to walk out of one). The server answers
 * with a `left` frame, which clears the shared snapshot.
 */
function onLeave() {
  const playing = status.value === 'playing'
  const question = playing
    ? '对局尚未结束，离开将判负。确定离开房间吗？'
    : status.value === 'waiting'
      ? '解散房间并停止匹配？'
      : '离开房间？'
  if (!window.confirm(question)) return
  if (!protocol.leave()) toast('连接已断开，请稍后重试', 'error')
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

      <!-- 被将军的提示要显眼：被将军时只能走能解将的着法。 -->
      <div v-else-if="room.inCheck" class="check-alert" :class="{ 'check-alert-mine': iAmChecked }">
        <span class="check-alert-icon" aria-hidden="true">将</span>
        <div>
          <div class="check-alert-title">
            {{ sideLabel(room.turn) }}被将军！{{ iAmChecked ? '必须应将' : '只能应将' }}
          </div>
          <div class="check-alert-sub">
            <template v-if="iAmChecked">只能走解将的着法（吃掉将军的子、垫将、或走将），送将无效。</template>
            <template v-else>对手必须走解将的着法，送将无效。</template>
          </div>
        </div>
      </div>

      <div v-else-if="status === 'waiting'" class="banner banner-info">
        等待对手加入…把房间号告诉朋友，或在大厅等待自动匹配
      </div>

      <!-- 准备阶段：双方都点了准备才开始。 -->
      <div v-else-if="status === 'ready'" class="ready-banner">
        <div class="ready-line">
          <span class="ready-side" :class="{ on: redReady }">红方{{ redReady ? '已准备' : '未准备' }}</span>
          <span class="ready-vs">VS</span>
          <span class="ready-side" :class="{ on: blackReady }">黑方{{ blackReady ? '已准备' : '未准备' }}</span>
        </div>
        <div class="ready-hint">{{ readyHint }}</div>
      </div>

      <!-- 一方的提和/悔棋请求还没答复时要一直看得见。 -->
      <div v-if="!incomingRequest && myPendingDraw" class="banner banner-pending">
        你已提议和棋，等待对手回应…
      </div>
      <div v-else-if="!incomingRequest && myPendingTakeback" class="banner banner-pending">
        你已请求悔棋，等待对手回应…
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
          <template v-else-if="status === 'waiting'">等待对手入座。</template>
          <template v-else-if="status === 'ready'">双方点「准备」后即可走棋。</template>
          <template v-else-if="interactive">
            轮到你了：点选己方棋子，再点绿色落点{{ room.inCheck ? '（被将军，必须应将）' : '' }}。
          </template>
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

      <div class="card ready-card">
        <button
          v-if="canReady"
          class="btn btn-primary ready-btn"
          :disabled="youReady"
          :title="youReady ? '已准备，等待对手' : '双方准备后开始对局'"
          @click="onReady"
        >{{ readyLabel }}</button>
        <p v-if="canReady" class="muted ready-card-hint">{{ readyHint }}</p>
        <p v-else-if="status === 'waiting'" class="muted ready-card-hint">
          匹配中：可以点「解散房间」取消匹配。
        </p>

        <div class="btn-row">
          <button class="btn btn-danger" @click="onLeave">
            {{ status === 'waiting' ? '解散房间' : '离开房间' }}
          </button>
          <button class="btn" @click="backToLobby">返回大厅</button>
        </div>
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

/* 提和/悔棋等待回应：常驻提示，不再只靠 toast */
.banner-pending {
  background: #fff6e0;
  border: 1px solid #e8cf94;
  color: #8a6410;
  text-align: center;
  animation: pending-pulse 1.8s ease-in-out infinite;
}

@keyframes pending-pulse {
  0%, 100% { box-shadow: 0 0 0 0 rgba(217, 162, 36, 0); }
  50% { box-shadow: 0 0 0 3px rgba(217, 162, 36, 0.25); }
}

/* ---- 将军提示 ---- */

.check-alert {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  border-radius: 10px;
  background: #fdeceb;
  border: 2px solid #e06a60;
  color: #a32a22;
  text-align: left;
}

.check-alert-mine {
  background: #fbdcd9;
  border-color: #c0392b;
  animation: check-alert-pulse 1.2s ease-in-out infinite;
}

@keyframes check-alert-pulse {
  0%, 100% { box-shadow: 0 0 0 0 rgba(192, 57, 43, 0); }
  50% { box-shadow: 0 0 0 5px rgba(192, 57, 43, 0.18); }
}

.check-alert-icon {
  flex: none;
  width: 38px;
  height: 38px;
  border-radius: 50%;
  background: #c0392b;
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-family: 'KaiTi', 'STKaiti', 'Songti SC', 'SimSun', serif;
  font-size: 1.3rem;
  font-weight: 700;
}

.check-alert-title {
  font-size: 1.1rem;
  font-weight: 700;
}

.check-alert-sub {
  font-size: 0.84rem;
  font-weight: 400;
  opacity: 0.9;
}

/* ---- 准备阶段 ---- */

.ready-banner {
  padding: 12px 14px;
  border-radius: 10px;
  background: #eef4fd;
  border: 2px solid #c3d7f2;
  color: #2c5282;
  text-align: center;
}

.ready-line {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  font-weight: 600;
}

.ready-side {
  padding: 2px 10px;
  border-radius: 999px;
  background: #fff;
  border: 1px solid #c3d7f2;
  font-size: 0.92rem;
}

.ready-side.on {
  background: #eaf6ec;
  border-color: #bde0c3;
  color: #276b36;
}

.ready-vs {
  font-size: 0.8rem;
  opacity: 0.6;
}

.ready-hint {
  margin-top: 6px;
  font-size: 0.86rem;
  font-weight: 400;
}

.ready-card {
  text-align: center;
}

.ready-btn {
  width: 100%;
  padding: 12px 14px;
  font-size: 1.02rem;
  font-weight: 700;
}

.ready-card-hint {
  margin: 8px 0 10px;
}

.ready-card .btn-row {
  justify-content: center;
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
