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

    <table v-else class="table lb-table">
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
          <td class="num rank" data-label="名次">{{ i + 1 }}</td>
          <td data-label="棋手">
            <router-link :to="`/profile/${u.id}`">{{ displayName(u) }}</router-link>
            <span v-if="u.nickname && u.nickname !== u.username" class="muted"> @{{ u.username }}</span>
            <span v-if="u.id === myId" class="badge badge-amber me-tag">我</span>
          </td>
          <td class="num rating" data-label="等级分">{{ u.rating }}</td>
          <td class="num" data-label="胜">{{ u.wins }}</td>
          <td class="num" data-label="负">{{ u.losses }}</td>
          <td class="num" data-label="和">{{ u.draws }}</td>
          <td class="num" data-label="总局数">{{ (u.wins || 0) + (u.losses || 0) + (u.draws || 0) }}</td>
          <td class="num" data-label="胜率">{{ winRate(u) }}</td>
          <td class="muted" data-label="最近在线">{{ fmtDateTime(u.lastSeenAt) }}</td>
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

/* ===== 手机端适配（排行榜 9 列 → 紧凑卡片）=====
   表格有 9 列，逐行「列名: 值」会把每张卡片拉得很长，
   因此这里用 4 行网格：名次+棋手 / 等级分 / 胜·负·和·总局数·胜率 / 最近在线。
   依赖当前列顺序（1 名次、2 棋手、3 等级分、4-8 战绩、9 最近在线）。
   桌面端不进入媒体查询，仍是普通表格。 */
@media (max-width: 640px) {
  .card-head {
    flex-wrap: wrap;
    gap: 8px;
  }

  .limit select {
    min-height: 40px;
  }

  .lb-table {
    display: block;
    width: 100%;
  }

  .lb-table thead {
    display: none;
  }

  .lb-table tbody {
    display: block;
  }

  .lb-table tr {
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    gap: 8px 6px;
    margin-bottom: 10px;
    padding: 10px 12px;
    border: 1px solid var(--border-soft);
    border-radius: 10px;
    background: #fffdf8;
  }

  .lb-table tr:last-child {
    margin-bottom: 0;
  }

  .lb-table tr:hover {
    background: #fffdf8;
  }

  .lb-table tr.me {
    background: #fff6e0;
    border-color: #e8cf94;
  }

  .lb-table td {
    display: block;
    min-width: 0;
    padding: 0;
    border: none;
    text-align: left;
    white-space: normal;
    overflow-wrap: anywhere;
  }

  /* 列名作为小标题放在数值上方 */
  .lb-table td::before {
    content: attr(data-label);
    display: block;
    font-size: 0.72rem;
    line-height: 1.3;
    color: var(--text-muted);
    font-weight: 600;
  }

  .lb-table td:nth-child(1) {
    grid-column: 1;
    grid-row: 1;
    align-self: center;
  }

  .lb-table td:nth-child(2) {
    grid-column: 2 / -1;
    grid-row: 1;
    font-size: 0.98rem;
    font-weight: 600;
  }

  .lb-table td:nth-child(3) {
    grid-column: 1 / -1;
    grid-row: 2;
  }

  .lb-table td:nth-child(4),
  .lb-table td:nth-child(5),
  .lb-table td:nth-child(6),
  .lb-table td:nth-child(7),
  .lb-table td:nth-child(8) {
    grid-row: 3;
  }

  .lb-table td:nth-child(9) {
    grid-column: 1 / -1;
    grid-row: 4;
  }

  .lb-table .rank {
    font-size: 1.05rem;
    font-weight: 700;
    color: var(--text);
  }

  .lb-table .rating {
    font-size: 1.15rem;
  }
}
</style>
