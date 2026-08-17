import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { fileURLToPath } from 'node:url'

const accountRoot = fileURLToPath(new URL('.', import.meta.url))

export default defineConfig({
  root: accountRoot,
  plugins: [svelte()],
  server: { port: 4175, strictPort: true },
  preview: { host: '127.0.0.1', port: 4175, strictPort: true },
  build: { outDir: 'dist', emptyOutDir: true },
})
