import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath } from 'node:url';

const outDir = fileURLToPath(new URL('../../../internal/server/web/static', import.meta.url));

// Admin WebGUI (index.html) and operator UI (control.html) are embedded into the server.
// Dev: `npm run dev -w @opensignage/admin` proxies the API to a running server.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir,
    emptyOutDir: true,
    target: 'es2022',
    rollupOptions: {
      input: {
        index: fileURLToPath(new URL('./index.html', import.meta.url)),
        control: fileURLToPath(new URL('./control.html', import.meta.url)),
      },
    },
  },
  server: {
    fs: { allow: ['../../..'] },
    proxy: {
      '/api': { target: process.env.OS_DEV_SERVER ?? 'https://localhost:8443', secure: false, ws: true, changeOrigin: false },
      '/fonts': { target: process.env.OS_DEV_SERVER ?? 'https://localhost:8443', secure: false },
      '/ca.crt': { target: process.env.OS_DEV_SERVER ?? 'https://localhost:8443', secure: false },
    },
  },
});
