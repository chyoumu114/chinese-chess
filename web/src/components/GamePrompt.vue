<script setup>
// Blocking dialog for the requests that need a decision from this player.
//
// A draw or takeback offer used to be a single toast in the corner plus a thin
// strip inside the side panel, which players routinely missed. The answer is
// deliberately modal: it dims the page, sits in the centre of the screen and
// spells out who is asking and what each button does.
//
// It lives in the application shell rather than in the game view so an offer
// arriving while the player is browsing the lobby is just as visible.

import { computed, ref, watch } from 'vue'
import { state } from '../store.js'
import { protocol } from '../ws.js'
import { sideLabel } from '../format.js'

const youAre = computed(() => (state.room && state.room.youAre) || '')
const drawOfferedBy = computed(
  () => state.pending.draw || (state.room && state.room.drawOfferedBy) || ''
)
const takebackRequestedBy = computed(
  () => state.pending.takeback || (state.room && state.room.takebackRequestedBy) || ''
)

/** Only the opponent's requests are ours to answer. */
const incomingDraw = computed(() => !!drawOfferedBy.value && drawOfferedBy.value !== youAre.value)
const incomingTakeback = computed(
  () => !!takebackRequestedBy.value && takebackRequestedBy.value !== youAre.value
)

/** The offer currently on screen, used as the key of the "later" dismissal. */
const currentOffer = computed(() =>
  incomingDraw.value ? `draw:${drawOfferedBy.value}` : incomingTakeback.value ? `takeback:${takebackRequestedBy.value}` : ''
)

const dismissed = ref('')
watch(currentOffer, (key) => {
  // A new request always reopens the dialog, even after an earlier "later".
  if (key !== dismissed.value) dismissed.value = ''
})

const visible = computed(() => !!currentOffer.value && dismissed.value !== currentOffer.value)
const kind = computed(() => (incomingDraw.value ? 'draw' : incomingTakeback.value ? 'takeback' : ''))

const title = computed(() => (kind.value === 'draw' ? '对手提议和棋' : '对手请求悔棋'))

const detail = computed(() => {
  const side = sideLabel(kind.value === 'draw' ? drawOfferedBy.value : takebackRequestedBy.value)
  return kind.value === 'draw'
    ? `${side}提议和棋：接受后本局立即判和，双方都可点「准备」换边再来一局。`
    : `${side}请求悔棋：同意后撤销对手的上一着，轮次与棋盘都会回退。`
})

function respond(accept) {
  if (kind.value === 'draw') {
    protocol.respondDraw(accept)
    state.pending.draw = ''
  } else {
    protocol.respondTakeback(accept)
    state.pending.takeback = ''
  }
  dismissed.value = ''
}

/** 稍后处理 keeps the request pending; the panel and the banner still show it. */
function later() {
  dismissed.value = currentOffer.value
}
</script>

<template>
  <transition name="prompt">
    <div v-if="visible" class="prompt-backdrop" role="dialog" aria-modal="true">
      <div class="prompt-dialog" :class="`prompt-${kind}`">
        <div class="prompt-icon" aria-hidden="true">{{ kind === 'draw' ? '和' : '悔' }}</div>
        <h2 class="prompt-title">{{ title }}</h2>
        <p class="prompt-detail">{{ detail }}</p>

        <div class="prompt-buttons">
          <button class="btn prompt-accept" @click="respond(true)">
            {{ kind === 'draw' ? '接受和棋' : '同意悔棋' }}
          </button>
          <button class="btn prompt-decline" @click="respond(false)">
            {{ kind === 'draw' ? '拒绝，继续下' : '拒绝' }}
          </button>
        </div>

        <button class="prompt-later" @click="later">稍后处理</button>
      </div>
    </div>
  </transition>
</template>

<style scoped>
.prompt-backdrop {
  position: fixed;
  inset: 0;
  z-index: 900;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  background: rgba(31, 41, 51, 0.45);
  backdrop-filter: blur(2px);
}

.prompt-dialog {
  width: min(92vw, 420px);
  background: #fffdf8;
  border: 2px solid var(--accent);
  border-radius: 16px;
  padding: 20px 22px 14px;
  text-align: center;
  box-shadow: 0 18px 48px rgba(31, 41, 51, 0.32);
  animation: prompt-pop 0.18s ease-out;
}

@keyframes prompt-pop {
  from {
    transform: scale(0.94);
    opacity: 0;
  }
  to {
    transform: scale(1);
    opacity: 1;
  }
}

.prompt-icon {
  width: 46px;
  height: 46px;
  margin: 0 auto 8px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-family: 'KaiTi', 'STKaiti', 'Songti SC', 'SimSun', serif;
  font-size: 1.5rem;
  font-weight: 700;
  color: #fff;
  background: var(--accent);
}

.prompt-takeback .prompt-icon {
  background: #3b7dd8;
}

.prompt-takeback {
  border-color: #7fa9e8;
}

.prompt-title {
  margin: 0 0 8px;
  font-size: 1.3rem;
}

.prompt-detail {
  margin: 0 0 16px;
  color: var(--text-muted);
  font-size: 0.92rem;
  text-align: left;
}

.prompt-buttons {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}

.prompt-accept,
.prompt-decline {
  padding: 12px 10px;
  font-size: 1rem;
  font-weight: 600;
  border-radius: 10px;
}

.prompt-accept {
  background: #b3261e;
  border-color: #9c1f18;
  color: #fff;
}

.prompt-accept:hover:not(:disabled) {
  background: #9c1f18;
}

.prompt-decline {
  background: #fff;
}

.prompt-later {
  margin-top: 10px;
  border: none;
  background: transparent;
  color: var(--text-muted);
  font-size: 0.84rem;
  font-family: inherit;
  cursor: pointer;
  text-decoration: underline;
}

.prompt-enter-active,
.prompt-leave-active {
  transition: opacity 0.18s ease;
}

.prompt-enter-from,
.prompt-leave-to {
  opacity: 0;
}
</style>
