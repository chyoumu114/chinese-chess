// REST client for the xiangqi backend.
//
// Every response is JSON. Failures use the shape
//   { "error": { "code": "...", "message": "..." } }
// which this module unwraps into a thrown ApiError so callers can show the
// server's own Chinese message.

const TOKEN_KEY = 'xiangqi.token'

/** Error carrying the backend's machine-readable code. */
export class ApiError extends Error {
  constructor(code, message, status) {
    super(message || '请求失败')
    this.name = 'ApiError'
    this.code = code || 'unknown'
    this.status = status || 0
  }
}

export function getToken() {
  try {
    return localStorage.getItem(TOKEN_KEY) || ''
  } catch {
    return ''
  }
}

export function setToken(token) {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token)
    else localStorage.removeItem(TOKEN_KEY)
  } catch {
    /* private mode: keep the token in memory only */
  }
}

export function clearToken() {
  setToken('')
}

/**
 * request performs one JSON call.
 * @param {string} method HTTP verb
 * @param {string} path path beginning with /api
 * @param {{query?: object, body?: object, auth?: boolean}} [opts]
 */
async function request(method, path, opts = {}) {
  const { query, body, auth = false } = opts
  let url = path
  if (query) {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(query)) {
      if (v === undefined || v === null || v === '') continue
      qs.set(k, String(v))
    }
    const s = qs.toString()
    if (s) url += (url.includes('?') ? '&' : '?') + s
  }

  const headers = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const token = getToken()
  if (token) headers.Authorization = `Bearer ${token}`

  let res
  try {
    res = await fetch(url, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body)
    })
  } catch (err) {
    throw new ApiError('network', '无法连接服务器，请确认后端已启动', 0)
  }

  const text = await res.text()
  let payload = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }

  if (!res.ok) {
    const e = payload && payload.error
    throw new ApiError(
      (e && e.code) || 'http_' + res.status,
      (e && e.message) || `请求失败（${res.status}）`,
      res.status
    )
  }

  // A 401 on an authenticated call means the stored token is stale.
  if (auth && res.status === 401) {
    clearToken()
  }
  return payload
}

export const api = {
  /** GET /api/health */
  health: () => request('GET', '/api/health'),

  /** POST /api/auth/register -> { token, expiresAt, user } */
  register: (username, password, nickname) =>
    request('POST', '/api/auth/register', { body: { username, password, nickname } }),

  /** POST /api/auth/login -> { token, expiresAt, user } */
  login: (username, password) =>
    request('POST', '/api/auth/login', { body: { username, password } }),

  /** GET /api/auth/me -> { user, stats } */
  me: () => request('GET', '/api/auth/me', { auth: true }),

  /** GET /api/leaderboard?limit= -> { users } */
  leaderboard: (limit = 50) => request('GET', '/api/leaderboard', { query: { limit } }),

  /** GET /api/lobby -> { openRooms, playing } */
  lobby: () => request('GET', '/api/lobby'),

  /** GET /api/users/{id} -> { user, stats, games } */
  user: (id) => request('GET', `/api/users/${encodeURIComponent(id)}`),

  /**
   * GET /api/games
   * @param {{limit?: number, offset?: number, mine?: boolean, userId?: number, status?: string}} [filters]
   */
  games: (filters = {}) => {
    const { limit = 20, offset = 0, mine = false, userId, status } = filters
    return request('GET', '/api/games', {
      query: { limit, offset, mine: mine ? 1 : undefined, userId, status }
    })
  },

  /** GET /api/games/{id} -> { game } */
  game: (id) => request('GET', `/api/games/${encodeURIComponent(id)}`),

  /** GET /api/games/{id}/moves -> { game, moves } */
  gameMoves: (id) => request('GET', `/api/games/${encodeURIComponent(id)}/moves`),

  /** GET /api/rules -> board geometry, piece table, clocks */
  rules: () => request('GET', '/api/rules'),

  /** GET /api/ai/move?fen=&level=&budgetMs= */
  aiMove: ({ fen, level = 2, budgetMs = 800 } = {}) =>
    request('GET', '/api/ai/move', { query: { fen, level, budgetMs } })
}

export default api
