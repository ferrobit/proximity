import { writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const placeholder = fileURLToPath(new URL('./dist/.gitkeep', import.meta.url))

export default defineConfig({
  plugins: [vue(), { name: 'keep-dist-placeholder', closeBundle: () => writeFileSync(placeholder, '') }],
  build: { outDir: 'dist', emptyOutDir: true, chunkSizeWarningLimit: 2500 },
  server: { proxy: { '/api': 'http://127.0.0.1:8899' } }
})
