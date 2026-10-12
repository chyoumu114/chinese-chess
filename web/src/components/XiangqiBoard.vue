<script setup>
// SVG xiangqi board.
//
// ORIENTATION (per docs/frontend-contract.md §一):
//   FEN rank 0 is the black back rank; FEN rank 9 is the red back rank. A square
//   name is <col letter><fen rank>, so e9 (red king) and e0 (black king) are the
//   two palace centres. Red advances towards smaller rank numbers.
//
//   The board below is its own view of the position, but the SERVER stays in FEN
//   coordinates: `legalFrom`, `lastMove` and `checkSquare` all arrive as square
//   names. Only the two mapping helpers `viewCol()` / `viewRow()` translate FEN
//   coordinates into screen coordinates, and every click is translated back with
//   `squareAt()` before it is emitted. Nothing else in this file is allowed to
//   assume a direction, so a perspective change can never corrupt a move.
//
//   `orientation` picks which side sits at the BOTTOM of the screen:
//     "red"   (default) — ranks 0..9 top-to-bottom: red plays from the bottom.
//     "black"           — ranks 9..0 top-to-bottom: black plays from the bottom,
//                         i.e. the board is turned around for the black player.
//     "auto"            — follow the side this client plays (see GameView).
//
// This component NEVER decides legality: `legalFrom`, supplied by the server in
// the room snapshot, is the only source of legal destinations.

import { computed, ref, watch } from 'vue'
import { FILES, isFlipped, squareAtScreen, viewRow as toViewRow } from '../board.js'

const props = defineProps({
  /** Full position FEN, e.g. "rnbakabnr/9/... w - - 0 1". */
  fen: { type: String, default: '' },
  /** Server-provided map: origin square -> array of legal destination squares. */
  legalFrom: { type: Object, default: () => ({}) },
  /** Side this client plays: "red" | "black" | "". */
  youAre: { type: String, default: '' },
  /** Side to move: "red" | "black". */
  turn: { type: String, default: '' },
  /** Whether clicks may produce moves. */
  interactive: { type: Boolean, default: false },
  /** Optional { from, to } of the previous move, highlighted. */
  lastMove: { type: Object, default: null },
  /** Optional square of a king in check, highlighted. */
  checkSquare: { type: String, default: '' },
  /**
   * Which side is drawn at the bottom of the screen: "red" (default) keeps the
   * standard FEN orientation, "black" turns the board around, and "auto" follows
   * the side this client plays (falling back to "red" while unseated).
   */
  orientation: { type: String, default: 'red' }
})

const emit = defineEmits(['move'])

// File letters a..i, and the FEN letter -> Chinese glyph table. Red uses the
// uppercase letters.
const COLS = FILES
const GLYPH = {
  K: '帅', A: '仕', B: '相', N: '马', R: '车', C: '炮', P: '兵',
  k: '将', a: '士', b: '象', n: '马', r: '车', c: '炮', p: '卒'
}

// ---- geometry --------------------------------------------------------------

const CELL = 60
const PAD = 34
const BOARD_W = 8 * CELL
const BOARD_H = 9 * CELL
const W = BOARD_W + PAD * 2
const H = BOARD_H + PAD * 2

/** x of a column, 0 = file "a". */
const x = (col) => PAD + col * CELL
/** y of a screen row, 0 = the top of the board, 9 = the bottom. */
const y = (row) => PAD + row * CELL

// ---- perspective -----------------------------------------------------------
//
// `flipped` is the single decision that turns the board around; `viewRow()` and
// `squareAt()` (both from ../board.js, where they are unit tested) are the only
// places allowed to apply it.

/** True when the board is drawn from the point of view of the black side. */
const flipped = computed(() => isFlipped(props.orientation, props.youAre))

/** viewCol maps a FEN file onto the screen column showing it. */
const viewCol = (fenCol) => fenCol
/** viewRow maps a FEN rank onto the screen row showing it. */
const viewRow = (fenRow) => toViewRow(fenRow, flipped.value)

