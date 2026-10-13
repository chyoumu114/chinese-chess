// WebSocket client for the xiangqi real-time protocol.
//
// Frames are JSON text with a uniform envelope: { "type": "...", "data": {...} }.
// The connection is re-established every 2 seconds after an unexpected close,
// and a `sync` frame is sent as soon as a new socket opens so the client
// immediately regains the authoritative room snapshot.

import { getToken } from './api.js'

const RECONNECT_MS = 2000
const PING_MS = 25000

export class WsClient {
  constructor() {
    this.socket = null
    this.status = 'idle' // idle | connecting | open | closed
    this.reconnectTimer = null
    this.pingTimer = null
    /** @type {Map<string, Set<Function>>} */
    this.handlers = new Map()
    /** @type {Set<Function>} */
    this.anyHandlers = new Set()
    this.manualClose = false
    this.attempts = 0
  }

  // ---- subscription -------------------------------------------------------

  /**
   * on subscribes to one message type (or '*' for every frame).
   * @returns {Function} unsubscribe
   */
  on(type, handler) {
    if (type === '*') {
      this.anyHandlers.add(handler)
      return () => this.anyHandlers.delete(handler)
    }
    if (!this.handlers.has(type)) this.handlers.set(type, new Set())
    this.handlers.get(type).add(handler)
    return () => {
      const set = this.handlers.get(type)
      if (set) set.delete(handler)
    }
  }

  /** onStatus subscribes to connection status changes. */
  onStatus(handler) {
    if (!this.handlers.has('__status')) this.handlers.set('__status', new Set())
    this.handlers.get('__status').add(handler)
    return () => this.handlers.get('__status').delete(handler)
  }

  #emit(type, data) {
    const set = this.handlers.get(type)
    if (set) {
      for (const fn of [...set]) {
        try {
          fn(data)
        } catch (err) {
          console.error('[ws] handler failed for', type, err)
        }
      }
    }
    for (const fn of [...this.anyHandlers]) {
      try {
        fn(type, data)
      } catch (err) {
        console.error('[ws] wildcard handler failed for', type, err)
      }
    }
  }

  #setStatus(status) {
    if (this.status === status) return
    this.status = status
    const set = this.handlers.get('__status')
    if (set) for (const fn of [...set]) fn(status)
  }

  // ---- lifecycle ----------------------------------------------------------

  /** connect opens the socket, reusing the stored JWT when present. */
  connect() {
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      return
    }
    this.manualClose = false
    this.#clearReconnect()

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const token = getToken()
    const url = `${proto}//${location.host}/ws` + (token ? `?token=${encodeURIComponent(token)}` : '')

    this.#setStatus('connecting')
    let socket
    try {
      socket = new WebSocket(url)
    } catch (err) {
      console.error('[ws] cannot open socket', err)
      this.#setStatus('closed')
      this.#scheduleReconnect()
      return
    }
    this.socket = socket

    socket.onopen = () => {
      this.attempts = 0
      this.#setStatus('open')
      // Re-sync unconditionally: the room snapshot may have moved on while the
      // socket was down, and the server also uses it to re-attach the seat.
      this.send('sync')
      this.#startPing()
    }

    socket.onmessage = (event) => {
      let frame
      try {
        frame = JSON.parse(event.data)
      } catch {
        console.warn('[ws] ignoring non-JSON frame', event.data)
        return
      }
      if (!frame || typeof frame.type !== 'string') return
      this.#emit(frame.type, frame.data === undefined ? {} : frame.data)
    }

    socket.onerror = () => {
      // The close event that follows carries the reconnect decision.
    }

    socket.onclose = () => {
      this.#stopPing()
      if (this.socket === socket) this.socket = null
      this.#setStatus('closed')
      if (!this.manualClose) this.#scheduleReconnect()
    }
  }

  /** close shuts the socket down for good, e.g. on sign-out. */
  close() {
    this.manualClose = true
    this.#clearReconnect()
    this.#stopPing()
    if (this.socket) {
      try {
        this.socket.close()
      } catch {
        /* already gone */
      }
      this.socket = null
    }
    this.#setStatus('idle')
  }

  /** send writes one envelope. Returns false when the socket is not open. */
  send(type, data) {
    const socket = this.socket
    if (!socket || socket.readyState !== WebSocket.OPEN) return false
    const frame = data === undefined ? { type } : { type, data }
    socket.send(JSON.stringify(frame))
    return true
  }

  #scheduleReconnect() {
    if (this.reconnectTimer) return
    this.attempts += 1
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.connect()
    }, RECONNECT_MS)
  }

  #clearReconnect() {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }

  #startPing() {
    this.#stopPing()
    this.pingTimer = setInterval(() => {
      // A dead socket never fires close on some networks; the heartbeat keeps
      // the server's read deadline fresh.
      this.send('ping')
    }, PING_MS)
  }

  #stopPing() {
    if (this.pingTimer) {
      clearInterval(this.pingTimer)
      this.pingTimer = null
    }
  }
}

export const ws = new WsClient()

// Convenience wrappers so views read like the protocol table.
export const protocol = {
  join: (payload = {}) => ws.send('join', payload),
  // ready accepts the next game. Two human players both have to send it before
  // the pieces move; after a finished game it starts a rematch in the same room
  // with the colours swapped.
  ready: () => ws.send('ready'),
  // leave gives up the current seat: it cancels a quick match, or forfeits a
  // running game. The server answers with a `left` frame.
  leave: () => ws.send('leave'),
  move: (from, to) => ws.send('move', { from, to }),
  resign: () => ws.send('resign'),
  offerDraw: () => ws.send('offer_draw'),
  respondDraw: (accept) => ws.send('respond_draw', { accept: !!accept }),
  requestTakeback: () => ws.send('request_takeback'),
  respondTakeback: (accept) => ws.send('respond_takeback', { accept: !!accept }),
  chat: (text) => ws.send('chat', { text }),
  sync: () => ws.send('sync')
}

export default ws
