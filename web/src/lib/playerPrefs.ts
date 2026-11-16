// The viewer's player settings, remembered in this browser: volume, mute,
// speed, subtitle language and size. localStorage only holds these
// conveniences, never a token or anything the server needs. Every access is
// guarded: storage may be missing, full or blocked (private windows), and the
// player then simply starts from the defaults.

export const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2] as const;
export type SubtitleSize = "s" | "m" | "l";
export const SUBTITLE_SIZES: readonly SubtitleSize[] = ["s", "m", "l"];

export interface PlayerPrefs {
  volume: number; // 0..1
  muted: boolean;
  speed: number; // one of SPEEDS
  /** BCP 47 language of the last subtitles chosen; null: subtitles off. */
  subtitleLang: string | null;
  subtitleSize: SubtitleSize;
}

export const DEFAULT_PREFS: PlayerPrefs = { volume: 1, muted: false, speed: 1, subtitleLang: null, subtitleSize: "m" };

export const PREFS_KEY = "player-prefs";

export interface Storage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

function storage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

/** Whatever is stored, checked field by field; anything odd is the default. */
export function parsePrefs(raw: string | null): PlayerPrefs {
  if (raw === null) return DEFAULT_PREFS;
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return DEFAULT_PREFS;
  }
  if (typeof v !== "object" || v === null) return DEFAULT_PREFS;
  const o = v as Record<string, unknown>;
  const p = { ...DEFAULT_PREFS };
  if (typeof o.volume === "number" && o.volume >= 0 && o.volume <= 1) p.volume = o.volume;
  if (typeof o.muted === "boolean") p.muted = o.muted;
  if (typeof o.speed === "number" && (SPEEDS as readonly number[]).includes(o.speed)) p.speed = o.speed;
  if (o.subtitleLang === null || (typeof o.subtitleLang === "string" && /^[a-z]{2,3}$/.test(o.subtitleLang))) {
    p.subtitleLang = o.subtitleLang;
  }
  if (typeof o.subtitleSize === "string" && (SUBTITLE_SIZES as readonly string[]).includes(o.subtitleSize)) {
    p.subtitleSize = o.subtitleSize as SubtitleSize;
  }
  return p;
}

export function loadPrefs(s: Storage | null = storage()): PlayerPrefs {
  try {
    return parsePrefs(s?.getItem(PREFS_KEY) ?? null);
  } catch {
    return DEFAULT_PREFS;
  }
}

/** Saves the prefs; returns false when storage refused (nothing else happens). */
export function savePrefs(p: PlayerPrefs, s: Storage | null = storage()): boolean {
  if (!s) return false;
  try {
    s.setItem(PREFS_KEY, JSON.stringify(p));
    return true;
  } catch {
    return false;
  }
}
