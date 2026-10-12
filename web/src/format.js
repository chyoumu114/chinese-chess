// Presentation helpers: Chinese labels for the backend's enum values, clock
// formatting and small board utilities shared by the views.

export const SIDE_LABEL = { red: '红方', black: '黑方' }

const OUTCOME_LABEL = {
  ongoing: '进行中',
  red_win: '红方胜',
  black_win: '黑方胜',
  draw: '和棋'
}

const REASON_LABEL = {
  checkmate: '将死',
  stalemate: '困毙',
  king_capture: '擒王',
  resign: '认输',
  timeout: '超时',
  draw_agreed: '协议和棋',
  sixty_move: '六十回合无吃子',
  abandoned: '掉线判负'
}

const STATUS_LABEL = {
  waiting: '等待对手',
  playing: '对局中',
  finished: '已结束'
}

const PRESENCE_LABEL = {
  online: '空闲',
  waiting: '等待中',
  playing: '对局中'
}

export function sideLabel(side) {
  return SIDE_LABEL[side] || '未知'
}

export function outcomeLabel(outcome) {
  return OUTCOME_LABEL[outcome] || '未知结果'
}

export function reasonLabel(reason) {
  return REASON_LABEL[reason] || reason || ''
}

export function statusLabel(status) {
  return STATUS_LABEL[status] || status || ''
}

export function presenceLabel(status) {
  return PRESENCE_LABEL[status] || status || ''
}

/**
 * resultText renders the banner text, e.g. "红方胜 · 将死".
 */
export function resultText(outcome, reason) {
  const head = outcomeLabel(outcome)
  const tail = reasonLabel(reason)
  return tail ? `${head} · ${tail}` : head
}

/**
 * fmtClock renders remaining milliseconds as mm:ss.
 *
 * Zero means "no clock": the server sends msLeft 0 when a seat has no time
 * limit (see XIANGQI_TURN_TIMEOUT / XIANGQI_GAME_TIMEOUT), and showing "00:00"
 * there would read as a losing position, so it is labelled 不限 instead.
 */
export function fmtClock(ms) {
  if (ms === null || ms === undefined || Number.isNaN(Number(ms))) return '--:--'
  const value = Math.max(0, Math.floor(Number(ms)))
  if (value === 0) return '不限'
  const total = Math.floor(value / 1000)
  const m = Math.floor(total / 60)
  const s = total % 60
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
}

/** fmtDateTime renders an ISO timestamp in the visitor's locale. */
export function fmtDateTime(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export const COLS = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i']

/** squareName converts board coordinates to the protocol's square name. */
export function squareName(col, fenRow) {
  return `${COLS[col]}${fenRow}`
}

/** splitSquare converts "e9" into { col: 4, row: 9 }. */
export function splitSquare(square) {
  if (!square || square.length < 2) return null
  const col = COLS.indexOf(String(square).charAt(0).toLowerCase())
  const row = Number(String(square).slice(1))
  if (col < 0 || Number.isNaN(row) || row < 0 || row > 9) return null
  return { col, row }
}

/**
 * moveSquares returns { from, to } for a played move.
 *
 * A room snapshot's MoveView carries `from`/`to`, while the stored Move returned
 * by GET /api/games/{id}/moves only carries `uci` (for example "h7e7").
 */
export function moveSquares(move) {
  if (!move) return null
  if (move.from && move.to) return { from: move.from, to: move.to }
  const uci = String(move.uci || '')
  if (uci.length < 4) return null
  return { from: uci.slice(0, 2), to: uci.slice(2, 4) }
}

/** rgb returns the accent colour of a side. */
export function sideColor(side) {
  return side === 'red' ? '#c0392b' : '#1f2933'
}

/**
 * encodeFenMove is a small helper for the read-only replay: given the FEN of a
 * position it locates a piece. It never judges legality — it only reads glyphs
 * for the check indicator.
 */
export function findKingSquare(fen, side) {
  if (!fen) return ''
  const placement = fen.trim().split(/\s+/)[0] || ''
  const rows = placement.split('/')
  const target = side === 'red' ? 'K' : 'k'
  for (let r = 0; r < rows.length && r < 10; r++) {
    let col = 0
    for (const ch of rows[r]) {
      if (ch >= '1' && ch <= '9') {
        col += Number(ch)
        continue
      }
      if (ch === target) return squareName(col, r)
      col += 1
    }
  }
  return ''
}

/** myResult maps a finished game to the signed-in user's outcome. */
export function myResult(game, userId) {
  if (!game || !userId) return ''
  if (game.outcome === 'draw') return '和'
  const mineIsRed = game.redUserId === userId
  const mineIsBlack = game.blackUserId === userId
  if (!mineIsRed && !mineIsBlack) return ''
  if (game.outcome === 'red_win') return mineIsRed ? '胜' : '负'
  if (game.outcome === 'black_win') return mineIsBlack ? '胜' : '负'
  return ''
}

/** ratingFor extracts a rating when the API provided one. */
export function ratingFor(player) {
  if (!player) return null
  return player.rating ?? null
}

export const AI_LEVELS = [
  { value: 1, label: '简单', key: 'easy' },
  { value: 2, label: '普通', key: 'normal' },
  { value: 3, label: '困难', key: 'hard' }
]
