// Board coordinates: FEN coordinates <-> screen coordinates.
//
// The protocol is always FEN coordinates (docs/frontend-contract.md §一):
// `fen`, `legalFrom`, `from`/`to` and `checkSquare` all name squares as
// <file letter><FEN rank>, with rank 0 the black back rank.
//
// Which side is drawn at the bottom of the screen is a pure presentation
// choice. These helpers are the ONLY place where the two systems are mapped, so
// a perspective change cannot leak into the move protocol: the renderer maps FEN
// -> screen, and every click maps screen -> FEN before it is emitted.

export const ROWS = 10
export const COLS = 9

/** FEN file letters, left to right. */
export const FILES = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i']

/**
 * parseSquare turns "e9" into { file: 4, row: 9 }, or null when malformed.
 */
export function parseSquare(square) {
  if (!square || typeof square !== 'string' || square.length < 2) return null
  const file = FILES.indexOf(square.charAt(0).toLowerCase())
  const row = Number(square.slice(1))
  if (file < 0 || !Number.isInteger(row) || row < 0 || row >= ROWS) return null
  return { file, row }
}

/** squareName is the inverse of parseSquare: (4, 9) -> "e9". */
export function squareName(screenFile, fenRow) {
  return `${FILES[screenFile]}${fenRow}`
}

/**
 * normOrientation resolves what `orientation` means for a viewer.
 *
 *   "black" -> black at the bottom        "red" -> red at the bottom
 *   "auto"  -> whatever side the viewer plays, red while not seated
 *   anything else falls back to "red", the standard view.
 */
export function normOrientation(orientation, youAre) {
  if (orientation === 'auto') return youAre === 'black' ? 'black' : 'red'
  return orientation === 'black' ? 'black' : 'red'
}

/** isFlipped reports whether the board is drawn from black's point of view. */
export function isFlipped(orientation, youAre) {
  return normOrientation(orientation, youAre) === 'black'
}

/**
 * viewRow maps a FEN rank onto the screen row that displays it.
 * Unflipped (red at the bottom) rank 0 is the top row; flipped (black at the
 * bottom) rank 0 is the bottom row.
 */
export function viewRow(fenRow, flipped) {
  return flipped ? ROWS - 1 - fenRow : fenRow
}

/** screenRowToFenRow is the inverse of viewRow, used when a click arrives. */
export function screenRowToFenRow(screenRow, flipped) {
  return flipped ? ROWS - 1 - screenRow : screenRow
}

/**
 * squareAtScreen returns the FEN square name drawn at a screen cell, which is
 * exactly what a click on that cell must send to the server.
 */
export function squareAtScreen(screenFile, screenRow, flipped) {
  return squareName(screenFile, screenRowToFenRow(screenRow, flipped))
}
