// On-screen feedback over the video: the skip bubble ("10s" on the left or
// right), the volume level, and the play/pause flash. Each appears at once
// and is gone after FADE_MS; the component fades it with CSS over that
// time. Pure state plus injected timers, so the timing is unit tested.

export const FADE_MS = 600;

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

export type Feedback =
  | { kind: "skip"; side: "back" | "forward"; seconds: number; seq: number }
  | { kind: "volume"; percent: number; muted: boolean; seq: number }
  | { kind: "play" | "pause"; seq: number };

/**
 * Holds the one feedback shown now. Skips in the same direction inside
 * FADE_MS of the last one add up ("5s", "10s", "15s") and restart the fade;
 * a skip the other way, or any other feedback, starts over. seq changes on
 * every update, so the component can restart its fade animation.
 */
export class FeedbackState {
  private current: Feedback | null = null;
  private timer: number | null = null;
  private seq = 0;

  constructor(
    private readonly onChange: (f: Feedback | null) => void,
    private readonly timers: Timers = browserTimers,
  ) {}

  shown(): Feedback | null {
    return this.current;
  }

  skip(seconds: number): void {
    const side = seconds < 0 ? "back" : "forward";
    const abs = Math.abs(seconds);
    const prev = this.current;
    const total = prev?.kind === "skip" && prev.side === side ? prev.seconds + abs : abs;
    this.show({ kind: "skip", side, seconds: total, seq: ++this.seq });
  }

  volume(level: number, muted: boolean): void {
    this.show({ kind: "volume", percent: Math.round(level * 100), muted, seq: ++this.seq });
  }

  playPause(playing: boolean): void {
    this.show({ kind: playing ? "play" : "pause", seq: ++this.seq });
  }

  dispose(): void {
    if (this.timer !== null) this.timers.clearTimeout(this.timer);
    this.timer = null;
  }

  private show(f: Feedback): void {
    this.current = f;
    this.onChange(f);
    this.dispose();
    this.timer = this.timers.setTimeout(() => {
      this.timer = null;
      this.current = null;
      this.onChange(null);
    }, FADE_MS);
  }
}
