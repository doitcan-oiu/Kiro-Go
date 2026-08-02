import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// The Go server mounts the panel under /admin/, so every emitted asset URL must
// be prefixed accordingly. Dev requests to /admin/api are proxied to the running
// Go process (default :8080, override with KIRO_DEV_TARGET).
const target = process.env.KIRO_DEV_TARGET || 'http://127.0.0.1:8080'

export default defineConfig({
  base: '/admin/',
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 900,
  },
  server: {
    port: 5173,
    proxy: {
      '/admin/api': { target, changeOrigin: true },
      '/v1': { target, changeOrigin: true },
    },
  },
})
