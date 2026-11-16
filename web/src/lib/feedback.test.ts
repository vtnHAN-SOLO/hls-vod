import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { FADE_MS, type Feedback, FeedbackState, type Timers } from "./feedback";

const timers: Timers = {
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (id) => {
    clearTimeout(id);
  },
};

function state() {
  const seen: (Feedback | null)[] = [];
  const s = new FeedbackState((f) => seen.push(f), timers);
  return { s, seen };
}

describe("FeedbackState", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows a skip at once and clears it after the fade", () => {
    const { s } = state();
    s.skip(5);
    expect(s.shown()).toMatchObject({ kind: "skip", side: "forward", seconds: 5 });
    vi.advanceTimersByTime(FADE_MS - 1);
    expect(s.shown()).not.toBeNull();
    vi.advanceTimersByTime(1);
    expect(s.shown()).toBeNull();
  });

  it("adds up skips in the same direction inside the fade, and restarts it", () => {
    const { s } = state();
    s.skip(-5);
    vi.advanceTimersByTime(400);
    s.skip(-5);
    expect(s.shown()).toMatchObject({ side: "back", seconds: 10 });
    vi.advanceTimersByTime(400);
    s.skip(-5);
    expect(s.shown()).toMatchObject({ side: "back", seconds: 15 });
    vi.advanceTimersByTime(FADE_MS - 1); // restarted by the last skip
    expect(s.shown()).not.toBeNull();
    vi.advanceTimersByTime(1);
    expect(s.shown()).toBeNull();
  });

  it("starts over after the fade, and when the direction changes", () => {
    const { s } = state();
    s.skip(5);
    vi.advanceTimersByTime(FADE_MS);
    s.skip(5);
    expect(s.shown()).toMatchObject({ seconds: 5 });
    s.skip(-5);
    expect(s.shown()).toMatchObject({ side: "back", seconds: 5 });
  });

  it("gives every update a new seq, so the fade animation restarts", () => {
    const { s, seen } = state();
    s.skip(5);
    s.skip(5);
    const seqs = seen.filter((f) => f !== null).map((f) => f.seq);
    expect(new Set(seqs).size).toBe(2);
  });

  it("shows the volume as a percentage, or muted", () => {
    const { s } = state();
    s.volume(0.7, false);
    expect(s.shown()).toMatchObject({ kind: "volume", percent: 70, muted: false });
    s.volume(0.7, true);
    expect(s.shown()).toMatchObject({ kind: "volume", muted: true });
    s.skip(5); // another kind replaces it
    expect(s.shown()).toMatchObject({ kind: "skip", seconds: 5 });
  });

  it("flashes play and pause", () => {
    const { s } = state();
    s.playPause(true);
    expect(s.shown()).toMatchObject({ kind: "play" });
    s.playPause(false);
    expect(s.shown()).toMatchObject({ kind: "pause" });
  });

  it("does nothing after dispose", () => {
    const { s, seen } = state();
    s.skip(5);
    s.dispose();
    vi.advanceTimersByTime(FADE_MS * 2);
    expect(seen.at(-1)).not.toBeNull();
  });
});
