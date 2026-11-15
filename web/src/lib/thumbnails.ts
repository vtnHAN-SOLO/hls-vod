// Seek-preview thumbnails: the transcoder writes sprite sheets plus a WebVTT
// index whose cues look like
//
//   00:00:10.000 --> 00:00:20.000
//   thumbs/sprite_000.jpg#xywh=160,0,160,90
//
// Image paths are relative to the VTT file's URL.

export interface ThumbnailCue {
  start: number; // seconds
  end: number; // seconds
  image: string; // absolute URL of the sprite sheet
  x: number;
  y: number;
  width: number;
  height: number;
}

const TIMESTAMP = /^(?:(\d+):)?(\d{2}):(\d{2})\.(\d{3})$/;
const XYWH = /^(.+)#xywh=(\d+),(\d+),(\d+),(\d+)$/;

function seconds(ts: string): number | null {
  const m = TIMESTAMP.exec(ts.trim());
  if (!m) return null;
  const [, h = "0", min = "0", s = "0", ms = "0"] = m;
  return Number(h) * 3600 + Number(min) * 60 + Number(s) + Number(ms) / 1000;
}

/**
 * Parses a thumbnails WebVTT file. Cues that are malformed are skipped
 * rather than failing the whole index: previews are a nicety, playback
 * never depends on them. Cues are returned sorted by start time.
 */
export function parseThumbnails(vtt: string, vttUrl: string): ThumbnailCue[] {
  const text = vtt.replace(/^\uFEFF/, "").replace(/\r\n?/g, "\n");
  if (!text.startsWith("WEBVTT")) return [];

  const cues: ThumbnailCue[] = [];
  for (const block of text.split(/\n{2,}/)) {
    const lines = block.split("\n").map((l) => l.trim());
    const timing = lines.findIndex((l) => l.includes("-->"));
    if (timing < 0) continue;
    const [from = "", to = ""] = (lines[timing] ?? "").split("-->");
    const start = seconds(from);
    const end = seconds(to.trim().split(/\s+/)[0] ?? "");
    const ref = XYWH.exec(lines[timing + 1] ?? "");
    if (start === null || end === null || end <= start || !ref) continue;
    const [, path = "", x = "0", y = "0", w = "0", h = "0"] = ref;
    let image: string;
    try {
      image = new URL(path, vttUrl).href;
    } catch {
      continue;
    }
    cues.push({ start, end, image, x: Number(x), y: Number(y), width: Number(w), height: Number(h) });
  }
  return cues.sort((a, b) => a.start - b.start);
}

/** The cue covering time t (seconds), by binary search; null if none. */
export function cueAt(cues: readonly ThumbnailCue[], t: number): ThumbnailCue | null {
  let lo = 0;
  let hi = cues.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    const c = cues[mid];
    if (!c) break;
    if (t < c.start) hi = mid - 1;
    else if (t >= c.end) lo = mid + 1;
    else return c;
  }
  return null;
}
