import type { ReactNode } from "react";

/** Tile tints for the backdrop wall: token colours only. */
const TINTS = ["bg-raised", "bg-overlay", "bg-accent/25", "bg-surface", "bg-accent/10", "bg-overlay-hover"];

/**
 * The sign-in pages' frame: the logo, large, over a centred card, on a dimmed
 * wall of tiles.
 *
 * The wall is drawn, not a movie image: every image is behind a signed URL
 * the api hands out only to a session, and these pages are for visitors
 * without one. An endpoint that showed catalog images to anyone would leak
 * what a private library holds.
 */
export function AuthShell({ children, wide = false }: { children: ReactNode; wide?: boolean }) {
  return (
    <main className="relative isolate flex min-h-[calc(100vh-4rem)] items-center justify-center overflow-hidden px-4 py-12">
      <div aria-hidden="true" className="absolute inset-0 -z-10 overflow-hidden">
        <div className="absolute -inset-x-1/4 -inset-y-1/4 grid -rotate-12 grid-cols-6 gap-4 opacity-50 sm:grid-cols-8">
          {Array.from({ length: 96 }, (_, i) => (
            <div key={i} className={`aspect-video rounded-xl ${TINTS[(i * 7 + Math.floor(i / 8)) % TINTS.length] ?? "bg-raised"}`} />
          ))}
        </div>
        <div className="absolute inset-0 bg-[radial-gradient(ellipse_at_top,var(--color-accent)_0%,transparent_55%)] opacity-25" />
        <div className="absolute inset-0 bg-gradient-to-b from-bg/60 via-bg/85 to-bg" />
      </div>
      <div className={`flex w-full flex-col items-center gap-5 ${wide ? "max-w-md" : "max-w-sm"}`}>
        {/* The site's logo; decorative, the header names the site. 192 px file for 1x, 512 for denser screens. */}
        <img
          src="/logo-192.png"
          srcSet="/logo-192.png 1x, /logo-512.png 2x"
          alt=""
          width={120}
          height={120}
          decoding="async"
          draggable={false}
          className="drop-shadow-[0_12px_32px_var(--color-scrim)] select-none"
        />
        <div className="flex w-full flex-col gap-6 rounded-2xl border border-line bg-surface/90 p-6 shadow-2xl shadow-scrim/60 backdrop-blur-xl sm:p-8">
          {children}
        </div>
      </div>
    </main>
  );
}
