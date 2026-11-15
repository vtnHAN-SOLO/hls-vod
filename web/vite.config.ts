import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The SPA's response headers (CSP and friends), one source of truth: served
// by `vite preview` here, and by the web nginx in the cluster. Not by
// the dev server: Vite's HMR client and React Refresh need inline scripts
// and eval that the policy forbids on purpose.
import securityHeaders from "./security-headers.json";

// Dev server: one origin for the browser, split by path exactly as the
// Ingress will split it in the cluster. No CORS in the app's own requests,
// and the code never knows a host name.
//
//   /api/*    -> api            (make run,      127.0.0.1:8080)
//   /media/*  -> media-delivery (make media-up, 127.0.0.1:8081)
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    host: "127.0.0.1", // never the LAN
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
      "/media": { target: "http://127.0.0.1:8081", changeOrigin: false },
    },
  },
  build: {
    // The /watch chunk is mostly hls.js (~520 kB minified, ~160 kB gzip),
    // loaded only on that route. Keep the warning for everything else.
    chunkSizeWarningLimit: 650,
  },
  preview: {
    host: "127.0.0.1",
    port: 4173,
    strictPort: true,
    headers: securityHeaders,
    // Same split as the dev server, so the built app can be walked end to end.
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
      "/media": { target: "http://127.0.0.1:8081", changeOrigin: false },
    },
  },
  test: { environment: "node", include: ["src/**/*.test.ts"] },
});
