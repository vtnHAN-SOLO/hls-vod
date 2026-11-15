import { describe, expect, it } from "vitest";

import { formatBitrate, formatBytes, formatRemaining, formatTime, watchedFraction } from "./format";

describe("formatTime", () => {
  it.each([
    [0, "0:00"],
    [75, "1:15"],
    [400, "6:40"],
    [3725, "1:02:05"],
    [-1, "0:00"],
    [Number.NaN, "0:00"],
  ])("%s -> %s", (t, want) => {
    expect(formatTime(t)).toBe(want);
  });
});

describe("formatBytes", () => {
  it.each([
    [0, "0 B"],
    [512, "512 B"],
    [96_943_571, "92.5 MB"],
    [4_000_000_000, "3.7 GB"],
    [null, "—"],
  ])("%s -> %s", (n, want) => {
    expect(formatBytes(n)).toBe(want);
  });
});

describe("formatBitrate", () => {
  it.each([
    [1939, "1.9 Mbit/s"],
    [837, "837 kbit/s"],
    [null, "—"],
  ])("%s -> %s", (k, want) => {
    expect(formatBitrate(k)).toBe(want);
  });
});

describe("watch progress", () => {
  it.each([
    [600, 5400, 600 / 5400],
    [0, 5400, 0],
    [6000, 5400, 1],
    [-5, 5400, 0],
    [600, null, 0],
    [600, 0, 0],
  ])("watchedFraction(%s, %s) = %s", (pos, runtime, want) => {
    expect(watchedFraction(pos, runtime)).toBeCloseTo(want);
  });

  it.each([
    [600, 5400, "80 min left"],
    [5390, 5400, "1 min left"],
    [5400, 5400, "1 min left"],
    [600, null, "at 10:00"],
  ])("formatRemaining(%s, %s) = %s", (pos, runtime, want) => {
    expect(formatRemaining(pos, runtime)).toBe(want);
  });
});
