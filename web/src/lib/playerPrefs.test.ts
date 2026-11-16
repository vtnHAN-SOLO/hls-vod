import { describe, expect, it } from "vitest";

import { DEFAULT_PREFS, loadPrefs, parsePrefs, PREFS_KEY, savePrefs, type Storage } from "./playerPrefs";

function memory(): Storage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return { data, getItem: (k) => data.get(k) ?? null, setItem: (k, v) => data.set(k, v) };
}

describe("player prefs", () => {
  it("round-trips through storage", () => {
    const s = memory();
    const p = { volume: 0.3, muted: true, speed: 1.5, subtitleLang: "vi", subtitleSize: "l" as const };
    expect(savePrefs(p, s)).toBe(true);
    expect(loadPrefs(s)).toEqual(p);
  });

  it("starts from the defaults with nothing stored, no storage, or storage that throws", () => {
    expect(loadPrefs(memory())).toEqual(DEFAULT_PREFS);
    expect(loadPrefs(null)).toEqual(DEFAULT_PREFS);
    const broken: Storage = {
      getItem: () => {
        throw new Error("SecurityError");
      },
      setItem: () => {
        throw new Error("QuotaExceededError");
      },
    };
    expect(loadPrefs(broken)).toEqual(DEFAULT_PREFS);
    expect(savePrefs(DEFAULT_PREFS, broken)).toBe(false);
    expect(savePrefs(DEFAULT_PREFS, null)).toBe(false);
  });

  it("keeps the valid fields of what is stored and defaults the rest", () => {
    expect(parsePrefs("not json")).toEqual(DEFAULT_PREFS);
    expect(parsePrefs("[1]")).toEqual(DEFAULT_PREFS);
    expect(
      parsePrefs(JSON.stringify({ volume: 2, muted: "yes", speed: 3, subtitleLang: "<x>", subtitleSize: "xl" })),
    ).toEqual(DEFAULT_PREFS);
    expect(parsePrefs(JSON.stringify({ volume: 0.4, speed: 0.75, subtitleLang: null }))).toEqual({
      ...DEFAULT_PREFS,
      volume: 0.4,
      speed: 0.75,
    });
  });

  it("uses one key", () => {
    const s = memory();
    savePrefs(DEFAULT_PREFS, s);
    expect([...s.data.keys()]).toEqual([PREFS_KEY]);
  });
});
