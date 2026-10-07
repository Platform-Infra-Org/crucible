/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'

export default defineConfig({
  plugins: [react()],
  worker: { format: 'es' },
  // monaco-yaml's worker imports monaco-editor/esm/vs/…, which monaco-editor 0.57's exports map no longer allows.
  resolve: { alias: [{ find: /^monaco-editor\/esm\/vs\/(.*)$/, replacement: fileURLToPath(new URL('./node_modules/monaco-editor/esm/vs/$1', import.meta.url)) }] },
  server: {
    proxy: {
      '/api': { target: 'http://localhost:8080', ws: true },
      '/auth': 'http://localhost:8080',
    },
  },
  test: { environment: 'node' },
})
