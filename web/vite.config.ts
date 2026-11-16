import { readFileSync } from "node:fs";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { loadEnv, type Plugin } from "vite";
import { defineConfig } from "vitest/config";

import { type Brand, manifestFor, resolveBrand, tokenValue } from "./src/lib/brand";
import { parse, substitute } from "./src/lib/markdown";

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
// The brand (src/lib/brand.ts): VITE_BRAND_NAME and VITE_BRAND_ACCENT from
// web/.env.local or the environment (a container build passes them as build
// args), defaulted here and written back into process.env, so index.html's
// %VITE_BRAND_NAME%, import.meta.env and the manifest see the same values.
function brandFromEnv(mode: string): Brand {
  const brand = resolveBrand(loadEnv(mode, import.meta.dirname, "VITE_"));
  process.env.VITE_BRAND_NAME = brand.name;
  process.env.VITE_BRAND_ACCENT = brand.accent;
  return brand;
}

// manifest.webmanifest, generated from the brand and the design tokens
// (src/index.css): emitted into the build (vite preview and the web nginx
// serve that file), and generated on request by the dev server, so there is
// no static copy to fall out of date.
function brandManifest(brand: Brand): Plugin {
  const body = () => {
    const css = readFileSync(new URL("./src/index.css", import.meta.url), "utf8");
    const bg = tokenValue(css, "color-bg");
    return JSON.stringify(manifestFor(brand, { background: bg, theme: bg }), null, 2) + "\n";
  };
  const serve = (server: { middlewares: { use: (path: string, fn: (req: unknown, res: import("node:http").ServerResponse) => void) => void } }) => {
    server.middlewares.use("/manifest.webmanifest", (_req, res) => {
      res.setHeader("Content-Type", "application/manifest+json");
      res.end(body());
    });
  };
  return {
    name: "brand-manifest",
    configureServer: serve,
    generateBundle() {
      this.emitFile({ type: "asset", fileName: "manifest.webmanifest", source: body() });
    },
  };
}

// The site's own pages (src/content/*.md, e.g. About): {{brand}} replaced,
// parsed into a tree (src/lib/markdown.ts) at build time, so the bundle
// holds data and no Markdown library, and React renders it without setting
// HTML. Raw HTML or an unknown placeholder fails the build.
function markdownPages(brand: Brand): Plugin {
  return {
    name: "markdown-pages",
    enforce: "pre",
    transform(code, id) {
      if (!id.endsWith(".md")) return null;
      try {
        const doc = parse(substitute(code, { brand: brand.name }));
        return { code: `export default ${JSON.stringify(doc)};\n`, map: null };
      } catch (err) {
        this.error(`${id}: ${err instanceof Error ? err.message : String(err)}`);
      }
    },
  };
}

export default defineConfig(({ mode }) => {
  const brand = brandFromEnv(mode);
  return {
    plugins: [react(), tailwindcss(), brandManifest(brand), markdownPages(brand)],
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
  };
});
