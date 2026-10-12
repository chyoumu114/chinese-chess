// Unit tests for the board perspective mapping (web/src/board.js).
//
// The move protocol is in FEN coordinates; only the renderer flips the board.
// These tests pin the two properties that make that safe:
//   1. a square is drawn where it is claimed to be drawn, and
//   2. clicking that cell sends back the very same square name.
//
// Run with:  npm test

import test from 'node:test'
import assert from 'node:assert/strict'

import {
  FILES,
  ROWS,
  parseSquare,
  squareName,
  normOrientation,
  isFlipped,
  viewRow,
  screenRowToFenRow,
  squareAtScreen
} from '../src/board.js'

test('parseSquare and squareName are inverse', () => {
  assert.deepEqual(parseSquare('e9'), { file: 4, row: 9 })
  assert.deepEqual(parseSquare('a0'), { file: 0, row: 0 })
  assert.deepEqual(parseSquare('I3'), { file: 8, row: 3 })
  assert.equal(parseSquare('j3'), null)
  assert.equal(parseSquare('e10'), null)
  assert.equal(parseSquare(''), null)
  assert.equal(squareName(4, 9), 'e9')
})

test('normOrientation follows the side the viewer plays', () => {
  assert.equal(normOrientation('auto', 'black'), 'black')
  assert.equal(normOrientation('auto', 'red'), 'red')
  // Not seated (spectator) keeps the standard view.
  assert.equal(normOrientation('auto', ''), 'red')
  assert.equal(normOrientation('black', 'red'), 'black')
  assert.equal(normOrientation('', ''), 'red')
  assert.equal(isFlipped('auto', 'black'), true)
  assert.equal(isFlipped('auto', 'red'), false)
})

test('viewRow keeps rank 0 on top for the standard view', () => {
  for (let fenRow = 0; fenRow < ROWS; fenRow++) {
    assert.equal(viewRow(fenRow, false), fenRow)
  }
  // Red (rank 9) sits at the bottom, black (rank 0) at the top.
  assert.equal(viewRow(9, false), 9)
  assert.equal(viewRow(0, false), 0)
})

test('viewRow puts black at the bottom when the board is turned around', () => {
  assert.equal(viewRow(0, true), 9, 'the black back rank goes to the bottom')
  assert.equal(viewRow(9, true), 0, 'the red back rank goes to the top')
  // The mapping is a permutation of 0..9: every rank keeps its own row.
  const seen = new Set()
  for (let fenRow = 0; fenRow < ROWS; fenRow++) seen.add(viewRow(fenRow, true))
  assert.equal(seen.size, ROWS)
})

test('screenRowToFenRow is the inverse of viewRow in both orientations', () => {
  for (const flipped of [false, true]) {
    for (let fenRow = 0; fenRow < ROWS; fenRow++) {
      assert.equal(screenRowToFenRow(viewRow(fenRow, flipped), flipped), fenRow)
    }
  }
})

test('a click on a drawn cell yields that very square', () => {
  for (const flipped of [false, true]) {
    for (const square of ['a0', 'e0', 'i0', 'a9', 'e9', 'i9', 'd4', 'f5']) {
      const { file, row } = parseSquare(square)
      assert.equal(squareAtScreen(file, viewRow(row, flipped), flipped), square)
    }
  }
})

test('the starting kings are drawn where each side expects them', () => {
  // Red king on e9, black king on e0 (docs/frontend-contract.md §一).
  assert.equal(squareAtScreen(4, viewRow(9, false), false), 'e9')
  assert.equal(squareAtScreen(4, viewRow(9, true), true), 'e9')
  // Red at the bottom: its king has the larger screen row. Flipped: smaller.
  assert.ok(viewRow(9, false) > viewRow(0, false))
  assert.ok(viewRow(9, true) < viewRow(0, true))
})

test('the file letters never mirror, so files stay a..i left to right', () => {
  for (let file = 0; file < FILES.length; file++) {
    const square = squareAtScreen(file, 4, true)
    assert.equal(square.charAt(0), FILES[file])
  }
})
