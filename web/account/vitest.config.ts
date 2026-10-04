import { defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  plugins: [svelte()],
  resolve: { conditions: ['browser'] },
  test: {
    environment: 'jsdom',
    include: ['tests/**/*.test.ts'],
    passWithNoTests: false,
    env: {
      VITE_SESAME_API_URL: 'https://api.test.invalid',
      VITE_SESAME_SITE_ORIGIN: 'https://website.test.invalid',
    },
  },
})
