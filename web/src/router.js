import { createRouter, createWebHistory } from 'vue-router'
import { state, connectRealtime } from './store.js'

const LoginView = () => import('./views/LoginView.vue')
const LobbyView = () => import('./views/LobbyView.vue')
const GameView = () => import('./views/GameView.vue')
const HistoryView = () => import('./views/HistoryView.vue')
const LeaderboardView = () => import('./views/LeaderboardView.vue')
const ProfileView = () => import('./views/ProfileView.vue')

const routes = [
  { path: '/', redirect: '/lobby' },
  { path: '/login', name: 'login', component: LoginView, meta: { title: '登录' } },
  { path: '/lobby', name: 'lobby', component: LobbyView, meta: { title: '大厅' } },
  // :id is optional: the lobby sends `join` first, then routes to the room.
  { path: '/game/:id?', name: 'game', component: GameView, meta: { title: '对局室' } },
  // :id is optional: without it the list is shown alone.
  { path: '/history/:id?', name: 'history', component: HistoryView, meta: { title: '棋谱' } },
  { path: '/leaderboard', name: 'leaderboard', component: LeaderboardView, meta: { title: '排行榜' } },
  { path: '/profile/:id?', name: 'profile', component: ProfileView, meta: { title: '个人资料', auth: true } },
  { path: '/:pathMatch(.*)*', redirect: '/lobby' }
]

const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 })
})

router.beforeEach((to) => {
  document.title = to.meta.title ? `${to.meta.title} · 中国象棋在线对战` : '中国象棋 · 在线对战'
  // The game socket is shared by every view; make sure it is running as soon as
  // the visitor lands on a page that needs live data.
  if (to.name === 'lobby' || to.name === 'game') connectRealtime()
  if (to.meta.auth && !state.token) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  return true
})

export default router
