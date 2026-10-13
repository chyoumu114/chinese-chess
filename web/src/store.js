// Shared presentation state. There is no state-management library: a single
// reactive object plus a few helpers is enough for this application.
//
// All server-driven facts live here so that navigating between views never
// loses the authoritative room snapshot or the chat history.

import { reactive, computed } from 'vue'
import { api, getToken, setToken, clearToken } from './api.js'
import { ws, protocol } from './ws.js'

export const state = reactive({
  /** JWT as stored in localStorage. */
  token: getToken(),
  /** Signed-in User object, or null for a guest. */
  user: null,
  /** { wins, losses, draws, total } for the signed-in user. */
  stats: null,
  /** 'idle' | 'connecting' | 'open' | 'closed' */
  connection: 'idle',
  /** Authoritative RoomView snapshot, or null outside a game. */
  room: null,
  /** { from, to } of the most recently announced move. */
  lastMove: null,
  /** Terminal detail of the game: mirrors `room.result` / the `game_over` frame. */
  result: null,
  /** Server-side lobby roster: { online: [...], rooms: n }. */
  presence: { online: [], rooms: 0 },
  /** Chat lines for the current room. */
  chat: [],
  /** Outstanding draw/takeback prompts: { draw: side|'', takeback: side|'' }. */
  pending: { draw: '', takeback: '' },
  /** Toast notifications: { id, kind, text }. */
  toasts: [],
  /** Lobby payload from GET /api/lobby. playingRooms are games under way. */
  lobby: { openRooms: [], playingRooms: [], playing: 0 },
  /** Cached GET /api/rules payload (board geometry, start FEN). */
  rules: null
})

export const isSignedIn = computed(() => !!state.user)
export const youAre = computed(() => (state.room && state.room.youAre) || '')

let toastSeq = 0

/**
 * toast surfaces a message to the user. Every server `error` frame goes through
 * here, so no failure is ever silent.
 */
export function toast(text, kind = 'info', ttl = 4500) {
  if (!text) return
  const id = ++toastSeq
  state.toasts.push({ id, kind, text: String(text) })
  setTimeout(() => dismissToast(id), ttl)
  return id
}

export function dismissToast(id) {
  const i = state.toasts.findIndex((t) => t.id === id)
  if (i >= 0) state.toasts.splice(i, 1)
}

export function toastError(message, code) {
  const suffix = code && code !== 'unknown' ? `（${code}）` : ''
  return toast(`${message}${suffix}`, 'error', 6000)
}

// ---- session ---------------------------------------------------------------

/** applySession stores a login/registration result. */
export function applySession(payload) {
  if (!payload) return
  state.token = payload.token || ''
  setToken(state.token)
  state.user = payload.user || null
  state.stats = payload.stats || null
}

/** loadMe refreshes the signed-in profile, clearing a stale token. */
export async function loadMe() {
  if (!state.token) return null
  try {
    const data = await api.me()
    state.user = data.user
    state.stats = data.stats
    return data
  } catch (err) {
    if (err.status === 401) {
      signOut(false)
    }
    return null
  }
}

/** signOut forgets the session; announce=false keeps the UI quiet. */
export function signOut(announce = true) {
  clearToken()
  state.token = ''
  state.user = null
  state.stats = null
  state.room = null
  state.chat = []
  state.pending = { draw: '', takeback: '' }
  ws.close()
  if (announce) toast('已退出登录', 'info')
}

// ---- realtime wiring -------------------------------------------------------

let installed = false

/**
 * installRealtime routes server frames into shared state. It is called once
 * from main.js; views add their own subscriptions for UI-specific reactions.
 */