/** squareAt is the inverse of viewRow(): the square drawn at a screen cell. */
function squareAt(screenCol, screenRow) {
  return squareAtScreen(screenCol, screenRow, flipped.value)
}

// ---- FEN parsing -----------------------------------------------------------

/**
 * pieces parses the placement field of the FEN into drawable pieces.
 * The index of a rank in the FEN string is its FEN row (contract §一); the
 * screen position comes from viewCol() / viewRow() only.
 */
const pieces = computed(() => {
  const placement = (props.fen || '').trim().split(/\s+/)[0] || ''
  const ranks = placement.split('/')
  const out = []
  for (let fenRow = 0; fenRow < ranks.length && fenRow < 10; fenRow++) {
    let col = 0
    for (const ch of ranks[fenRow]) {
      if (ch >= '1' && ch <= '9') {
        col += Number(ch)
        continue
      }
      if (col > 8) break
      const upper = ch.toUpperCase()
      if (!GLYPH[ch]) {
        col += 1
        continue
      }
      out.push({
        square: `${COLS[col]}${fenRow}`,
        cx: x(viewCol(col)),
        cy: y(viewRow(fenRow)),
        side: ch === upper ? 'red' : 'black',
        glyph: GLYPH[ch],
        key: `${COLS[col]}${fenRow}`
      })
      col += 1
    }
  }
  return out
})

const occupancy = computed(() => {
  const map = new Map()
  for (const p of pieces.value) map.set(p.square, p)
  return map
})

// ---- board furniture -------------------------------------------------------

const squares = computed(() => {
  const out = []
  for (let screenRow = 0; screenRow < 10; screenRow++) {
    for (let screenCol = 0; screenCol < 9; screenCol++) {
      out.push({
        square: squareAt(screenCol, screenRow),
        x: x(screenCol) - CELL / 2,
        y: y(screenRow) - CELL / 2
      })
    }
  }
  return out
})

/** Horizontal lines: one per screen row, edge to edge. */
const hLines = computed(() =>
  Array.from({ length: 10 }, (_, row) => ({ x1: x(0), y1: y(row), x2: x(8), y2: y(row) }))
)

/** Vertical lines: files b..h are interrupted by the river. */
const vLines = computed(() => {
  const out = []
  for (let col = 0; col < 9; col++) {
    if (col === 0 || col === 8) {
      out.push({ x1: x(col), y1: y(0), x2: x(col), y2: y(9) })
    } else {
      out.push({ x1: x(col), y1: y(0), x2: x(col), y2: y(4) })
      out.push({ x1: x(col), y1: y(5), x2: x(col), y2: y(9) })
    }
  }
  return out
})

/**
 * The two palace diagonals. The palace rectangles live on FEN ranks 0-2 (black)
 * and 7-9 (red), so their screen rows follow viewRow().
 */
const palaceLines = computed(() => {
  const blackTop = viewRow(0)
  const blackBottom = viewRow(2)
  const redTop = viewRow(7)
  const redBottom = viewRow(9)
  return [
    // black palace, FEN ranks 0-2, files d-f
    { x1: x(3), y1: y(blackTop), x2: x(5), y2: y(blackBottom) },
    { x1: x(5), y1: y(blackTop), x2: x(3), y2: y(blackBottom) },
    // red palace, FEN ranks 7-9, files d-f
    { x1: x(3), y1: y(redTop), x2: x(5), y2: y(redBottom) },
    { x1: x(5), y1: y(redTop), x2: x(3), y2: y(redBottom) }
  ]
})

/**
 * Small positioning crosses at the cannon and pawn starting points (FEN ranks
 * 2-3 and 6-7). A cross looks the same upside down, so only the row mapping is
 * needed here. Crosses on the outer files only draw their inner half.
 */
