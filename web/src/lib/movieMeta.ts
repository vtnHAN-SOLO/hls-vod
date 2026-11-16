// How a movie's facts are written in cards, the spotlight and the detail
// header. Only data the api has: no ratings, genres or cast.
import { formatTime } from "./format";

/** 6720 -> "1 h 52 min", 1920 -> "32 min", 36 -> "36 s"; null for unknown. */
export function formatRuntime(sec: number | null): string | null {
  if (sec === null || !Number.isFinite(sec) || sec <= 0) return null;
  if (sec < 60) return `${String(Math.round(sec))} s`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${String(min)} min`;
  const h = Math.floor(min / 60);
  const m = min % 60;
  return m === 0 ? `${String(h)} h` : `${String(h)} h ${String(m)} min`;
}

/** The top quality as a badge: 2160 -> "4K", 1080 -> "1080p", null -> null. */
export function qualityLabel(height: number | null): string | null {
  if (height === null || !Number.isFinite(height) || height <= 0) return null;
  if (height >= 2160) return "4K";
  return `${String(height)}p`;
}

/** Year and runtime, the parts that are known, in order. */
export function metaParts(movie: { year: number | null; runtime_sec: number | null }): string[] {
  const parts: string[] = [];
  if (movie.year !== null) parts.push(String(movie.year));
  const rt = formatRuntime(movie.runtime_sec);
  if (rt !== null) parts.push(rt);
  return parts;
}

/**
 * The play button's label: "Resume 23:10" when there is a position to resume
 * from, else "Play". A position of 0 is nothing to resume.
 */
export function playLabel(resumeSec: number | null | undefined): string {
  return resumeSec != null && resumeSec > 0 ? `Resume ${formatTime(resumeSec)}` : "Play";
}

/** The popular ranking's windows, as the tabs label them. */
export const POPULAR_WINDOWS = [
  { id: "week", label: "Week" },
  { id: "month", label: "Month" },
  { id: "all", label: "All time" },
] as const;

export type PopularWindow = (typeof POPULAR_WINDOWS)[number]["id"];

/** "1 viewer", "3 viewers". */
export function viewersLabel(n: number): string {
  return n === 1 ? "1 viewer" : `${String(n)} viewers`;
}
