import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  plugins: [svelte()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        ws: true,
      },
    },
  },
  build: {
    outDir: '../web/dist',
    emptyOutDir: true,
  },
})