export function installRealtime() {
  if (installed) return
  installed = true

  ws.onStatus((status) => {
    state.connection = status
  })

  ws.on('welcome', (data) => {
    if (data && data.name && !state.user) {
      state.user = { id: data.userId || 0, username: data.name, nickname: data.name }
    }
  })

  ws.on('room', (room) => {
    if (!room) return
    state.room = room
    // The snapshot carries the terminal detail once the game is decided, so a
    // reconnect (or a missed game_over frame) still renders the right banner.
    if (room.status === 'finished') {
      state.result = room.result || {
        outcome: room.outcome,
        reason: room.reason,
        winnerSide: room.winnerSide,
        fen: room.fen,
        moveCount: (room.moves || []).length
      }
    } else {
      state.result = null
    }
    state.pending.draw = room.drawOfferedBy || ''
    state.pending.takeback = room.takebackRequestedBy || ''
    const moves = room.moves || []
    const last = moves[moves.length - 1]
    state.lastMove = last ? { from: last.from, to: last.to } : null
  })

  ws.on('move_made', (data) => {
    if (!data) return
    state.lastMove = { from: data.from, to: data.to }
    if (data.check) toast('将军！', 'warn', 2500)
  })

  ws.on('game_over', (data) => {
    state.result = data || null
    if (state.room) {
      state.room = {
        ...state.room,
        status: 'finished',
        outcome: (data && data.outcome) || state.room.outcome,
        reason: (data && data.reason) || state.room.reason,
        winnerSide: (data && data.winnerSide) || state.room.winnerSide,
        fen: (data && data.fen) || state.room.fen
      }
    }
    state.pending = { draw: '', takeback: '' }
  })

  ws.on('error', (data) => {
    toastError((data && data.message) || '操作失败', data && data.code)
  })

  ws.on('chat', (line) => {
    if (!line) return
    state.chat.push(line)
    if (state.chat.length > 300) state.chat.splice(0, state.chat.length - 300)
  })

  ws.on('draw_offered', (data) => {
    state.pending.draw = (data && data.by) || ''
    toast(`${(data && data.name) || '对手'} 提议和棋`, 'warn')
  })
  ws.on('draw_declined', (data) => {
    state.pending.draw = ''
    toast(`${(data && data.by) || '对手'} 拒绝和棋`, 'info')
  })
  ws.on('takeback_requested', (data) => {
    state.pending.takeback = (data && data.by) || ''
    toast(`${(data && data.name) || '对手'} 请求悔棋`, 'warn')
  })
  ws.on('takeback_applied', () => {
    state.pending.takeback = ''
    toast('悔棋已生效', 'info')
  })
  ws.on('takeback_declined', (data) => {
    state.pending.takeback = ''
    toast(`${(data && data.by) || '对手'} 拒绝悔棋`, 'info')
  })

  ws.on('opponent_left', (data) => {
    toast(`${(data && data.name) || '对手'} 已断线离开`, 'error', 8000)
  })

  // The server acknowledged that we gave up our seat. Dropping the snapshot is
  // what removes the "you are in a game" bar and returns the lobby to normal.
  ws.on('left', () => {
    clearRoom()
    state.chat = []
    toast('已离开房间', 'info')
  })

  ws.on('presence', (data) => {
    state.presence = {
      online: (data && data.online) || [],
      rooms: (data && data.rooms) || 0
    }
  })
}

/** connectRealtime ensures the socket is running (used after login and on boot). */
export function connectRealtime() {
  installRealtime()
  ws.connect()
}

// ---- lobby -----------------------------------------------------------------

/**
 * loadLobby refreshes GET /api/lobby into the shared state.
 *
 * The payload gained `playingRooms` (rooms whose status is "playing", which
 * anyone may watch). An older backend does not send that array, so every list is
 * normalised here: a missing or malformed field becomes an empty array instead of
 * breaking the lobby.
 */
export async function loadLobby() {
  const data = (await api.lobby()) || {}
  state.lobby = {
    ...data,
    openRooms: Array.isArray(data.openRooms) ? data.openRooms : [],
    playingRooms: Array.isArray(data.playingRooms) ? data.playingRooms : [],
    playing: data.playing ?? 0
  }
  return state.lobby
}

// ---- room helpers ----------------------------------------------------------

/** joinRoom asks the server to seat us: a specific room, a match, or the AI. */
export function joinRoom(payload = {}) {
  connectRealtime()
  const ok = protocol.join(payload)
  if (!ok) {
    toast('连接尚未就绪，正在重连…', 'warn')
  }
  return ok
}

/**
 * watchRoom attaches as a spectator. `watch` takes no seat, so the room snapshot
 * the server answers with carries an empty `youAre`; `leave` stops watching.
 */
export function watchRoom(roomId) {
  connectRealtime()
  const ok = protocol.watchRoom(roomId)
  if (!ok) {
    toast('连接尚未就绪，正在重连…', 'warn')
  }
  return ok
}

/** clearRoom drops the local snapshot (used when leaving a finished game). */
export function clearRoom() {
  state.room = null
  state.lastMove = null
  state.result = null
  state.pending = { draw: '', takeback: '' }
}

/** loadRules caches GET /api/rules for the start FEN and clock values. */
export async function loadRules() {
  if (state.rules) return state.rules
  try {
    state.rules = await api.rules()
  } catch {
    state.rules = null
  }
  return state.rules
}

/** startFen returns the initial position, falling back to the contract value. */
export function startFen() {
  return (state.rules && state.rules.startFen) || 'rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1'
}

export default state
