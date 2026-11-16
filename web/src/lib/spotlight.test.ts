import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { INTERVAL_MS, Spotlight, type Timers } from "./spotlight";

const fakeTimers: Timers = {
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (id) => {
    clearTimeout(id);
  },
};

function spotlight() {
  const shown: number[] = [];
  const s = new Spotlight((i) => shown.push(i), fakeTimers);
  return { s, shown };
}

describe("Spotlight", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("advances every 8 s and wraps", () => {
    const { s, shown } = spotlight();
    s.start(3, false);
    vi.advanceTimersByTime(INTERVAL_MS - 1);
    expect(shown).toEqual([]);
    vi.advanceTimersByTime(1);
    expect(shown).toEqual([1]);
    vi.advanceTimersByTime(2 * INTERVAL_MS);
    expect(shown).toEqual([1, 2, 0]);
    expect(s.autoplaying()).toBe(true);
  });

  it("never advances by itself with reduced motion", () => {
    const { s, shown } = spotlight();
    s.start(3, true);
    vi.advanceTimersByTime(10 * INTERVAL_MS);
    expect(shown).toEqual([]);
    expect(s.autoplaying()).toBe(false);
    // The viewer can still move it.
    s.next();
    expect(s.current()).toBe(1);
    vi.advanceTimersByTime(10 * INTERVAL_MS);
    expect(s.current()).toBe(1);
  });

  it("stops when reduced motion is switched on mid-way, and resumes when it is off", () => {
    const { s, shown } = spotlight();
    s.start(3, false);
    vi.advanceTimersByTime(INTERVAL_MS / 2);
    s.setReducedMotion(true);
    vi.advanceTimersByTime(10 * INTERVAL_MS);
    expect(shown).toEqual([]);
    s.setReducedMotion(false);
    vi.advanceTimersByTime(INTERVAL_MS);
    expect(shown).toEqual([1]);
  });

  it("holds while hovered or focused, until every reason is gone", () => {
    const { s, shown } = spotlight();
    s.start(3, false);
    s.pause("hover");
    s.pause("focus");
    vi.advanceTimersByTime(5 * INTERVAL_MS);
    expect(shown).toEqual([]);
    s.resume("hover");
    vi.advanceTimersByTime(5 * INTERVAL_MS);
    expect(shown).toEqual([]); // focus still inside
    s.resume("focus");
    vi.advanceTimersByTime(INTERVAL_MS);
    expect(shown).toEqual([1]);
  });

  it("gives a manually chosen slide a full interval", () => {
    const { s } = spotlight();
    s.start(4, false);
    vi.advanceTimersByTime(INTERVAL_MS - 100);
    s.goTo(2);
    vi.advanceTimersByTime(INTERVAL_MS - 1);
    expect(s.current()).toBe(2);
    vi.advanceTimersByTime(1);
    expect(s.current()).toBe(3);
  });

  it("wraps prev and next", () => {
    const { s } = spotlight();
    s.start(3, true);
    s.prev();
    expect(s.current()).toBe(2);
    s.next();
    expect(s.current()).toBe(0);
  });

  it("does nothing with one slide, and nothing after stop", () => {
    const one = spotlight();
    one.s.start(1, false);
    vi.advanceTimersByTime(5 * INTERVAL_MS);
    expect(one.shown).toEqual([]);

    const { s, shown } = spotlight();
    s.start(3, false);
    s.stop();
    vi.advanceTimersByTime(5 * INTERVAL_MS);
    expect(shown).toEqual([]);
  });

  it("goes back to the first slide when the list shrinks under the current one", () => {
    const { s } = spotlight();
    s.start(5, true);
    s.goTo(4);
    s.start(2, true);
    expect(s.current()).toBe(0);
  });
});
