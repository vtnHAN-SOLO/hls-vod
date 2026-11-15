import { describe, expect, it } from "vitest";

import { ProgressTracker, SAVE_INTERVAL_MS } from "./progress";

function setup(start = 0) {
  let now = 1_000_000;
  const saves: [number, boolean][] = [];
  const t = new ProgressTracker(
    (pos, { final }) => saves.push([pos, final]),
    start,
    () => now,
  );
  const advance = (ms: number) => {
    now += ms;
  };
  return { t, saves, advance };
}

describe("ProgressTracker", () => {
  it("saves every 10 s while playing, in whole seconds", () => {
    const { t, saves, advance } = setup();
    for (let i = 1; i <= 25; i++) {
      advance(1000);
      t.onTime(i + 0.4, true);
    }
    expect(saves).toEqual([
      [10, false],
      [20, false],
    ]);
  });

  it("does not save on time updates while paused (seeking), but pause and hide do", () => {
    const { t, saves, advance } = setup();
    advance(SAVE_INTERVAL_MS * 3);
    t.onTime(300, false);
    expect(saves).toEqual([]);
    t.onHide();
    expect(saves).toEqual([[300, true]]);
  });

  it("saves on pause, once", () => {
    const { t, saves, advance } = setup();
    advance(3000);
    t.onTime(3, true);
    t.onPause(3.2);
    t.onPause(3.2);
    expect(saves).toEqual([[3, false]]);
    // The interval restarts after a pause save.
    advance(SAVE_INTERVAL_MS - 1);
    t.onTime(4, true);
    expect(saves).toHaveLength(1);
  });

  it("opening at the resume point and leaving saves nothing", () => {
    const { t, saves } = setup(30.7);
    t.onTime(30.9, false);
    t.onHide();
    expect(saves).toEqual([]);
  });

  it("pagehide saves the last position as final", () => {
    const { t, saves, advance } = setup(30);
    advance(4000);
    t.onTime(34, true);
    t.onHide();
    expect(saves).toEqual([[34, true]]);
  });

  it("leaving the page inside the app is an ordinary save", () => {
    const { t, saves, advance } = setup();
    advance(2000);
    t.onTime(2, true);
    t.onLeave();
    t.onHide();
    expect(saves).toEqual([[2, false]]);
  });

  it("ignores junk times", () => {
    const { t, saves } = setup();
    t.onTime(Number.NaN, true);
    t.onTime(-5, true);
    t.onHide();
    expect(saves).toEqual([]);
  });
});
