// Posters and publishing: which image a card asks for, what an upload must
// be before it is sent, and why a movie cannot be published yet. The api
// decides in the end (it reads the image's type from its bytes and the
// database enforces the publishing rule); this is for quick, clear feedback.
import type { AdminMovie, Movie } from "../api/client";

export type Poster = NonNullable<Movie["poster"]>;

/** Must match PUT /api/v1/admin/movies/{id}/poster in docs/openapi.yaml. */
export const MAX_POSTER_BYTES = 10 * 1024 * 1024;
export const MIN_POSTER_WIDTH = 300;
export const MIN_POSTER_HEIGHT = 450;
export const MAX_POSTER_PIXELS = 25_000_000;
/** The file picker's filter; the api checks the bytes, not this. */
export const POSTER_ACCEPT = "image/jpeg,image/png,image/webp";
const POSTER_TYPES = new Set(POSTER_ACCEPT.split(","));

/**
 * The `sizes` of each place a poster is drawn, so the browser picks the
 * smallest variant that is sharp there. They follow the layouts' widths:
 * a row card (RowItem portrait), a grid card, the spotlight, the detail page.
 */
export const POSTER_SIZES = {
  row: "(min-width: 1536px) 12vw, (min-width: 1024px) 14vw, (min-width: 768px) 18vw, (min-width: 640px) 23vw, 34vw",
  // The home grid: beside the most-popular sidebar from lg on.
  grid: "(min-width: 1536px) 13vw, (min-width: 1280px) 15vw, (min-width: 1024px) 17vw, (min-width: 640px) 24vw, 33vw",
  // Browse: the full width, 3 to 7 columns.
  browse: "(min-width: 1536px) 14vw, (min-width: 1280px) 16vw, (min-width: 1024px) 20vw, (min-width: 640px) 25vw, 33vw",
  ranked: "64px",
  spotlight: "(min-width: 1280px) 15rem, (min-width: 1024px) 13rem, (min-width: 768px) 11rem, 0px",
  detail: "(min-width: 768px) 18rem, 60vw",
} as const;

/** `srcset` of every variant: "url 300w, url 600w, …". */
export function posterSrcSet(p: Poster): string {
  return p.variants.map((v) => `${v.url} ${String(v.width)}w`).join(", ");
}

/**
 * The `src` for browsers without srcset, and the first guess before one is
 * chosen: the 600 variant when there is one, else the largest.
 */
export function posterSrc(p: Poster): string {
  const mid = p.variants.find((v) => v.width === 600);
  return (mid ?? p.variants.at(-1))?.url ?? "";
}

/** A sentence for a file that cannot be a poster, or null when it may be one. */
export function checkPosterFile(f: { size: number; type: string }): string | null {
  // The type the browser guessed from the name: a first filter only.
  if (!POSTER_TYPES.has(f.type)) return "Choose a JPEG, PNG or WebP image.";
  if (f.size > MAX_POSTER_BYTES) return "The image is larger than 10 MB.";
  if (f.size === 0) return "The file is empty.";
  return null;
}

/** A sentence for an image the api would refuse by its size, or null. */
export function checkPosterDimensions(width: number, height: number): string | null {
  if (width * height > MAX_POSTER_PIXELS) return "The image has more than 25 megapixels.";
  if (width < MIN_POSTER_WIDTH || height < MIN_POSTER_HEIGHT) {
    return `The image must be at least ${String(MIN_POSTER_WIDTH)}×${String(MIN_POSTER_HEIGHT)} pixels (it is ${String(width)}×${String(height)}).`;
  }
  return null;
}

/**
 * The centred 2:3 part of a width×height image that the api keeps, as
 * fractions of the image: what the preview shows.
 */
export function cropFractions(width: number, height: number): { x: number; y: number; w: number; h: number } {
  const k = Math.min(Math.floor(width / 2), Math.floor(height / 3));
  const cw = 2 * k;
  const ch = 3 * k;
  return {
    x: Math.floor((width - cw) / 2) / width,
    y: Math.floor((height - ch) / 2) / height,
    w: cw / width,
    h: ch / height,
  };
}

/** Upload progress as a whole percentage, 0..100. */
export function percent(loaded: number, total: number): number {
  if (total <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((loaded / total) * 100)));
}

export type PublishBlocker = "not_ready" | "missing_poster" | "missing_overview";

/** What a movie lacks to be published, in the api's order; empty when it can be. */
export function publishBlockers(m: Pick<AdminMovie, "status" | "poster" | "overview">): PublishBlocker[] {
  const out: PublishBlocker[] = [];
  if (m.status !== "ready") out.push("not_ready");
  if (m.poster === null) out.push("missing_poster");
  if (m.overview === null || m.overview.trim() === "") out.push("missing_overview");
  return out;
}

export const BLOCKER_TEXT: Record<PublishBlocker, string> = {
  not_ready: "It is not ready to play yet.",
  missing_poster: "Upload a poster first.",
  missing_overview: "Write a description first.",
};

export function isPublishBlocker(code: string): code is PublishBlocker {
  return code in BLOCKER_TEXT;
}

/** The sentence a disabled Publish button explains itself with. */
export function publishHint(m: Pick<AdminMovie, "status" | "poster" | "overview">): string | null {
  const b = publishBlockers(m);
  if (b.length === 0) return null;
  return b.map((x) => BLOCKER_TEXT[x]).join(" ");
}
