import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    proxy: {
      // SSE must be listed first — more specific path wins.
      '/api/jobs/stream': {
        target: process.env.API_TARGET ?? 'http://localhost:8080',
        rewrite: (p) => p.replace(/^\/api/, ''),
        changeOrigin: true,
      },
      '/api': {
        target: process.env.API_TARGET ?? 'http://localhost:8080',
        rewrite: (p) => p.replace(/^\/api/, ''),
        changeOrigin: true,
      },
    },
  },
})
