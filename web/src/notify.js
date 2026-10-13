// 落子提示：走子音效 + 页面不在前台时的标签页闪动。
//
// 音效刻意不用音频文件：一声「啪」用 Web Audio 合成即可，省掉一个静态资源、
// 一次网络请求，也不会出现「文件 404 就永远静音」这种哑剧。浏览器要求页面有过
// 交互才允许出声，所以 AudioContext 在第一次真正要发声时才创建，并尽量 resume。

const MUTE_KEY = 'xiangqi.sound.muted'

let audioCtx = null

/** 是否静音（预留给设置开关，目前只读 localStorage）。 */
function muted() {
  try {
    return window.localStorage.getItem(MUTE_KEY) === '1'
  } catch (err) {
    return false
  }
}

function context() {
  const Ctor = window.AudioContext || window.webkitAudioContext
  if (!Ctor) return null
  if (!audioCtx) audioCtx = new Ctor()
  if (audioCtx.state === 'suspended') {
    audioCtx.resume().catch(() => {})
  }
  return audioCtx
}

/**
 * playMoveSound 播放一声落子：三角波快速下滑加极短包络，听感接近棋子敲在木盘上。
 */
export function playMoveSound() {
  if (muted()) return
  const ac = context()
  if (!ac) return
  const at = ac.currentTime
  const osc = ac.createOscillator()
  const gain = ac.createGain()
  osc.type = 'triangle'
  osc.frequency.setValueAtTime(360, at)
  osc.frequency.exponentialRampToValueAtTime(110, at + 0.09)
  gain.gain.setValueAtTime(0.0001, at)
  gain.gain.exponentialRampToValueAtTime(0.32, at + 0.006)
  gain.gain.exponentialRampToValueAtTime(0.0001, at + 0.19)
  osc.connect(gain)
  gain.connect(ac.destination)
  osc.start(at)
  osc.stop(at + 0.22)
}

// ---- 后台标签页闪动 -------------------------------------------------------

// 内联的红色图标：页面在后台提醒时，标签页图标换成这个红点。
const RED_ICON =
  'data:image/svg+xml,' +
  encodeURIComponent(
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">' +
      '<circle cx="16" cy="16" r="15" fill="#d92b2b"/>' +
      '<circle cx="16" cy="16" r="7" fill="#fff"/></svg>'
  )

let timer = null
let flashing = false
let baseTitle = ''
let baseIcon = ''
let alertText = ''

function iconLink() {
  let link = document.querySelector('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.setAttribute('rel', 'icon')
    document.head.appendChild(link)
  }
  return link
}

/**
 * flashTab 在页面不在前台时把标签页闪红：标题与红点图标交替出现。重复调用只更新
 * 提示文案，不会叠加定时器。页面回到前台会自动停下。
 */
export function flashTab(text) {
  if (!document.hidden) return
  alertText = text || '棋盘有变化'
  if (flashing) return
  flashing = true
  baseTitle = document.title
  baseIcon = iconLink().getAttribute('href') || ''
  let on = false
  timer = window.setInterval(() => {
    on = !on
    document.title = on ? `【${alertText}】` : baseTitle
    iconLink().setAttribute('href', on ? RED_ICON : baseIcon)
  }, 700)
}

/** stopTabAlert 撤掉闪动，恢复原来的标题与图标。 */
export function stopTabAlert() {
  if (!flashing) return
  flashing = false
  if (timer) {
    window.clearInterval(timer)
    timer = null
  }
  document.title = baseTitle
  iconLink().setAttribute('href', baseIcon)
}

// 回到前台就不再闪：人已经看到棋盘了。
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) stopTabAlert()
})