const crossLines = computed(() => {
  const points = [
    [1, 2], [7, 2], [1, 7], [7, 7],
    [0, 3], [2, 3], [4, 3], [6, 3], [8, 3],
    [0, 6], [2, 6], [4, 6], [6, 6], [8, 6]
  ]
  const gap = 6
  const len = 9
  const out = []
  for (const [col, fenRow] of points) {
    const cx = x(viewCol(col))
    const cy = y(viewRow(fenRow))
    for (const sx of [-1, 1]) {
      if (col === 0 && sx === -1) continue // nothing outside file a
      if (col === 8 && sx === 1) continue // nothing outside file i
      for (const sy of [-1, 1]) {
        out.push({
          x1: cx + sx * (gap + len), y1: cy + sy * gap,
          x2: cx + sx * gap, y2: cy + sy * gap
        })
        out.push({
          x1: cx + sx * gap, y1: cy + sy * (gap + len),
          x2: cx + sx * gap, y2: cy + sy * gap
        })
      }
    }
  }
  return out
})

const riverY = PAD + 4.5 * CELL
const riverLeftX = PAD + 2 * CELL
const riverRightX = PAD + 6 * CELL

const frame = {
  x: x(0) - 12,
  y: y(0) - 12,
  width: BOARD_W + 24,
  height: BOARD_H + 24
}

// ---- interaction -----------------------------------------------------------

const selected = ref('')
const destinations = computed(() => {
  if (!selected.value) return []
  const list = props.legalFrom ? props.legalFrom[selected.value] : null
  return Array.isArray(list) ? list : []
})
const destinationSet = computed(() => new Set(destinations.value))

/** Squares whose click currently does something, for the cursor hint. */
const actionable = computed(() => {
  const set = new Set()
  if (!props.interactive) return set
  if (selected.value) {
    set.add(selected.value)
    for (const d of destinations.value) set.add(d)
    return set
  }
  if (props.turn && props.youAre && props.turn !== props.youAre) return set
  for (const [from, tos] of Object.entries(props.legalFrom || {})) {
    if (Array.isArray(tos) && tos.length) set.add(from)
  }
  return set
})

// A new position invalidates the local selection.
watch(
  () => props.fen,
  () => {
    selected.value = ''
  }
)
watch(
  () => props.interactive,
  (on) => {
    if (!on) selected.value = ''
  }
)

function onSquareClick(square) {
  if (!props.interactive) return

  // 1. Completing a move: any highlighted destination, empty or occupied.
  if (selected.value && destinationSet.value.has(square)) {
    emit('move', { from: selected.value, to: square })
    selected.value = ''
    return
  }

  // 2. Clicking the selected piece again deselects it.
  if (selected.value === square) {
    selected.value = ''
    return
  }

  const piece = occupancy.value.get(square)
  if (!piece) {
    selected.value = ''
    return
  }

  // 3. Only the side to move, and only this client's own side, may be picked.
  if (props.youAre && piece.side !== props.youAre) {
    selected.value = ''
    return
  }
  if (props.turn && props.youAre && props.turn !== props.youAre) return

  // 4. The server decides whether the piece has any legal move at all.
  const options = props.legalFrom ? props.legalFrom[square] : null
  selected.value = Array.isArray(options) && options.length ? square : ''
}

/** points returns the centre of a square name, or null when malformed. */
function centreOf(square) {
  if (!square) return null
  const fenCol = COLS.indexOf(String(square).charAt(0).toLowerCase())
  const fenRow = Number(String(square).slice(1))
  if (fenCol < 0 || Number.isNaN(fenRow)) return null
  return { cx: x(viewCol(fenCol)), cy: y(viewRow(fenRow)) }
}

const lastFrom = computed(() => (props.lastMove ? centreOf(props.lastMove.from) : null))
const lastTo = computed(() => (props.lastMove ? centreOf(props.lastMove.to) : null))
const selectedCentre = computed(() => centreOf(selected.value))
const checkCentre = computed(() => centreOf(props.checkSquare))
const destMarkers = computed(() =>
  destinations.value.map((square) => {
    const centre = centreOf(square)
    return centre ? { square, occupied: occupancy.value.has(square), ...centre } : null
  }).filter(Boolean)
)

