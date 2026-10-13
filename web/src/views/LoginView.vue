<script setup>
// Login and registration share one page behind a toggle.

import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '../api.js'
import { applySession, loadMe, connectRealtime, toast } from '../store.js'

const router = useRouter()
const route = useRoute()

const mode = ref('login') // 'login' | 'register'
const username = ref('')
const password = ref('')
const nickname = ref('')
const busy = ref(false)
const error = ref('')

const isRegister = computed(() => mode.value === 'register')
const title = computed(() => (isRegister.value ? '注册新账号' : '登录'))
const submitLabel = computed(() => (busy.value ? '请稍候…' : isRegister.value ? '注册并进入' : '登录'))

function switchMode(next) {
  mode.value = next
  error.value = ''
}

async function submit() {
  if (busy.value) return
  error.value = ''

  const name = username.value.trim()
  if (!name || !password.value) {
    error.value = '请填写用户名与密码'
    return
  }
  if (isRegister.value && password.value.length < 6) {
    error.value = '密码至少 6 位'
    return
  }

  busy.value = true
  try {
    const payload = isRegister.value
      ? await api.register(name, password.value, nickname.value.trim())
      : await api.login(name, password.value)

    applySession(payload)
    await loadMe()
    connectRealtime()
    toast(isRegister.value ? '注册成功，欢迎加入！' : `欢迎回来，${payload.user?.nickname || payload.user?.username || ''}`, 'info')
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/lobby'
    router.push(redirect)
  } catch (err) {
    error.value = err.message || '操作失败'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <div class="card login-card">
      <h1 class="login-title">{{ title }}</h1>
      <p class="muted login-sub">中国象棋在线对战 · 匹配真人或挑战电脑</p>

      <div class="tabs">
        <button
          class="tab"
          :class="{ active: !isRegister }"
          type="button"
          @click="switchMode('login')"
        >登录</button>
        <button
          class="tab"
          :class="{ active: isRegister }"
          type="button"
          @click="switchMode('register')"
        >注册</button>
      </div>

      <form @submit.prevent="submit">
        <label class="field">
          <span>用户名</span>
          <input
            v-model="username"
            type="text"
            autocomplete="username"
            placeholder="英文或数字，用于登录"
            maxlength="32"
          />
        </label>

        <label class="field">
          <span>密码</span>
          <input
            v-model="password"
            type="password"
            :autocomplete="isRegister ? 'new-password' : 'current-password'"
            placeholder="至少 6 位"
          />
        </label>

        <label v-if="isRegister" class="field">
          <span>昵称</span>
          <input v-model="nickname" type="text" placeholder="显示在棋盘旁的名字（可留空）" maxlength="24" />
        </label>

        <p v-if="error" class="form-error">{{ error }}</p>

        <button class="btn btn-primary submit" type="submit" :disabled="busy">
          {{ submitLabel }}
        </button>
      </form>

      <p class="muted hint">
        未登录也可以浏览大厅与排行榜，但需要登录才能入座对局。
      </p>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  display: flex;
  justify-content: center;
  padding: 24px 0;
}

.login-card {
  width: 100%;
  max-width: 420px;
}

.login-title {
  margin-bottom: 4px;
}

.login-sub {
  margin-top: 0;
  margin-bottom: 16px;
}

.tabs {
  display: flex;
  gap: 6px;
  background: #f6efe2;
  padding: 4px;
  border-radius: 10px;
  margin-bottom: 16px;
}

.tab {
  flex: 1;
  border: none;
  background: transparent;
  padding: 7px 0;
  border-radius: 7px;
  font-size: 0.94rem;
  font-family: inherit;
  color: var(--text-muted);
  cursor: pointer;
}

.tab.active {
  background: #fffdf8;
  color: var(--text);
  font-weight: 600;
  box-shadow: 0 1px 3px rgba(31, 41, 51, 0.12);
}

.form-error {
  color: #b3261e;
  font-size: 0.88rem;
  margin: 0 0 10px;
}

.submit {
  width: 100%;
  padding: 10px;
  font-size: 1rem;
}

.hint {
  margin: 14px 0 0;
  text-align: center;
}

/* ===== 手机端适配（表单自适应 + 点按区域 ≥40px）=====
   输入框宽度本就 100%，这里只压缩外边距、放大字号与高度，
   避免 iOS 聚焦缩放并保证手指可点。桌面端不受影响。 */
@media (max-width: 640px) {
  .login-page {
    padding: 12px 0 4px;
  }

  .login-card {
    max-width: 100%;
    padding: 14px 14px 16px;
  }

  .login-title {
    font-size: 1.3rem;
  }

  .login-sub {
    font-size: 0.82rem;
    margin-bottom: 14px;
  }

  /* 分段切换按钮：高度 ≥44px */
  .tabs {
    gap: 4px;
    margin-bottom: 14px;
  }

  .tab {
    padding: 12px 0;
    min-height: 44px;
    font-size: 0.98rem;
  }

  /* 输入框：44px 高度，字号 1rem（16px）防 iOS 自动放大 */
  .login-card input {
    min-height: 44px;
    font-size: 1rem;
    padding: 10px 12px;
  }

  .submit {
    min-height: 46px;
    padding: 12px;
    font-size: 1.02rem;
  }

  .hint {
    font-size: 0.8rem;
    margin-top: 12px;
  }
}
</style>
