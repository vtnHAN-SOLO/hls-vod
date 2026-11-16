// The subtitle choices in the player's settings menu: tracks grouped by
// language, each named by its language (Intl.DisplayNames) and, when the
// source gave one, its own title; duplicates numbered. Pure, so the naming
// rules are tested, including a movie with dozens of tracks.

export interface Track {
  /** Index into the playback response's subtitles (and the <track> elements). */
  index: number;
  language: string; // BCP 47, "und" when unknown
  label: string | null; // the stream's title
}

export interface TrackChoice {
  index: number;
  language: string;
  name: string; // "English", "English (SDH)", "English 2"
}

export interface LanguageGroup {
  language: string;
  name: string; // the language's display name
  tracks: TrackChoice[];
}

export type LanguageNamer = (tag: string) => string | undefined;

/** Intl.DisplayNames in the viewer's language, or nothing where it is missing. */
export function browserNamer(locales?: string | string[]): LanguageNamer {
  let dn: Intl.DisplayNames | null = null;
  try {
    dn = new Intl.DisplayNames(locales, { type: "language" });
  } catch {
    dn = null;
  }
  return (tag) => {
    if (!dn || tag === "und") return undefined;
    try {
      const name = dn.of(tag);
      return name && name !== tag ? name : undefined;
    } catch {
      return undefined;
    }
  };
}

/** A language's name: the display name, "Unknown language" for und, else the tag. */
export function languageName(tag: string, namer: LanguageNamer): string {
  if (tag === "und") return "Unknown language";
  const n = namer(tag);
  return n ? n.charAt(0).toLocaleUpperCase() + n.slice(1) : tag;
}

/**
 * The tracks grouped by language, groups in the order their first track
 * appears. Inside a group: "English (SDH)" when the stream has a title,
 * and a number after any name that would otherwise repeat ("English",
 * "English 2"), so every choice reads differently.
 */
export function groupTracks(tracks: readonly Track[], namer: LanguageNamer): LanguageGroup[] {
  const groups = new Map<string, LanguageGroup>();
  for (const t of tracks) {
    let g = groups.get(t.language);
    if (!g) {
      g = { language: t.language, name: languageName(t.language, namer), tracks: [] };
      groups.set(t.language, g);
    }
    const label = t.label?.trim();
    g.tracks.push({ index: t.index, language: t.language, name: label ? `${g.name} (${label})` : g.name });
  }
  for (const g of groups.values()) {
    const seen = new Map<string, number>();
    const counts = new Map<string, number>();
    for (const c of g.tracks) counts.set(c.name, (counts.get(c.name) ?? 0) + 1);
    for (const c of g.tracks) {
      if ((counts.get(c.name) ?? 0) < 2) continue;
      const n = (seen.get(c.name) ?? 0) + 1;
      seen.set(c.name, n);
      if (n > 1) c.name = `${c.name} ${String(n)}`;
    }
  }
  return [...groups.values()];
}

/** The track to show for a remembered language: the first of it, else none. */
export function trackForLanguage(tracks: readonly Track[], language: string | null): number | null {
  if (language === null) return null;
  return tracks.find((t) => t.language === language)?.index ?? null;
}

/** The short label in the menu's main page: "Off", or the choice's name. */
export function currentSubtitleName(groups: readonly LanguageGroup[], index: number | null): string {
  if (index === null) return "Off";
  for (const g of groups) for (const c of g.tracks) if (c.index === index) return c.name;
  return "Off";
}
