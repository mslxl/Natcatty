import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import path from 'path';
import { defineConfig } from 'vite';

export default defineConfig(() => {
    return {
      base: "./",
      server: {
        port: 5173,
        host: 'localhost',
        headers: {
          // Required for SharedArrayBuffer and WASM in some browsers
          'Cross-Origin-Opener-Policy': 'same-origin',
          // Use credentialless to allow loading cross-origin images (e.g. Google avatars)
          // while still enabling crossOriginIsolated.
          'Cross-Origin-Embedder-Policy': 'credentialless',
        },
      },
      build: {
        chunkSizeWarningLimit: 3000,
        target: 'esnext', // Required for top-level await in WASM modules
        rollupOptions: {
          output: {
            // Disable code splitting for Electron compatibility with file:// protocol
            manualChunks: undefined,
          },
        },
      },
      plugins: [tailwindcss(), react()],
      optimizeDeps: {
        exclude: ['ghostty-web'], // Don't pre-bundle ghostty-web to preserve WASM imports
      },
      resolve: {
        alias: {
          '@': path.resolve(__dirname, '.'),
        }
      }
    };
});
