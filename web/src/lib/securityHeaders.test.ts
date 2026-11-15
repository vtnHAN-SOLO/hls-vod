import { describe, expect, it } from "vitest";

import headers from "../../security-headers.json";

function directives(csp: string): Map<string, string[]> {
  return new Map(
    csp
      .split(";")
      .map((d) => d.trim().split(/\s+/))
      .filter((parts) => parts[0] !== "")
      .map(([name = "", ...sources]) => [name, sources]),
  );
}

describe("security-headers.json", () => {
  const csp = directives(headers["Content-Security-Policy"]);

  it("allows only what the app needs", () => {
    expect(Object.fromEntries(csp)).toEqual({
      "default-src": ["'self'"],
      // Turnstile, loaded only on pages with a challenge (Cloudflare's
      // documented needs: script-src and frame-src).
      "script-src": ["'self'", "https://challenges.cloudflare.com"],
      "frame-src": ["https://challenges.cloudflare.com"],
      "connect-src": ["'self'"],
      "img-src": ["'self'", "data:"],
      // hls.js plays through MediaSource (blob: URLs) and its worker.
      "media-src": ["'self'", "blob:"],
      "worker-src": ["'self'", "blob:"],
      "style-src": ["'self'"],
      "object-src": ["'none'"],
      "base-uri": ["'self'"],
      "form-action": ["'self'"],
      "frame-ancestors": ["'none'"],
    });
  });

  it("never allows inline script, eval or inline styles", () => {
    for (const sources of csp.values()) {
      for (const bad of ["'unsafe-inline'", "'unsafe-eval'", "*"]) expect(sources).not.toContain(bad);
    }
  });

  it("sets the other headers", () => {
    expect(headers["X-Content-Type-Options"]).toBe("nosniff");
    expect(headers["Referrer-Policy"]).toBe("no-referrer");
    expect(headers["Permissions-Policy"]).toBe("camera=(), microphone=(), geolocation=()");
  });
});