defineExpose({ clearSelection: () => (selected.value = '') })
</script>

<template>
  <div class="board-wrap" :class="{ 'board-interactive': interactive }">
    <svg
      class="xiangqi-board"
      :class="{ flipped }"
      :data-orientation="flipped ? 'black' : 'red'"
      :viewBox="`0 0 ${W} ${H}`"
      xmlns="http://www.w3.org/2000/svg"
      role="img"
      aria-label="中国象棋棋盘"
    >
      <defs>
        <linearGradient id="boardWood" x1="0" y1="0" x2="0.6" y2="1">
          <stop offset="0%" stop-color="#f3dcb6" />
          <stop offset="100%" stop-color="#e6c795" />
        </linearGradient>
        <radialGradient id="pieceFace" cx="35%" cy="30%" r="80%">
          <stop offset="0%" stop-color="#fffaf0" />
          <stop offset="100%" stop-color="#efdcbb" />
        </radialGradient>
      </defs>

      <rect class="board-bg" x="0" y="0" :width="W" :height="H" rx="12" />

      <!-- invisible click targets, one per intersection -->
      <g class="squares">
        <rect
          v-for="sq in squares"
          :key="sq.square"
          :x="sq.x"
          :y="sq.y"
          :width="CELL"
          :height="CELL"
          :class="{ 'sq-actionable': actionable.has(sq.square) }"
          @click="onSquareClick(sq.square)"
        />
      </g>

      <!-- board furniture -->
      <g class="grid" aria-hidden="true">
        <line v-for="(l, i) in hLines" :key="`h${i}`" v-bind="l" class="grid-line" />
        <line v-for="(l, i) in vLines" :key="`v${i}`" v-bind="l" class="grid-line" />
        <line v-for="(l, i) in palaceLines" :key="`p${i}`" v-bind="l" class="grid-line" />
        <line v-for="(l, i) in crossLines" :key="`c${i}`" v-bind="l" class="cross-line" />
      </g>

      <g class="river" aria-hidden="true">
        <text class="river-text" :x="riverLeftX" :y="riverY">楚河</text>
        <text class="river-text" :x="riverRightX" :y="riverY">汉界</text>
      </g>

      <rect
        class="board-frame"
        :x="frame.x"
        :y="frame.y"
        :width="frame.width"
        :height="frame.height"
        rx="4"
        aria-hidden="true"
      />

      <!-- previous move -->
      <g class="last-move" aria-hidden="true">
        <rect
          v-if="lastFrom"
          :x="lastFrom.cx - CELL * 0.45"
          :y="lastFrom.cy - CELL * 0.45"
          :width="CELL * 0.9"
          :height="CELL * 0.9"
          rx="6"
        />
        <rect
          v-if="lastTo"
          :x="lastTo.cx - CELL * 0.45"
          :y="lastTo.cy - CELL * 0.45"
          :width="CELL * 0.9"
          :height="CELL * 0.9"
          rx="6"
        />
      </g>

      <!-- pieces -->
      <g class="pieces">
        <g
          v-for="p in pieces"
          :key="p.key"
          class="piece"
          :class="[`piece-${p.side}`, { 'piece-selected': selected === p.square }]"
          aria-hidden="true"
        >
          <circle :cx="p.cx" :cy="p.cy" :r="CELL * 0.44" class="piece-disc" />
          <circle :cx="p.cx" :cy="p.cy" :r="CELL * 0.37" class="piece-inner" />
          <text
            class="piece-glyph"
            :x="p.cx"
            :y="p.cy"
            text-anchor="middle"
            dominant-baseline="central"
          >{{ p.glyph }}</text>
        </g>
      </g>

      <!-- selection ring -->
      <rect
        v-if="selectedCentre"
        class="selected-ring"
        :x="selectedCentre.cx - CELL * 0.46"
        :y="selectedCentre.cy - CELL * 0.46"
        :width="CELL * 0.92"
        :height="CELL * 0.92"
        rx="8"
        aria-hidden="true"
      />

      <!-- legal destinations, from the server's legalFrom only -->
      <g class="destinations" aria-hidden="true">
        <template v-for="d in destMarkers" :key="`d-${d.square}`">
          <circle v-if="!d.occupied" class="dest-dot" :cx="d.cx" :cy="d.cy" r="7" />
          <circle v-else class="dest-ring" :cx="d.cx" :cy="d.cy" :r="CELL * 0.47" />
        </template>
      </g>

      <!-- king in check -->
      <circle
        v-if="checkCentre"
        class="check-ring"
        :cx="checkCentre.cx"
        :cy="checkCentre.cy"
        :r="CELL * 0.5"
        aria-hidden="true"
      />
    </svg>
  </div>
