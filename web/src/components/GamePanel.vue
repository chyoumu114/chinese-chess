<script setup>
// Player panel: both seats with clock and rating, whose turn it is, and the
// in-game action buttons (认输 / 提和 / 悔棋 / 聊天).

import { computed } from 'vue'
import { AI_LEVELS, fmtClock, sideLabel } from '../format.js'

const AI_KEY_LABEL = Object.fromEntries(AI_LEVELS.map((level) => [level.key, level.label]))

const props = defineProps({
  /** PlayerView for the red seat. */
  red: { type: Object, default: null },
  /** PlayerView for the black seat. */
  black: { type: Object, default: null },
  /** Side to move. */
  turn: { type: String, default: '' },
  /** Side this client plays. */
  youAre: { type: String, default: '' },
  /** "waiting" | "playing" | "finished". */
  status: { type: String, default: '' },
  /** Side that has offered a draw, if any. */
  drawOfferedBy: { type: String, default: '' },
  /** Side that has requested a takeback, if any. */
  takebackRequestedBy: { type: String, default: '' },
  /** Chat button visibility. */
  showChat: { type: Boolean, default: true }
})

const emit = defineEmits(['resign', 'offer-draw', 'request-takeback', 'chat', 'respond-draw', 'respond-takeback'])

const playing = computed(() => props.status === 'playing')
const seated = computed(() => !!props.youAre)

/**
 * The seat of the side this client plays is put next to the board, i.e. at the
 * bottom of the panel, so a black player sees their own clock on the same side
 * of the screen as their pieces. Spectators keep the standard black-then-red
 * order. The order is decided purely by CSS `order`, so nothing has to be
 * duplicated in the template.
 */
const selfAtBottom = computed(() => props.youAre === 'black')

const canResign = computed(() => playing.value && seated.value)
const canOfferDraw = computed(() => playing.value && seated.value && !props.drawOfferedBy)
const canRequestTakeback = computed(
  () => playing.value && seated.value && !props.takebackRequestedBy
)
const canChat = computed(() => seated.value || props.showChat)

/**
 * Whether this seat is actually counting down. The server reports msLeft 0 when
 * a seat has no clock (unlimited time), and such a seat must not look like it is
 * running out of time.
 */
function clocked(player) {
  return !!player && Number(player.msLeft) > 0
}

/** Whether this seat exists but plays without a clock. */
function unlimited(player) {
  return !!player && !clocked(player)
}

/** The opponent's outstanding prompts are the ones this client must answer. */
const incomingDraw = computed(
  () => !!props.drawOfferedBy && props.drawOfferedBy !== props.youAre
)
const incomingTakeback = computed(
  () => !!props.takebackRequestedBy && props.takebackRequestedBy !== props.youAre
)

const pendingSelfDraw = computed(
  () => !!props.drawOfferedBy && props.drawOfferedBy === props.youAre
)
const pendingSelfTakeback = computed(
  () => !!props.takebackRequestedBy && props.takebackRequestedBy === props.youAre
)

function seatClass(side) {
  return {
    seat: true,
    [`seat-${side}`]: true,
    'seat-turn': props.turn === side && playing.value,
    'seat-you': props.youAre === side
  }
}

function displayName(player) {
  if (!player) return '虚位以待'
  // 座位空出来以后端不带名字、也没有 userId：那是空位，不是某个匿名玩家。
  // 一方离开判负后，这里必须腾出来给下一个进来的人。
  if (!player.userId && !player.isAI) return '虚位以待'
  const name = player.name || '（匿名）'
  // The server labels the computer opponent with the engine's level key
  // ("电脑（easy）"); show the Chinese level name instead.
  return name.replace(/（(easy|normal|hard)）/g, (_, key) => `（${AI_KEY_LABEL[key] || key}）`)
}

/** True when the seat needs an explicit "computer" tag. */
function showAiTag(player) {
  return !!player && player.isAI && !String(player.name || '').startsWith('电脑')
}

/**
 * A seat only advertises readiness while a game is not running: before the first
 * move, and between games, when 准备 is what starts the next one.
 */
function showReady(player) {
  return props.status !== 'playing' && !!player && !!player.ready
}
</script>

