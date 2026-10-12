<script setup>
// Lobby: live roster, waiting rooms, quick match and human-vs-computer setup.

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api.js'
import { state, joinRoom, toast, connectRealtime } from '../store.js'
import { AI_LEVELS, fmtDateTime, presenceLabel, sideLabel } from '../format.js'

const router = useRouter()

const joining = ref(false)
const aiLevel = ref(2)
const aiSide = ref('red')
const health = ref(null)

const signedIn = computed(() => !!state.user)
const openRooms = computed(() => state.lobby.openRooms || [])
const online = computed(() => state.presence.online || [])

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

let poll = null

async function refreshLobby() {
  try {
    state.lobby = await api.lobby()
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
  poll = setInterval(() => {
    refreshLobby()
    refreshHealth()
  }, 8000)
})

onBeforeUnmount(() => {
  if (poll) clearInterval(poll)
})

function requireLogin() {
  if (signedIn.value) return true
  toast('请先登录再开始对局', 'warn')
  router.push({ name: 'login', query: { redirect: '/lobby' } })
  return false
}

function quickMatch() {
  if (!requireLogin()) return
  joining.value = true
  if (!joinRoom({})) joining.value = false
}

function joinOpenRoom(room) {
  if (!requireLogin()) return
  joining.value = true
  if (!joinRoom({ roomId: room.id, rated: room.rated })) joining.value = false
}

function startAI() {
  if (!requireLogin()) return
  joining.value = true
  if (!joinRoom({ aiLevel: aiLevel.value, side: aiSide.value, rated: false })) joining.value = false
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
          {{ joining ? '匹配中…' : '快速匹配' }}
        </button>
        <button class="btn" @click="refreshLobby">刷新</button>
      </div>
    </div>

    <div class="grid-2">
      <section class="card">
        <div class="card-head">
          <h2>等待中的房间</h2>
          <span class="muted">{{ openRooms.length }} 个</span>
        </div>

        <table v-if="openRooms.length" class="table">
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
                <button class="btn btn-sm" :disabled="joining" @click="joinOpenRoom(room)">加入</button>
              </td>
            </tr>
          </tbody>
        </table>

        <p v-else class="empty">暂时没有等待中的房间，试试「快速匹配」。</p>
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

        <button class="btn btn-primary" :disabled="joining" @click="startAI">开始人机对战</button>
      </div>

      <p v-if="!signedIn" class="muted ai-hint">登录后即可开始人机对战。</p>
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
