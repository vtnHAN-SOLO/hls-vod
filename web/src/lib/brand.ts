// The product's name as a deployment shows it. One source: VITE_BRAND_NAME
// and VITE_BRAND_ACCENT (the trailing part drawn in the accent colour),
// read by vite.config.ts at build time and given defaults there, so the page
// <title>, the web app manifest and import.meta.env all agree. Pure, so the
// rules are unit tested; vite.config.ts and src/brand.ts both use it.
//
// The public repository keeps the neutral defaults. A deployment sets its
// own name in web/.env.local (gitignored) or as build args of the web image.

export const DEFAULT_BRAND_NAME = "HLS VOD";
export const DEFAULT_BRAND_ACCENT = "VOD";

export interface Brand {
  name: string;
  /** A suffix of name drawn in the accent colour; "" for none. */
  accent: string;
}

/** What a name may hold: it is written into index.html and the manifest as is. */
// eslint-disable-next-line no-control-regex -- control characters are exactly what is refused
const UNSAFE = /[<>&"'`\u0000-\u001f\u007f]/;
const MAX_NAME = 40;

/**
 * The brand from the build's environment: missing or empty values take the
 * defaults. A name that is too long or holds markup characters, or an
 * accent that is not a suffix of the name, throws: the build fails rather
 * than ship a broken title.
 */
export function resolveBrand(env: Record<string, string | undefined>): Brand {
  const given = env.VITE_BRAND_NAME?.trim() ?? "";
  const name = given === "" ? DEFAULT_BRAND_NAME : given;
  const accentRaw = env.VITE_BRAND_ACCENT;
  // With a custom name and no accent given, nothing is accented: the
  // default "VOD" belongs to the default name only.
  const accent =
    accentRaw === undefined || accentRaw.trim() === ""
      ? name === DEFAULT_BRAND_NAME
        ? DEFAULT_BRAND_ACCENT
        : ""
      : accentRaw.trim();
  if (Array.from(name).length > MAX_NAME || UNSAFE.test(name)) {
    throw new Error(`VITE_BRAND_NAME ${JSON.stringify(name)}: at most ${String(MAX_NAME)} characters, no < > & " ' \` or control characters`);
  }
  if (accent !== "" && (!name.endsWith(accent) || accent === name)) {
    throw new Error(`VITE_BRAND_ACCENT ${JSON.stringify(accent)} must be a trailing part of VITE_BRAND_NAME ${JSON.stringify(name)}`);
  }
  return { name, accent };
}

/** The name split for the wordmark: the plain head and the accented tail. */
export function wordmarkParts(b: Brand): { head: string; accent: string } {
  if (b.accent === "") return { head: b.name, accent: "" };
  return { head: b.name.slice(0, b.name.length - b.accent.length), accent: b.accent };
}

export interface ManifestColours {
  background: string;
  theme: string;
}

/** The web app manifest for the brand; colours come from the design tokens. */
export function manifestFor(b: Brand, colours: ManifestColours) {
  return {
    name: b.name,
    short_name: b.name,
    start_url: "/",
    display: "standalone",
    background_color: colours.background,
    theme_color: colours.theme,
    icons: [
      { src: "/logo-192.png", sizes: "192x192", type: "image/png" },
      { src: "/logo-512.png", sizes: "512x512", type: "image/png" },
    ],
  };
}

/** A token's value from index.css (the text after `--color-bg:`), for the manifest. */
export function tokenValue(css: string, token: string): string {
  const m = new RegExp(`--${token}:\\s*([^;]+);`).exec(css);
  if (!m?.[1]) throw new Error(`design token --${token} not found in index.css`);
  return m[1].trim();
}
