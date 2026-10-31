import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

/** 开发时直连 Go 本地服务（默认 5301，可用 EDU_PORT 改） */
const API = process.env.VITE_API_TARGET || 'http://127.0.0.1:5301'

export default defineConfig({
  plugins: [vue()],
  base: '/',
  server: {
    port: 5302,
    proxy: { '/api': API }
  },
  build: {
    outDir: '../client/web',
    emptyOutDir: true,
    assetsDir: 'assets',
    chunkSizeWarningLimit: 2000
  }
})
