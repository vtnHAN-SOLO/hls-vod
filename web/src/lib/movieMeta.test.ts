import { describe, expect, it } from "vitest";

import { formatRuntime, metaParts, playLabel, qualityLabel, viewersLabel } from "./movieMeta";

describe("formatRuntime", () => {
  it.each([
    [6720, "1 h 52 min"],
    [7200, "2 h"],
    [1920, "32 min"],
    [36, "36 s"],
    [0, null],
    [null, null],
  ])("%s -> %s", (sec, want) => {
    expect(formatRuntime(sec)).toBe(want);
  });
});

describe("qualityLabel", () => {
  it.each([
    [2160, "4K"],
    [1080, "1080p"],
    [720, "720p"],
    [null, null],
  ])("%s -> %s", (h, want) => {
    expect(qualityLabel(h)).toBe(want);
  });
});

describe("metaParts", () => {
  it("lists only what is known", () => {
    expect(metaParts({ year: 2019, runtime_sec: 1920 })).toEqual(["2019", "32 min"]);
    expect(metaParts({ year: null, runtime_sec: 1920 })).toEqual(["32 min"]);
    expect(metaParts({ year: null, runtime_sec: null })).toEqual([]);
  });
});

describe("playLabel", () => {
  it.each([
    [1390, "Resume 23:10"],
    [3725, "Resume 1:02:05"],
    [0, "Play"],
    [null, "Play"],
    [undefined, "Play"],
  ])("%s -> %s", (pos, want) => {
    expect(playLabel(pos)).toBe(want);
  });
});

describe("viewersLabel", () => {
  it("is singular for one", () => {
    expect(viewersLabel(1)).toBe("1 viewer");
    expect(viewersLabel(12)).toBe("12 viewers");
  });
});
