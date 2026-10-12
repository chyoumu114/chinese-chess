<script setup>
// Move list: orthodox Chinese notation in two columns, one row per full move.
// The notation strings come from the server; this component only lays them out.

import { computed } from 'vue'

const props = defineProps({
  /** MoveView[]: { ply, side, from, to, notation, check, msSpent }. */
  moves: { type: Array, default: () => [] },
  /** Ply currently shown on a replay board (0 = initial position). */
  activePly: { type: Number, default: 0 },
  /** Whether plies are clickable (replay). */
  selectable: { type: Boolean, default: false }
})

const emit = defineEmits(['select'])

const rows = computed(() => {
  const out = []
  for (const mv of props.moves || []) {
    // ply 1 is Red's first move; a full move is two plies.
    const number = Math.max(1, Math.ceil(mv.ply / 2))
    while (out.length < number) out.push({ number: out.length + 1, red: null, black: null })
    const row = out[number - 1]
    if (mv.side === 'red') row.red = mv
    else if (mv.side === 'black') row.black = mv
    else if (mv.ply % 2 === 1) row.red = mv
    else row.black = mv
  }
  return out
})

const plyCount = computed(() => (props.moves || []).length)

function cellClass(mv) {
  if (!mv) return 'cell empty'
  return `cell ${mv.side}${mv.ply === props.activePly ? ' active' : ''}${props.selectable ? ' clickable' : ''}`
}

function pick(mv) {
  if (!props.selectable || !mv) return
  emit('select', mv.ply)
}
</script>

<template>
  <div class="move-list">
    <div class="move-list-head">
      <span>棋谱</span>
      <span class="muted">{{ plyCount }} 着</span>
    </div>

    <div v-if="!rows.length" class="empty-hint">尚无着法</div>

    <div v-else class="move-table">
      <div class="move-row header">
        <span class="no">#</span>
        <span class="cell red">红方</span>
        <span class="cell black">黑方</span>
      </div>
      <div v-for="row in rows" :key="row.number" class="move-row">
        <span class="no">{{ row.number }}</span>
        <span :class="cellClass(row.red)" @click="pick(row.red)">
          <template v-if="row.red">
            {{ row.red.notation }}
            <em v-if="row.red.check" class="check-tag">将</em>
          </template>
        </span>
        <span :class="cellClass(row.black)" @click="pick(row.black)">
          <template v-if="row.black">
            {{ row.black.notation }}
            <em v-if="row.black.check" class="check-tag">将</em>
          </template>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.move-list {
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.move-list-head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  font-weight: 600;
  margin-bottom: 6px;
}

.move-table {
  overflow-y: auto;
  max-height: 340px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: #fffdf8;
}

.move-row {
  display: grid;
  grid-template-columns: 2.4rem 1fr 1fr;
  border-bottom: 1px solid var(--border-soft);
  font-variant-numeric: tabular-nums;
}

.move-row:last-child {
  border-bottom: none;
}

.move-row.header {
  position: sticky;
  top: 0;
  background: #f6efe2;
  font-size: 0.82rem;
  color: var(--text-muted);
  font-weight: 600;
  z-index: 1;
}

.no {
  padding: 5px 6px;
  color: var(--text-muted);
  font-size: 0.82rem;
  text-align: right;
  border-right: 1px solid var(--border-soft);
}

.cell {
  padding: 5px 10px;
  font-family: 'KaiTi', 'STKaiti', 'Songti SC', 'SimSun', serif;
  font-size: 1.02rem;
  min-height: 1.7rem;
}

.cell.black {
  border-left: 1px solid var(--border-soft);
}

.cell.clickable {
  cursor: pointer;
}

.cell.clickable:hover {
  background: #f3ead7;
}

.cell.active {
  background: #fbe6ac;
  font-weight: 700;
}

.cell.red {
  color: #b3261e;
}

.cell.black {
  color: #1f2933;
}

.check-tag {
  font-style: normal;
  font-size: 0.7rem;
  color: #fff;
  background: #e53935;
  border-radius: 3px;
  padding: 0 3px;
  margin-left: 3px;
  vertical-align: super;
}

.empty-hint {
  color: var(--text-muted);
  font-size: 0.9rem;
  padding: 10px 2px;
}
</style>
