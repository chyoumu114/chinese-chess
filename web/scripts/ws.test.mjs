// Unit tests for the WebSocket client's identity handling (web/src/ws.js).
//
// The server reads the caller's identity from `?token=` **when the socket is
// opened** and never changes it afterwards. So logging in or out has to tear the
// connection down and re-handshake, otherwise the client keeps talking as the
// previous identity — the bug this pins down: sign in from the lobby, click
// 「人机对战」, and the server answers login_required until you refresh.
//
// Run with:  npm test

import test from 'node:test'
import assert from 'node:assert/strict'

// ---- minimal browser environment ------------------------------------------

class FakeSocket {
  // 与浏览器 WebSocket 的静态常量保持一致：ws.js 会用 WebSocket.OPEN 与 readyState 比较，
  // 少了这几个常量，那个「已连接就不重复建连」的判断会静默失效。
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static opened = []

  constructor(url) {
    this.url = url
    this.readyState = 1 // OPEN
    this.closed = false
    FakeSocket.opened.push(url)
  }

  close() {
    this.closed = true
    this.readyState = 3 // CLOSED
  }

  send() {
    /* frames are irrelevant here */
  }
}

globalThis.location = { protocol: 'http:', host: 'xiangqi.example.com' }
globalThis.WebSocket = FakeSocket
const tokens = new Map()
globalThis.localStorage = {
  getItem: (key) => (tokens.has(key) ? tokens.get(key) : null),
  setItem: (key, value) => tokens.set(key, String(value)),
  removeItem: (key) => tokens.delete(key)
}

const { WsClient } = await import('../src/ws.js')
const { setToken, clearToken } = await import('../src/api.js')

test('登录后 reconnect 会用新 token 重新握手', () => {
  FakeSocket.opened.length = 0
  const client = new WsClient()

  setToken('guest-token')
  client.connect()
  assert.equal(FakeSocket.opened.length, 1)
  assert.match(FakeSocket.opened[0], /token=guest-token/)

  // 用户刚登录：token 变了，但已建立的连接身份不会自己更新。
  setToken('user-token')
  client.reconnect()

  assert.equal(FakeSocket.opened.length, 2, 'reconnect 必须重新握手')
  assert.match(FakeSocket.opened[1], /token=user-token/, '新连接要带上新 token')
  assert.equal(client.socket.closed, false, '新连接应当是活的')
})

test('登出后重连不带 token（回到游客身份）', () => {
  FakeSocket.opened.length = 0
  const client = new WsClient()

  setToken('user-token')
  client.connect()
  assert.match(FakeSocket.opened[0], /token=user-token/)

  clearToken()
  client.reconnect()

  assert.equal(FakeSocket.opened.length, 2)
  assert.doesNotMatch(FakeSocket.opened[1], /token=/, '游客连接不应带 token')
})

test('连接已打开时 connect 不会重复建连', () => {
  FakeSocket.opened.length = 0
  const client = new WsClient()

  setToken('user-token')
  client.connect()
  client.connect()

  assert.equal(FakeSocket.opened.length, 1, '幂等：已连接就不该再开一条')
})
