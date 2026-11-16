import { describe, expect, it } from "vitest";

import { volumeIcon, volumeSettable, wheelVolume } from "./volume";

describe("volumeIcon", () => {
  it.each([
    [1, false, "high"],
    [0.5, false, "high"],
    [0.49, false, "low"],
    [0, false, "muted"],
    [0.8, true, "muted"],
  ])("%d muted=%s -> %s", (v, m, want) => {
    expect(volumeIcon(v, m)).toBe(want);
  });
});

describe("volumeSettable", () => {
  it("is true where setting the volume sticks, and restores it", () => {
    const media = { volume: 0.8 };
    expect(volumeSettable(media)).toBe(true);
    expect(media.volume).toBe(0.8);
  });

  it("is false where the volume is fixed (iOS, iPadOS): the slider is hidden there", () => {
    const ios = {
      get volume() {
        return 1;
      },
      set volume(_v: number) {
        /* ignored, as iOS does */
      },
    };
    expect(volumeSettable(ios)).toBe(false);
  });

  it("is false where setting throws", () => {
    const locked = {
      get volume() {
        return 1;
      },
      set volume(_v: number) {
        throw new Error("not allowed");
      },
    };
    expect(volumeSettable(locked)).toBe(false);
  });
});

describe("wheelVolume", () => {
  it("raises on wheel up, lowers on wheel down, within 0..1", () => {
    expect(wheelVolume(0.5, -100)).toBe(0.55);
    expect(wheelVolume(0.5, 100)).toBe(0.45);
    expect(wheelVolume(1, -100)).toBe(1);
    expect(wheelVolume(0.02, 100)).toBe(0);
    expect(wheelVolume(0.5, 0)).toBe(0.5);
  });
});
