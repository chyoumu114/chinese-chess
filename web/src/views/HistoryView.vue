<script setup>
// Game history with a read-only replay board.
//
// Positions for the replay come from GET /api/games/{id}/moves: moves[i].fen is
// the position AFTER ply i+1, so position[0] is the start position.

import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import XiangqiBoard from '../components/XiangqiBoard.vue'
import MoveList from '../components/MoveList.vue'
import { api } from '../api.js'
import { state, startFen, loadRules, toast } from '../store.js'
import { myResult, moveSquares, outcomeLabel, reasonLabel, sideLabel, fmtDateTime } from '../format.js'

const route = useRoute()
const router = useRouter()

const games = ref([])
const loading = ref(false)
const listError = ref('')

const selected = ref(null)
const moves = ref([])
const movesLoading = ref(false)

const scope = ref('all') // 'all' | 'mine'
const ply = ref(0)

/** positions[0] is the start position; positions[i] is after ply i. */
const positions = computed(() => {
  const start = startFen()
  return [start, ...moves.value.map((m) => m.fen || start)]
})

const currentFen = computed(() => positions.value[ply.value] || positions.value[0])

const lastMove = computed(() => {
  if (ply.value === 0) return null
  // Stored moves only carry `uci` ("h7e7"), so the square pair is derived here.
  return moveSquares(moves.value[ply.value - 1])
})

const activeMove = computed(() => (ply.value > 0 ? moves.value[ply.value - 1] : null))

const resultLine = computed(() => {
  const g = selected.value
  if (!g) return ''
  return `${outcomeLabel(g.outcome)}${g.reason ? ' · ' + reasonLabel(g.reason) : ''}`
})

async function loadGames() {
  loading.value = true
  listError.value = ''
  try {
    const data = await api.games({
      limit: 30,
      status: 'finished',
      mine: scope.value === 'mine' && !!state.user
    })
    games.value = data.games || []
  } catch (err) {
    listError.value = err.message || '加载失败'
    games.value = []
  } finally {
    loading.value = false
  }
}

async function openGame(id) {
  movesLoading.value = true
  try {
    const data = await api.gameMoves(id)
    selected.value = data.game
    moves.value = data.moves || []
    ply.value = moves.value.length
    if (String(route.params.id || '') !== String(id)) {
      router.replace(`/history/${id}`)
    }
  } catch (err) {
    toast(err.message || '无法加载该对局', 'error')
  } finally {
    movesLoading.value = false
  }
}

onMounted(async () => {
  await loadRules()
  await loadGames()
  const id = Number(route.params.id || 0)
  if (id) await openGame(id)
})

watch(scope, (value) => {
  if (value === 'mine' && !state.user) {
    toast('请先登录再查看我的对局', 'warn')
    scope.value = 'all'
    return
  }
  loadGames()
})

watch(
  () => route.params.id,
  (raw) => {
    const id = Number(raw || 0)
    if (id && (!selected.value || selected.value.id !== id)) openGame(id)
    if (!id) {
      selected.value = null
      moves.value = []
      ply.value = 0
    }
  }
)

function step(delta) {
  const next = ply.value + delta
  if (next < 0 || next > moves.value.length) return
  ply.value = next
}

function toStart() {
  ply.value = 0
}

function toEnd() {
  ply.value = moves.value.length
}

function pickPly(p) {
  ply.value = p
}

function closeReplay() {
  selected.value = null
  moves.value = []
  ply.value = 0
  router.replace('/history')
}
</script>

