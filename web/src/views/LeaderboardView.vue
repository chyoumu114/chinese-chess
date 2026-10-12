<script setup>
// Leaderboard: accounts ordered by rating.

import { computed, onMounted, ref } from 'vue'
import { api } from '../api.js'
import { state } from '../store.js'
import { fmtDateTime } from '../format.js'

const users = ref([])
const loading = ref(false)
const error = ref('')
const limit = ref(50)

const myId = computed(() => (state.user ? state.user.id : 0))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await api.leaderboard(limit.value)
    users.value = data.users || []
  } catch (err) {
    error.value = err.message || '加载失败'
    users.value = []
  } finally {
    loading.value = false
  }
}

function winRate(user) {
  const total = (user.wins || 0) + (user.losses || 0) + (user.draws || 0)
  if (!total) return '—'
  return `${Math.round(((user.wins || 0) / total) * 100)}%`
}

function displayName(user) {
  return user.nickname || user.username
}

onMounted(load)
</script>

<template>
  <section class="card">
    <div class="card-head">
      <h1>排行榜</h1>
      <div class="btn-row">
        <label class="limit">
          <span class="muted">显示</span>
          <select v-model.number="limit" @change="load">
            <option :value="20">前 20 名</option>
            <option :value="50">前 50 名</option>
            <option :value="100">前 100 名</option>
          </select>
        </label>
        <button class="btn btn-sm" :disabled="loading" @click="load">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <p v-if="error" class="form-error">{{ error }}</p>
    <p v-else-if="!users.length && !loading" class="empty">还没有上榜的棋手。</p>

    <table v-else class="table">
      <thead>
        <tr>
          <th>名次</th>
          <th>棋手</th>
          <th class="num">等级分</th>
          <th class="num">胜</th>
          <th class="num">负</th>
          <th class="num">和</th>
          <th class="num">总局数</th>
          <th class="num">胜率</th>
          <th>最近在线</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(u, i) in users" :key="u.id" :class="{ me: u.id === myId }">
          <td class="num rank">{{ i + 1 }}</td>
          <td>
            <router-link :to="`/profile/${u.id}`">{{ displayName(u) }}</router-link>
            <span v-if="u.nickname && u.nickname !== u.username" class="muted"> @{{ u.username }}</span>
            <span v-if="u.id === myId" class="badge badge-amber me-tag">我</span>
          </td>
          <td class="num rating">{{ u.rating }}</td>
          <td class="num">{{ u.wins }}</td>
          <td class="num">{{ u.losses }}</td>
          <td class="num">{{ u.draws }}</td>
          <td class="num">{{ (u.wins || 0) + (u.losses || 0) + (u.draws || 0) }}</td>
          <td class="num">{{ winRate(u) }}</td>
          <td class="muted">{{ fmtDateTime(u.lastSeenAt) }}</td>
        </tr>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
.limit {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.limit select {
  width: auto;
  padding: 5px 8px;
  font-size: 0.86rem;
}

.rank {
  color: var(--text-muted);
}

.rating {
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

tbody tr.me {
  background: #fff6e0;
}

.me-tag {
  margin-left: 6px;
}

.form-error {
  color: #b3261e;
}
</style>
