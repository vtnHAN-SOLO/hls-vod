import { describe, expect, it } from "vitest";

import {
  browserNamer,
  currentSubtitleName,
  groupTracks,
  type LanguageNamer,
  type Track,
  trackForLanguage,
} from "./subtitleMenu";

const names: Record<string, string> = { en: "English", vi: "Vietnamese", fr: "French", ja: "Japanese" };
const namer: LanguageNamer = (tag) => names[tag];

describe("groupTracks", () => {
  it("groups by language in first-appearance order, with titles and numbers for repeats", () => {
    const tracks: Track[] = [
      { index: 0, language: "en", label: null },
      { index: 1, language: "vi", label: null },
      { index: 2, language: "en", label: "SDH" },
      { index: 3, language: "en", label: null },
      { index: 4, language: "und", label: null },
      { index: 5, language: "tlh", label: null },
    ];
    const groups = groupTracks(tracks, namer);
    expect(groups.map((g) => g.name)).toEqual(["English", "Vietnamese", "Unknown language", "tlh"]);
    expect(groups[0]?.tracks.map((c) => c.name)).toEqual(["English", "English (SDH)", "English 2"]);
  });

  it("keeps 49 tracks distinct and reachable", () => {
    const langs = ["en", "vi", "fr", "ja"];
    const tracks: Track[] = Array.from({ length: 49 }, (_, i) => ({
      index: i,
      language: langs[i % langs.length] ?? "und",
      label: i % 5 === 0 ? "Forced" : null,
    }));
    const groups = groupTracks(tracks, namer);
    const all = groups.flatMap((g) => g.tracks);
    expect(all).toHaveLength(49);
    expect(new Set(all.map((c) => c.index)).size).toBe(49);
    for (const g of groups) expect(new Set(g.tracks.map((c) => c.name)).size).toBe(g.tracks.length);
  });
});

describe("browserNamer", () => {
  it("names languages with Intl.DisplayNames, capitalised", () => {
    const n = browserNamer("en");
    expect(n("en")).toBe("English");
    expect(n("vi")).toBe("Vietnamese");
    expect(n("und")).toBeUndefined();
  });
});

describe("track choice", () => {
  const tracks: Track[] = [
    { index: 0, language: "en", label: null },
    { index: 1, language: "vi", label: null },
  ];
  it("finds the first track of a remembered language", () => {
    expect(trackForLanguage(tracks, "vi")).toBe(1);
    expect(trackForLanguage(tracks, "fr")).toBeNull();
    expect(trackForLanguage(tracks, null)).toBeNull();
  });
  it("names the current choice", () => {
    const g = groupTracks(tracks, namer);
    expect(currentSubtitleName(g, null)).toBe("Off");
    expect(currentSubtitleName(g, 1)).toBe("Vietnamese");
  });
});
