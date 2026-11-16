import { describe, expect, it } from "vitest";

import { moveIndex } from "./keys";

describe("moveIndex: tabs (horizontal, wrapping)", () => {
  const tabs = { orientation: "horizontal", wrap: true } as const;
  it.each([
    ["ArrowRight", 0, 1],
    ["ArrowRight", 2, 0], // past the last: the first
    ["ArrowLeft", 0, 2], // before the first: the last
    ["ArrowLeft", 2, 1],
    ["Home", 2, 0],
    ["End", 0, 2],
  ])("%s from %d -> %d", (key, from, want) => {
    expect(moveIndex(key, from, 3, tabs)).toBe(want);
  });

  it("ignores keys that are not movement", () => {
    for (const key of ["Tab", "Enter", " ", "a", "ArrowDown", "ArrowUp"]) {
      expect(moveIndex(key, 1, 3, tabs)).toBeNull();
    }
  });
});

describe("moveIndex: a row of cards (horizontal, stops at the ends)", () => {
  const row = { orientation: "horizontal", wrap: false } as const;
  it.each([
    ["ArrowRight", 0, 1],
    ["ArrowRight", 4, 4],
    ["ArrowLeft", 0, 0],
    ["ArrowLeft", 3, 2],
    ["End", 1, 4],
  ])("%s from %d -> %d", (key, from, want) => {
    expect(moveIndex(key, from, 5, row)).toBe(want);
  });
});

describe("moveIndex: a menu (vertical, wrapping)", () => {
  const menu = { orientation: "vertical", wrap: true } as const;
  it.each([
    ["ArrowDown", 0, 1],
    ["ArrowDown", 2, 0],
    ["ArrowUp", 0, 2],
    ["Home", 1, 0],
  ])("%s from %d -> %d", (key, from, want) => {
    expect(moveIndex(key, from, 3, menu)).toBe(want);
  });
  it("ignores horizontal arrows", () => {
    expect(moveIndex("ArrowRight", 0, 3, menu)).toBeNull();
  });
});

describe("moveIndex: edge cases", () => {
  it("has nothing to move to in an empty group", () => {
    expect(moveIndex("ArrowRight", 0, 0, { orientation: "horizontal", wrap: true })).toBeNull();
  });
  it("clamps an index outside the group (focus left a removed item)", () => {
    expect(moveIndex("ArrowLeft", 9, 3, { orientation: "horizontal", wrap: false })).toBe(1);
    expect(moveIndex("ArrowRight", -1, 3, { orientation: "horizontal", wrap: false })).toBe(1);
  });
});
