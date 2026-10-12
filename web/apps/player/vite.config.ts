import { defineConfig } from 'vite';
import { fileURLToPath } from 'node:url';

const outDir = fileURLToPath(new URL('../../../internal/agent/player/static', import.meta.url));

// The player is served by the local agent from its embedded static directory.
export default defineConfig({
  base: '/',
  build: {
    outDir,
    emptyOutDir: true,
    target: 'chrome100',
    assetsInlineLimit: 0,
    // /assets/{id} is the agent's media route; the app's own files live under /app/.
    assetsDir: 'app',
    sourcemap: false,
  },
  server: { fs: { allow: ['../../..'] } },
});
