/// <reference types="vitest/config" />
import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// PCODER_SERVER_URL points the dev proxy at the Go server: set to
// http://server:8080 inside docker-compose.dev.yml, default localhost.
const serverUrl = process.env.PCODER_SERVER_URL ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    proxy: {
      '/api': { target: serverUrl, changeOrigin: true, ws: true },
      '/ws': { target: serverUrl, ws: true },
    },
  },
  // Vitest (npm run test:unit): jsdom + the same '@' alias, setup file
  // installs @testing-library/jest-dom matchers.
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    globals: true,
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
