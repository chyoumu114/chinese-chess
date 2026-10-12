import { createApp } from 'vue'
import App from './App.vue'
import router from './router.js'
import './styles.css'
import { installRealtime, connectRealtime, loadMe, loadRules } from './store.js'

// Wire the WebSocket frames into the shared store before anything renders.
installRealtime()

const app = createApp(App)
app.use(router)
app.mount('#app')

// Guests may connect too (the server refuses to seat them), so the socket is
// opened unconditionally; then restore the stored session and cache the rules.
connectRealtime()
loadRules()
loadMe()
