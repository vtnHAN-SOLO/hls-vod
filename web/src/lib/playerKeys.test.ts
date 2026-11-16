import { describe, expect, it } from "vitest";

import { playerKey, stepVolume } from "./playerKeys";

const on = (tagName: string, extra: Record<string, unknown> = {}) => ({
  tagName,
  getAttribute: (n: string) => (n === "type" ? ((extra.type as string | undefined) ?? null) : null),
  isContentEditable: Boolean(extra.editable),
});
const player = on("DIV");

describe("playerKey", () => {
  it.each([
    [" ", { kind: "toggle-play" }],
    ["k", { kind: "toggle-play" }],
    ["ArrowLeft", { kind: "skip", seconds: -5 }],
    ["ArrowRight", { kind: "skip", seconds: 5 }],
    ["ArrowUp", { kind: "volume", delta: 0.1 }],
    ["ArrowDown", { kind: "volume", delta: -0.1 }],
    ["m", { kind: "toggle-mute" }],
    ["f", { kind: "toggle-fullscreen" }],
    ["c", { kind: "toggle-subtitles" }],
    ["0", { kind: "jump", fraction: 0 }],
    ["7", { kind: "jump", fraction: 0.7 }],
    ["Escape", { kind: "escape" }],
  ])("%j -> %o", (key, want) => {
    expect(playerKey({ key, target: player })).toEqual(want);
  });

  it("ignores other keys", () => {
    for (const key of ["a", "Enter", "Tab", "PageUp", "F5"]) expect(playerKey({ key, target: player })).toBeNull();
  });

  it("is not the player's while typing in a field", () => {
    for (const target of [on("INPUT"), on("INPUT", { type: "search" }), on("TEXTAREA"), on("SELECT"), on("DIV", { editable: true })]) {
      expect(playerKey({ key: " ", target })).toBeNull();
      expect(playerKey({ key: "k", target })).toBeNull();
      expect(playerKey({ key: "5", target })).toBeNull();
    }
    // Escape still closes a menu from a field inside it.
    expect(playerKey({ key: "Escape", target: on("INPUT") })).toEqual({ kind: "escape" });
  });

  it("works from a button, a checkbox or the volume slider", () => {
    for (const target of [on("BUTTON"), on("INPUT", { type: "range" }), on("INPUT", { type: "checkbox" })]) {
      expect(playerKey({ key: "m", target })).toEqual({ kind: "toggle-mute" });
    }
  });

  it("leaves shortcuts with modifiers to the browser", () => {
    expect(playerKey({ key: "f", ctrlKey: true, target: player })).toBeNull();
    expect(playerKey({ key: "ArrowLeft", altKey: true, target: player })).toBeNull();
    expect(playerKey({ key: "k", metaKey: true, target: player })).toBeNull();
  });
});

describe("stepVolume", () => {
  it.each([
    [0.5, 0.1, 0.6],
    [0.95, 0.1, 1],
    [0.05, -0.1, 0],
    [0.7, -0.1, 0.6], // whole percent, no 0.6000000001
  ])("%d %+d -> %d", (v, d, want) => {
    expect(stepVolume(v, d)).toBe(want);
  });
});