</template>

<style scoped>
.board-wrap {
  width: 100%;
  max-width: 560px;
  margin: 0 auto;
}

.xiangqi-board {
  width: 100%;
  height: auto;
  display: block;
  border-radius: 12px;
  box-shadow: 0 6px 22px rgba(74, 49, 22, 0.22);
}

.board-bg {
  fill: url(#boardWood);
}

/* every decorative element is inert so clicks land on the square rects */
.grid,
.river,
.board-frame,
.last-move,
.pieces,
.destinations,
.selected-ring,
.check-ring {
  pointer-events: none;
}

.grid-line {
  stroke: #7a5a34;
  stroke-width: 1.3;
  stroke-linecap: square;
}

.cross-line {
  stroke: #7a5a34;
  stroke-width: 1.6;
  stroke-linecap: round;
}

.board-frame {
  fill: none;
  stroke: #7a5a34;
  stroke-width: 2.4;
}

.river-text {
  fill: #8a6435;
  font-size: 26px;
  font-family: 'KaiTi', 'STKaiti', 'Songti SC', 'SimSun', serif;
  letter-spacing: 10px;
  text-anchor: middle;
  dominant-baseline: central;
  opacity: 0.8;
}

.squares rect {
  fill: transparent;
}

.board-interactive .sq-actionable {
  cursor: pointer;
}

.last-move rect {
  fill: #d9a224;
  opacity: 0.32;
}

.piece-disc {
  fill: #f6e6c8;
  stroke-width: 2.4;
}

.piece-inner {
  fill: none;
  stroke-width: 1.2;
  opacity: 0.75;
}

.piece-red .piece-disc,
.piece-red .piece-inner {
  stroke: #b3261e;
}
.piece-red .piece-glyph {
  fill: #b3261e;
}

.piece-black .piece-disc,
.piece-black .piece-inner {
  stroke: #1f2933;
}
.piece-black .piece-glyph {
  fill: #1f2933;
}

.piece-disc {
  filter: drop-shadow(0 1.5px 2px rgba(60, 40, 20, 0.35));
}

.piece-glyph {
  font-size: 30px;
  font-weight: 700;
  font-family: 'KaiTi', 'STKaiti', 'Songti SC', 'SimSun', serif;
  user-select: none;
}

.piece-selected .piece-disc {
  fill: #fbe6ac;
}

.selected-ring {
  fill: none;
  stroke: #e0a020;
  stroke-width: 3.5;
  stroke-dasharray: 8 5;
}

.dest-dot {
  fill: #2e7d32;
  opacity: 0.72;
}

.dest-ring {
  fill: none;
  stroke: #2e7d32;
  stroke-width: 3.5;
  opacity: 0.85;
}

.check-ring {
  fill: none;
  stroke: #e53935;
  stroke-width: 4;
  animation: check-pulse 1s ease-in-out infinite;
}

@keyframes check-pulse {
  0%, 100% { opacity: 0.35; }
  50% { opacity: 1; }
}
</style>
