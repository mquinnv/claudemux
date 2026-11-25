import { defineConfig } from 'vite';

// No @types/node here; this config only reads the environment.
declare const process: { env: Record<string, string | undefined> };

// The lobby origin is fixed at pack time (the network whitelist in app.json
// cannot change afterwards), so it is baked into the bundle. The token never
// is: it is entered on the phone and kept in the app's local storage.
export default defineConfig({
  build: { outDir: 'dist', target: 'es2022' },
  server: { host: true },
  define: {
    __REMOTE_ORIGIN__: JSON.stringify(process.env.CLAUDEMUX_REMOTE_ORIGIN ?? ''),
  },
});
