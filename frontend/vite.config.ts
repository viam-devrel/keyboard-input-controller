import { defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  // The app is served from a path under the Viam app host, never a domain
  // root, so asset URLs must be relative.
  base: './',
  plugins: [svelte()],
  build: { outDir: 'dist', emptyOutDir: true, target: 'esnext' },
  test: { environment: 'jsdom', globals: true },
})
