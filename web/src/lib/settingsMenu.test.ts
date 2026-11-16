import { describe, expect, it } from "vitest";

import { CLOSED, menuReduce, type MenuState, qualityLabel, speedLabel } from "./settingsMenu";

const main: MenuState = { open: true, page: "main" };

describe("menuReduce", () => {
  it("opens on the main page and closes with the gear", () => {
    expect(menuReduce(CLOSED, { kind: "toggle" })).toEqual(main);
    expect(menuReduce({ open: true, page: "speed" }, { kind: "toggle" })).toEqual(CLOSED);
  });

  it("goes to a sub-page and back", () => {
    const sub = menuReduce(main, { kind: "go", page: "subtitles" });
    expect(sub).toEqual({ open: true, page: "subtitles" });
    expect(menuReduce(sub, { kind: "back" })).toEqual(main);
    expect(menuReduce(sub, { kind: "chosen" })).toEqual(main);
  });

  it("steps back one level with Escape", () => {
    expect(menuReduce({ open: true, page: "quality" }, { kind: "escape" })).toEqual(main);
    expect(menuReduce(main, { kind: "escape" })).toEqual(CLOSED);
    expect(menuReduce(CLOSED, { kind: "escape" })).toEqual(CLOSED);
  });

  it("closes from anywhere, and ignores rows while closed", () => {
    expect(menuReduce({ open: true, page: "size" }, { kind: "close" })).toEqual(CLOSED);
    expect(menuReduce(CLOSED, { kind: "go", page: "speed" })).toEqual(CLOSED);
    expect(menuReduce(CLOSED, { kind: "back" })).toEqual(CLOSED);
  });
});

describe("labels", () => {
  it("names speeds and qualities", () => {
    expect(speedLabel(1)).toBe("Normal");
    expect(speedLabel(1.5)).toBe("1.5×");
    expect(qualityLabel(null, 720)).toBe("Auto (720p)");
    expect(qualityLabel(null, null)).toBe("Auto");
    expect(qualityLabel(480, 720)).toBe("480p");
  });
});
