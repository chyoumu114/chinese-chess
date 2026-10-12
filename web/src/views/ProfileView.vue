<script setup>
// Profile: the signed-in user's record and recent games.
// With /profile/:id it also renders any other account from GET /api/users/{id}.

import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../api.js'
import { state, loadMe, toast } from '../store.js'
import { fmtDateTime, myResult, outcomeLabel, reasonLabel } from '../format.js'

const route = useRoute()

const profile = ref(null)
const stats = ref(null)
const games = ref([])
const loading = ref(false)
const error = ref('')

const targetId = computed(() => Number(route.params.id || 0))
const isSelf = computed(() => !targetId.value || (state.user && state.user.id === targetId.value))
const displayName = computed(() => {
  const u = profile.value
  if (!u) return ''
  return u.nickname || u.username
})

const total = computed(() => {
  const s = stats.value
  if (!s) return 0
  return s.total ?? (s.wins || 0) + (s.losses || 0) + (s.draws || 0)
})

const winRate = computed(() => {
  if (!total.value) return '—'
  return `${Math.round(((stats.value.wins || 0) / total.value) * 100)}%`
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    if (isSelf.value) {
      if (!state.token) {
        error.value = '请先登录后查看个人资料'
        profile.value = null
        games.value = []
        return
      }
      const me = await loadMe()
      if (!me) {
        error.value = '登录状态已失效，请重新登录'
        return
      }
      profile.value = me.user
      stats.value = me.stats
      const list = await api.games({ limit: 10, mine: true })
      games.value = list.games || []
    } else {
      const data = await api.user(targetId.value)
      profile.value = data.user
      stats.value = data.stats
      games.value = data.games || []
    }
  } catch (err) {
    error.value = err.message || '加载失败'
    toast(error.value, 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(targetId, load)
watch(
  () => state.user && state.user.id,
  () => {
    if (isSelf.value) load()
  }
)

function favouriteSide() {
  const played = games.value.filter((g) => g.status === 'finished')
  if (!played.length) return '—'
  let red = 0
  let black = 0
  for (const g of played) {
    if (g.redUserId === profile.value?.id) red += 1
    else if (g.blackUserId === profile.value?.id) black += 1
  }
  if (red === black) return '红黑各半'
  return red > black ? '偏爱执红' : '偏爱执黑'
}

function resultOf(game) {
  if (!profile.value) return ''
  return myResult(game, profile.value.id)
}
</script>

<template>
  <div class="profile">
    <section class="card">
      <div class="card-head">
        <h1>{{ isSelf ? '我的资料' : '棋手资料' }}</h1>
        <button class="btn btn-sm" :disabled="loading" @click="load">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>

      <p v-if="error" class="form-error">{{ error }}</p>

      <template v-if="profile">
        <div class="who">
          <div class="avatar">{{ displayName.slice(0, 1) }}</div>
          <div>
            <h2 class="name">{{ displayName }}</h2>
            <p class="muted meta">
              @{{ profile.username }}
              <template v-if="profile.createdAt"> · 注册于 {{ fmtDateTime(profile.createdAt) }}</template>
            </p>
          </div>
          <div class="rating-box">
            <span class="rating-value">{{ profile.rating }}</span>
            <span class="muted">等级分</span>
          </div>
        </div>

        <div class="stat-grid">
          <div class="stat"><span class="stat-num">{{ stats ? stats.wins : '—' }}</span><span class="stat-label">胜</span></div>
          <div class="stat"><span class="stat-num">{{ stats ? stats.losses : '—' }}</span><span class="stat-label">负</span></div>
          <div class="stat"><span class="stat-num">{{ stats ? stats.draws : '—' }}</span><span class="stat-label">和</span></div>
          <div class="stat"><span class="stat-num">{{ total }}</span><span class="stat-label">总局数</span></div>
          <div class="stat"><span class="stat-num">{{ winRate }}</span><span class="stat-label">胜率</span></div>
          <div class="stat"><span class="stat-num small">{{ favouriteSide() }}</span><span class="stat-label">执子偏好</span></div>
        </div>
      </template>
    </section>

    <section v-if="profile" class="card">
      <div class="card-head">
        <h2>近期对局</h2>
        <span class="muted">最近 {{ games.length }} 局</span>
      </div>

      <table v-if="games.length" class="table">
        <thead>
          <tr>
            <th>编号</th>
            <th>红方</th>
            <th>黑方</th>
            <th>结果</th>
            <th>着数</th>
            <th>时间</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="g in games" :key="g.id">
            <td class="num">#{{ g.id }}</td>
            <td>{{ g.redName || '—' }}</td>
            <td>{{ g.blackName || '—' }}</td>
            <td>
              <span v-if="resultOf(g)" class="badge" :class="resultOf(g) === '胜' ? 'badge-green' : resultOf(g) === '负' ? 'badge-red' : 'badge-amber'">
                {{ resultOf(g) }}
              </span>
              <span class="muted"> {{ outcomeLabel(g.outcome) }}</span>
              <span class="muted"> {{ reasonLabel(g.reason) }}</span>
            </td>
            <td class="num">{{ g.moveCount }}</td>
            <td class="muted">{{ fmtDateTime(g.finishedAt || g.createdAt) }}</td>
            <td>
              <router-link v-if="g.status === 'finished'" class="btn btn-sm" :to="`/history/${g.id}`">复盘</router-link>
              <span v-else class="muted">进行中</span>
            </td>
          </tr>
        </tbody>
      </table>

      <p v-else class="empty">还没有对局记录。</p>
    </section>
  </div>
</template>

<style scoped>
.profile {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.who {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
}

.avatar {
  width: 52px;
  height: 52px;
  border-radius: 50%;
  background: #b3261e;
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1.4rem;
  font-family: 'KaiTi', 'STKaiti', 'Songti SC', 'SimSun', serif;
  flex: none;
}

.name {
  margin: 0;
}

.meta {
  margin: 2px 0 0;
}

.rating-box {
  margin-left: auto;
  text-align: right;
  display: flex;
  flex-direction: column;
}

.rating-value {
  font-size: 1.6rem;
  font-weight: 700;
  color: #b3261e;
  font-variant-numeric: tabular-nums;
  line-height: 1.1;
}

.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(96px, 1fr));
  gap: 10px;
  margin-top: 16px;
}

.stat {
  background: #f9f5ec;
  border: 1px solid var(--border-soft);
  border-radius: 10px;
  padding: 10px;
  text-align: center;
}

.stat-num {
  display: block;
  font-size: 1.25rem;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

.stat-num.small {
  font-size: 0.95rem;
}

.stat-label {
  font-size: 0.8rem;
  color: var(--text-muted);
}

.form-error {
  color: #b3261e;
}
</style>
