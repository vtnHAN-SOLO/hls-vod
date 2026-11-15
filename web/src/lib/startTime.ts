/**
 * Parses the ?t= start position in seconds ("90", "90.5"). Anything else,
 * including negatives, is ignored and playback starts from the beginning.
 *
 * ?t= overrides the saved watch progress (playback's resume_sec), which
 * lives in Postgres. Nothing is stored in the browser.
 */
export function parseStartTime(raw: string | null): number | null {
  if (raw === null || !/^\d+(\.\d+)?$/.test(raw)) return null;
  const t = Number(raw);
  return Number.isFinite(t) && t > 0 ? t : null;
}

/** Where to actually start: past the end (or within 1s of it) means from 0. */
export function clampStart(t: number | null, duration: number): number {
  if (t === null || !Number.isFinite(duration) || duration <= 0) return 0;
  return t < duration - 1 ? t : 0;
}
