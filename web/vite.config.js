import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The Go server serves web/dist as static files (XIANGQI_PUBLIC_DIR), so the
// build must be relative to the site root.
export default defineConfig({
  base: '/',
  plugins: [vue()],
  build: {
    outDir: 'dist',
    emptyOutDir: true
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true
      },
      // The WebSocket handshake cannot be proxied as plain HTTP.
      '/ws': {
        target: 'http://127.0.0.1:8080',
        ws: true,
        changeOrigin: true
      }
    }
  }
})
