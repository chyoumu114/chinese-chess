<script setup>
// Application shell: header navigation, connection indicator, routed view and
// the global toast host.

import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { state, signOut, toast } from './store.js'
import ToastHost from './components/ToastHost.vue'
import GamePrompt from './components/GamePrompt.vue'

const router = useRouter()

const connLabel = computed(() => {
  switch (state.connection) {
    case 'open':
      return '已连接'
    case 'connecting':
      return '连接中…'
    case 'closed':
      return '已断开，重连中…'
    default:
      return '未连接'
  }
})

const displayName = computed(() => {
  if (!state.user) return ''
  return state.user.nickname || state.user.username || ''
})

// 「你正在对局中」只针对真正坐在座位上的自己：观战时 state.room 同样有值，
// 但那是别人的对局，不该冒出这条常驻横幅。
const inRoom = computed(() => !!(state.room && state.room.id && state.room.youAre))

function doSignOut() {
  signOut()
  router.push('/lobby')
}

function backToGame() {
  if (inRoom.value) router.push(`/game/${state.room.id}`)
}
</script>

<template>
  <div class="app-shell">
    <header class="app-header">
      <div class="app-header-inner">
        <router-link to="/lobby" class="brand">
          <span class="brand-mark">象</span>
          <span class="brand-text">中国象棋 · 在线对战</span>
        </router-link>

        <nav class="nav">
          <router-link to="/lobby">大厅</router-link>
          <router-link to="/history">棋谱</router-link>
          <router-link to="/leaderboard">排行榜</router-link>
          <router-link to="/profile">个人资料</router-link>
        </nav>

        <div class="header-right">
          <span class="conn" :class="`conn-${state.connection}`" :title="connLabel">
            <span class="conn-dot" /><span class="conn-text">{{ connLabel }}</span>
          </span>
          <template v-if="state.user">
            <span class="who">{{ displayName }}</span>
            <button class="btn btn-sm" @click="doSignOut">退出</button>
          </template>
          <router-link v-else class="btn btn-sm btn-primary" to="/login">登录</router-link>
        </div>
      </div>

      <div v-if="inRoom" class="resume-bar">
        <span>你正在对局中（第 {{ state.room.id }} 号房间）</span>
        <button class="btn btn-sm btn-primary" @click="backToGame">返回对局</button>
      </div>
    </header>

    <main class="app-main">
      <router-view />
    </main>

    <footer class="app-footer">
      <span>规则与判定由服务端提供 · 前端不实现象棋规则</span>
    </footer>

    <ToastHost />
    <!-- Draw and takeback requests are answered in a modal, wherever they arrive. -->
    <GamePrompt />
  </div>
</template>

<style scoped>
.app-header {
  position: sticky;
  top: 0;
  z-index: 100;
  background: rgba(255, 253, 248, 0.96);
  backdrop-filter: blur(6px);
  border-bottom: 1px solid var(--border);
}

.app-header-inner {
  max-width: 1180px;
  margin: 0 auto;
  padding: 10px 16px;
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
}

.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  text-decoration: none;
  color: var(--text);
  font-weight: 700;
}

.brand-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: #b3261e;
  color: #fff;
  font-family: 'KaiTi', 'STKaiti', 'Songti SC', 'SimSun', serif;
  font-size: 1.15rem;
}

.nav {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
  flex: 1;
}

.nav a {
  text-decoration: none;
  color: var(--text-muted);
  padding: 6px 12px;
  border-radius: 8px;
  font-size: 0.95rem;
}

.nav a:hover {
  background: #f3efe6;
  color: var(--text);
}

.nav a.router-link-active {
  background: #f6efe2;
  color: #b3261e;
  font-weight: 600;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 10px;
}

.conn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 0.82rem;
  color: var(--text-muted);
}

.conn-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #9aa5b1;
}

.conn-open .conn-dot {
  background: #2e9e4f;
}

.conn-connecting .conn-dot {
  background: #d9a224;
}

.conn-closed .conn-dot {
  background: #d64545;
}

.who {
  font-size: 0.9rem;
  font-weight: 600;
}

.resume-bar {
  background: #fff6e0;
  border-top: 1px solid #e8cf94;
  padding: 6px 16px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  font-size: 0.88rem;
}

.app-main {
  max-width: 1180px;
  margin: 0 auto;
  padding: 18px 16px 40px;
}

.app-footer {
  text-align: center;
  color: var(--text-muted);
  font-size: 0.8rem;
  padding: 18px;
  border-top: 1px solid var(--border);
}

/* ===== 手机端适配（导航栏不溢出）=====
   窄屏下顶部栏改为两行：第一行 品牌 + 账户区，第二行 导航（可换行）。
   桌面端（>=1024px）不进入任何媒体查询，视觉不变。 */
@media (max-width: 640px) {
  .app-header-inner {
    padding: 8px 12px;
    gap: 8px 10px;
  }

  /* 品牌是唯一可压缩项：空间不足时文本省略号，绝不撑破一行 */
  .brand {
    min-width: 0;
    flex: 0 1 auto;
  }

  .brand-mark {
    width: 28px;
    height: 28px;
    font-size: 1rem;
    flex: none;
  }

  .brand-text {
    font-size: 0.92rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* 导航换到第二行，独占整行；顺序在账户区之后 */
  .nav {
    order: 3;
    flex: 1 1 100%;
    flex-wrap: wrap;
    gap: 2px;
  }

  .nav a {
    padding: 7px 10px;
    font-size: 0.88rem;
  }

  .header-right {
    margin-left: auto;
    flex: none;
    gap: 8px;
  }

  .conn {
    font-size: 0.76rem;
    white-space: nowrap;
    min-width: 0;
  }

  /* 长昵称截断，避免把连接状态与退出按钮挤出行外 */
  .who {
    font-size: 0.84rem;
    max-width: 4.5em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .resume-bar {
    flex-wrap: wrap;
    gap: 8px;
    padding: 8px 12px;
    text-align: center;
  }

  .app-main {
    padding: 12px 12px 32px;
  }

  .app-footer {
    padding: 14px 12px;
    font-size: 0.76rem;
  }
}

/* 比目标机型（360px）更窄时：连接状态只留颜色圆点，title 仍带完整文案 */
@media (max-width: 359px) {
  .conn-text {
    display: none;
  }

  .brand-text {
    font-size: 0.85rem;
  }
}
</style>
