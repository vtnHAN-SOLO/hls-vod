/**
 * The home page's spotlight: which slide shows, and when it advances by
 * itself. Pure state plus an injected timer, so the rules are unit tested
 * with fake timers:
 *
 * - it advances every INTERVAL_MS, wrapping to the first slide;
 * - it never advances by itself while paused (pointer over it, focus inside
 *   it, the tab hidden) or when the viewer prefers reduced motion;
 * - a manual move (arrows, dots, keys) restarts the wait, so the slide just
 *   chosen gets a full interval.
 */

export const INTERVAL_MS = 8_000;

export interface Timers {
  setTimeout: (fn: () => void, ms: number) => number;
  clearTimeout: (id: number) => void;
}

const browserTimers: Timers = {
  setTimeout: (fn, ms) => window.setTimeout(fn, ms),
  clearTimeout: (id) => {
    window.clearTimeout(id);
  },
};

/** Why the rotation is held; any one of them stops it ("user": the pause button). */
export type PauseReason = "hover" | "focus" | "hidden" | "user";

export class Spotlight {
  private index = 0;
  private count = 0;
  private reducedMotion = false;
  private readonly paused = new Set<PauseReason>();
  private timer: number | null = null;
  private stopped = true;

  constructor(
    private readonly onChange: (index: number) => void,
    private readonly timers: Timers = browserTimers,
    private readonly intervalMs = INTERVAL_MS,
  ) {}

  /** The current slide. */
  current(): number {
    return this.index;
  }

  /** Whether it will advance by itself (for a pause indicator). */
  autoplaying(): boolean {
    return !this.stopped && this.timer !== null;
  }

  /** Starts (or restarts) with count slides. */
  start(count: number, reducedMotion: boolean): void {
    this.count = count;
    this.reducedMotion = reducedMotion;
    this.stopped = false;
    if (this.index >= count) this.set(0);
    this.schedule();
  }

  /** Stops for good (unmount). */
  stop(): void {
    this.stopped = true;
    this.clear();
  }

  setReducedMotion(reduced: boolean): void {
    this.reducedMotion = reduced;
    this.schedule();
  }

  pause(reason: PauseReason): void {
    this.paused.add(reason);
    this.schedule();
  }

  resume(reason: PauseReason): void {
    this.paused.delete(reason);
    this.schedule();
  }

  next(): void {
    this.goTo(this.index + 1);
  }

  prev(): void {
    this.goTo(this.index - 1);
  }

  /** Shows slide i (wrapping), and gives it a full interval. */
  goTo(i: number): void {
    if (this.count === 0) return;
    this.set(((i % this.count) + this.count) % this.count);
    this.schedule();
  }

  private set(i: number): void {
    if (i === this.index) return;
    this.index = i;
    this.onChange(i);
  }

  private clear(): void {
    if (this.timer !== null) {
      this.timers.clearTimeout(this.timer);
      this.timer = null;
    }
  }

  private schedule(): void {
    this.clear();
    if (this.stopped || this.reducedMotion || this.paused.size > 0 || this.count < 2) return;
    this.timer = this.timers.setTimeout(() => {
      this.timer = null;
      this.set((this.index + 1) % this.count);
      this.schedule();
    }, this.intervalMs);
  }
}
