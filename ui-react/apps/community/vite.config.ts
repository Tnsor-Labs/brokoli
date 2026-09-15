import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const apiTarget = process.env.BROKOLI_API_TARGET || 'http://localhost:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    port: Number(process.env.PORT) || 5173,
    strictPort: true,
    proxy: { '/api': { target: apiTarget, ws: true, changeOrigin: false } },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // The icon sprite is referenced with <use href="sprite.svg#id">, which does not work from a data: URI.
    assetsInlineLimit: (file) => (file.endsWith('.svg') ? false : undefined),
  },
})
