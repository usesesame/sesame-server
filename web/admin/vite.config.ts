import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { fileURLToPath } from 'node:url'
import { shippedPackages } from './scripts/shipped-packages.mjs'

const adminRoot = fileURLToPath(new URL('.', import.meta.url))

export default defineConfig({
  root: adminRoot,
  plugins: [svelte(), shippedPackages('portal')],
  server: { port: 4174, strictPort: true },
  preview: { host: '127.0.0.1', port: 4174, strictPort: true },
  build: { outDir: 'dist', emptyOutDir: true },
})