<template>
  <div class="game-panel" :class="{ 'self-black': selfAtBottom }">
    <div :class="seatClass('black')">
      <div class="seat-main">
        <span class="side-badge black">黑</span>
        <div class="seat-text">
          <div class="seat-name">
            {{ displayName(black) }}
            <em v-if="showAiTag(black)" class="tag ai">电脑{{ black.aiLevel ? `（${black.aiLevel}）` : '' }}</em>
            <em v-if="youAre === 'black'" class="tag you">你</em>
            <em v-if="showReady(black)" class="tag ready">已准备</em>
          </div>
          <div class="seat-sub">
            <span v-if="black && black.rating">等级分 {{ black.rating }}</span>
            <span v-else-if="black && black.userId">编号 {{ black.userId }}</span>
            <span v-else>等待加入</span>
            <span v-if="black && !black.online" class="offline">· 离线</span>
          </div>
        </div>
      </div>
      <div
        class="clock"
        :class="{ ticking: turn === 'black' && playing && clocked(black), 'clock-open': unlimited(black) }"
      >
        {{ fmtClock(black ? black.msLeft : null) }}
      </div>
    </div>

    <div class="turn-strip">
      <template v-if="playing">
        <span class="turn-dot" :class="turn" />
        <span>{{ sideLabel(turn) }}走棋</span>
      </template>
      <template v-else-if="status === 'waiting'">
        <span class="turn-dot waiting" />
        <span>等待对手加入…</span>
      </template>
      <template v-else-if="status === 'finished'">
        <span>对局已结束</span>
      </template>
      <template v-else>
        <span>未入座</span>
      </template>
    </div>

    <div :class="seatClass('red')">
      <div class="seat-main">
        <span class="side-badge red">红</span>
        <div class="seat-text">
          <div class="seat-name">
            {{ displayName(red) }}
            <em v-if="showAiTag(red)" class="tag ai">电脑{{ red.aiLevel ? `（${red.aiLevel}）` : '' }}</em>
            <em v-if="youAre === 'red'" class="tag you">你</em>
            <em v-if="showReady(red)" class="tag ready">已准备</em>
          </div>
          <div class="seat-sub">
            <span v-if="red && red.rating">等级分 {{ red.rating }}</span>
            <span v-else-if="red && red.userId">编号 {{ red.userId }}</span>
            <span v-else>等待加入</span>
            <span v-if="red && !red.online" class="offline">· 离线</span>
          </div>
        </div>
      </div>
      <div
        class="clock"
        :class="{ ticking: turn === 'red' && playing && clocked(red), 'clock-open': unlimited(red) }"
      >
        {{ fmtClock(red ? red.msLeft : null) }}
      </div>
    </div>

    <div v-if="incomingDraw" class="prompt">
      <span>对手提议和棋</span>
      <div class="prompt-actions">
        <button class="btn btn-sm btn-primary" @click="emit('respond-draw', true)">接受</button>
        <button class="btn btn-sm" @click="emit('respond-draw', false)">拒绝</button>
      </div>
    </div>

    <div v-if="incomingTakeback" class="prompt">
      <span>对手请求悔棋</span>
      <div class="prompt-actions">
        <button class="btn btn-sm btn-primary" @click="emit('respond-takeback', true)">同意</button>
        <button class="btn btn-sm" @click="emit('respond-takeback', false)">拒绝</button>
      </div>
    </div>

    <div class="actions">
      <button
        class="btn btn-danger"
        :disabled="!canResign"
        :title="canResign ? '认输' : '对局未开始或未入座'"
        @click="emit('resign')"
      >认输</button>
      <button
        class="btn"
        :disabled="!canOfferDraw"
        :title="pendingSelfDraw ? '已提议，等待对手回应' : '提议和棋'"
        @click="emit('offer-draw')"
      >{{ pendingSelfDraw ? '已提和' : '提和' }}</button>
      <button
        class="btn"
        :disabled="!canRequestTakeback"
        :title="pendingSelfTakeback ? '已请求，等待对手回应' : '请求悔棋'"
        @click="emit('request-takeback')"
      >{{ pendingSelfTakeback ? '已请求' : '悔棋' }}</button>
      <button class="btn" :disabled="!canChat" @click="emit('chat')">聊天</button>
    </div>
  </div>
</template>

<style scoped>
.game-panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.seat {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: #fffdf8;
}

.seat-turn {
  border-color: var(--accent);
  box-shadow: 0 0 0 2px rgba(217, 162, 36, 0.22);
}

.seat-main {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.side-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 50%;
  font-weight: 700;
  font-family: 'KaiTi', 'STKaiti', 'Songti SC', 'SimSun', serif;
  flex: none;
}

.side-badge.red {
  color: #fff;
  background: #b3261e;
}

.side-badge.black {
  color: #fff;
  background: #1f2933;
}

.seat-text {
  min-width: 0;
}

.seat-name {
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.seat-sub {
  font-size: 0.8rem;
  color: var(--text-muted);
}

.offline {
  color: #d64545;
}

.tag {
  font-style: normal;
  font-size: 0.7rem;
  border-radius: 4px;
  padding: 1px 5px;
  margin-left: 5px;
  vertical-align: middle;
}

.tag.ai {
  background: #e8eef7;
  color: #2c5282;
}

.tag.you {
  background: #fbe6ac;
  color: #7a5a10;
}

.tag.ready {
  background: #eaf6ec;
  color: #276b36;
}

.clock {
  font-family: ui-monospace, 'SFMono-Regular', Consolas, monospace;
  font-size: 1.12rem;
  font-variant-numeric: tabular-nums;
  padding: 2px 8px;
  border-radius: 6px;
  background: #f3efe6;
  flex: none;
  white-space: nowrap;
}

.clock.ticking {
  background: #fbe6ac;
  color: #7a5a10;
  font-weight: 700;
}

/* Unlimited time: a word, not a countdown, so it is set quietly. */
.clock.clock-open {
  font-family: inherit;
  font-size: 0.95rem;
  color: var(--text-muted);
  background: transparent;
  padding: 2px 0;
}

/*
 * A black player reads the panel from their own side of the board: their seat
 * goes to the bottom (order 3) and the opponent's to the top (order 1). The turn
 * strip stays in the middle and the prompt/action blocks keep their place.
 */
.self-black .seat-red {
  order: 3;
}

.self-black .turn-strip {
  order: 2;
}

.self-black .seat-black {
  order: 1;
}

.turn-strip {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0.9rem;
  color: var(--text-muted);
  padding: 0 4px;
}

.turn-dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: #9aa5b1;
}

.turn-dot.red {
  background: #b3261e;
}

.turn-dot.black {
  background: #1f2933;
}

.turn-dot.waiting {
  background: var(--accent);
}

.prompt {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 8px;
  background: #fff6e0;
  border: 1px solid #e8cf94;
  font-size: 0.9rem;
}

.prompt-actions {
  display: flex;
  gap: 6px;
}

.actions {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
</style>