<template>
  <div class="history">
    <section class="card">
      <div class="card-head">
        <h1>棋谱与复盘</h1>
        <div class="btn-row">
          <div class="seg">
            <button
              type="button"
              class="seg-btn"
              :class="{ active: scope === 'all' }"
              @click="scope = 'all'"
            >全部对局</button>
            <button
              type="button"
              class="seg-btn"
              :class="{ active: scope === 'mine' }"
              @click="scope = 'mine'"
            >我的对局</button>
          </div>
          <button class="btn btn-sm" :disabled="loading" @click="loadGames">
            {{ loading ? '加载中…' : '刷新' }}
          </button>
        </div>
      </div>

      <p v-if="listError" class="form-error">{{ listError }}</p>
      <p v-else-if="!games.length && !loading" class="empty">还没有已结束的对局。</p>

      <table v-else class="table">
        <thead>
          <tr>
            <th>编号</th>
            <th>红方</th>
            <th>黑方</th>
            <th>结果</th>
            <th>着数</th>
            <th>结束时间</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="g in games"
            :key="g.id"
            :class="{ selected: selected && selected.id === g.id }"
            @click="openGame(g.id)"
          >
            <td class="num">#{{ g.id }}</td>
            <td>{{ g.redName || '—' }}</td>
            <td>{{ g.blackName || '—' }}</td>
            <td>
              <span class="badge" :class="g.outcome === 'red_win' ? 'badge-red' : g.outcome === 'black_win' ? 'badge-black' : 'badge-amber'">
                {{ outcomeLabel(g.outcome) }}
              </span>
              <span class="muted"> {{ reasonLabel(g.reason) }}</span>
              <span v-if="myResult(g, state.user && state.user.id)" class="badge badge-green mine-tag">
                {{ myResult(g, state.user.id) }}
              </span>
            </td>
            <td class="num">{{ g.moveCount }}</td>
            <td class="muted">{{ fmtDateTime(g.finishedAt || g.createdAt) }}</td>
            <td>
              <button class="btn btn-sm" @click.stop="openGame(g.id)">复盘</button>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <section v-if="selected" class="card replay">
      <div class="card-head">
        <h2>复盘 · 对局 #{{ selected.id }}</h2>
        <div class="btn-row">
          <span class="badge" :class="selected.outcome === 'red_win' ? 'badge-red' : selected.outcome === 'black_win' ? 'badge-black' : 'badge-amber'">
            {{ resultLine }}
          </span>
          <button class="btn btn-sm" @click="closeReplay">关闭</button>
        </div>
      </div>

      <div class="replay-layout">
        <div class="replay-board">
          <XiangqiBoard
            :fen="currentFen"
            :legal-from="{}"
            :you-are="''"
            :turn="''"
            :interactive="false"
            :last-move="lastMove"
            :check-square="''"
          />

          <div class="replay-controls">
            <button class="btn btn-sm" :disabled="ply === 0" @click="toStart">⏮ 开局</button>
            <button class="btn btn-sm" :disabled="ply === 0" @click="step(-1)">◀ 上一着</button>
            <span class="ply-indicator">
              {{ ply }} / {{ moves.length }}
              <template v-if="activeMove">
                · {{ sideLabel(activeMove.side) }} {{ activeMove.notation }}
              </template>
            </span>
            <button class="btn btn-sm" :disabled="ply >= moves.length" @click="step(1)">下一着 ▶</button>
            <button class="btn btn-sm" :disabled="ply >= moves.length" @click="toEnd">末着 ⏭</button>
          </div>

          <input
            class="ply-slider"
            type="range"
            min="0"
            :max="moves.length"
            :value="ply"
            @input="pickPly(Number($event.target.value))"
          />

          <div class="replay-meta muted">
            <span>红方：{{ selected.redName || '—' }}</span>
            <span>黑方：{{ selected.blackName || '—' }}</span>
            <span>{{ selected.rated ? '计分对局' : '友谊对局' }}</span>
            <span v-if="selected.aiLevel">电脑难度 {{ selected.aiLevel }}</span>
          </div>
        </div>

        <div class="replay-moves">
          <p v-if="movesLoading" class="muted">正在载入着法…</p>
          <MoveList :moves="moves" :active-ply="ply" selectable @select="pickPly" />
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.history {
  display: flex;
  flex-direction: column;
  gap: 16px;
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
  font-size: 0.88rem;
  padding: 5px 12px;
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

tbody tr {
  cursor: pointer;
}

tbody tr.selected {
  background: #fbe6ac;
}

.mine-tag {
  margin-left: 4px;
}

.form-error {
  color: #b3261e;
}

.replay-layout {
  display: grid;
  grid-template-columns: minmax(280px, 1fr) minmax(260px, 340px);
  gap: 18px;
  align-items: start;
}

@media (max-width: 860px) {
  .replay-layout {
    grid-template-columns: 1fr;
  }
}

.replay-controls {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 12px;
}

.ply-indicator {
  font-size: 0.86rem;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}

.ply-slider {
  width: 100%;
  margin-top: 8px;
}

.replay-meta {
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
  margin-top: 8px;
  font-size: 0.82rem;
}
</style>
