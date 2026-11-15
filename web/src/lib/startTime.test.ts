import { describe, expect, it } from "vitest";

import { clampStart, parseStartTime } from "./startTime";

describe("parseStartTime", () => {
  it.each([
    ["90", 90],
    ["90.5", 90.5],
    ["0", null],
    [null, null],
    ["", null],
    ["-5", null],
    ["1e3", null],
    ["90s", null],
    ["Infinity", null],
    ["NaN", null],
    [" 90", null],
  ])("%j -> %s", (raw, want) => {
    expect(parseStartTime(raw)).toBe(want);
  });
});

describe("clampStart", () => {
  it.each([
    [null, 400, 0],
    [120, 400, 120],
    [399.5, 400, 0],
    [500, 400, 0],
    [120, Number.NaN, 0],
    [120, Number.POSITIVE_INFINITY, 0],
  ])("t=%s duration=%s -> %s", (t, d, want) => {
    expect(clampStart(t, d)).toBe(want);
  });
});
