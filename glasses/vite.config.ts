import { defineConfig } from 'vite';

export default defineConfig({
  build: { outDir: 'dist', target: 'es2022' },
  server: { host: true },
});
