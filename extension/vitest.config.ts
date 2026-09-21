import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitest/config'

// Unit tests only — no CRXJS/manifest plugin, so the overlay and shared
// helpers can be exercised in jsdom without building the extension.
export default defineConfig({
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
  },
})
