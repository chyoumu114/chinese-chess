<script setup>
// Toast host: renders the shared notification queue. Every server `error`
// frame lands here, so no failure is silent.

import { state, dismissToast } from '../store.js'
</script>

<template>
  <div class="toast-host" aria-live="polite" aria-atomic="false">
    <transition-group name="toast">
      <div
        v-for="t in state.toasts"
        :key="t.id"
        class="toast"
        :class="`toast-${t.kind}`"
        role="status"
        @click="dismissToast(t.id)"
      >
        <span class="toast-icon" aria-hidden="true">
          {{ t.kind === 'error' ? '⚠' : t.kind === 'warn' ? '!' : '✓' }}
        </span>
        <span class="toast-text">{{ t.text }}</span>
        <button class="toast-close" title="关闭" @click.stop="dismissToast(t.id)">×</button>
      </div>
    </transition-group>
  </div>
</template>

<style scoped>
.toast-host {
  position: fixed;
  top: 14px;
  right: 14px;
  z-index: 1000;
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-width: min(92vw, 380px);
  pointer-events: none;
}

.toast {
  pointer-events: auto;
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 10px 12px;
  border-radius: 10px;
  background: #ffffff;
  border: 1px solid var(--border);
  border-left-width: 4px;
  box-shadow: 0 6px 18px rgba(31, 41, 51, 0.16);
  font-size: 0.9rem;
  cursor: pointer;
}

.toast-error {
  border-left-color: #d64545;
}

.toast-warn {
  border-left-color: #d9a224;
}

.toast-info {
  border-left-color: #3b7dd8;
}

.toast-icon {
  font-weight: 700;
  line-height: 1.3;
}

.toast-error .toast-icon {
  color: #d64545;
}

.toast-warn .toast-icon {
  color: #b07d0a;
}

.toast-info .toast-icon {
  color: #3b7dd8;
}

.toast-text {
  flex: 1;
  word-break: break-word;
}

.toast-close {
  border: none;
  background: transparent;
  color: var(--text-muted);
  font-size: 1.1rem;
  line-height: 1;
  cursor: pointer;
  padding: 0 2px;
}

.toast-enter-active,
.toast-leave-active {
  transition: opacity 0.22s ease, transform 0.22s ease;
}

.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateX(20px);
}
</style>
