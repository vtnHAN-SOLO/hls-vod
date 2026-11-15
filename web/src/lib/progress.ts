/**
 * When the player saves watch progress: every SAVE_INTERVAL_MS of playing,
 * on pause (which also fires at the end of the movie), on leaving the watch
 * page, and when the page itself goes away (pagehide). Pure, so the rules
 * are unit tested; the WatchPage wires it to the <video> events and the api.
 */

export const SAVE_INTERVAL_MS = 10_000;

/** `final`: the page is going away, so the request must outlive it (fetch keepalive). */
export type SaveProgress = (positionSec: number, opts: { final: boolean }) => void;

export class ProgressTracker {
  private position: number;
  private lastSaved: number;
  private lastSaveAt: number;

  /** startSec: where playback starts, so opening and closing saves nothing. */
  constructor(
    private readonly save: SaveProgress,
    startSec: number,
    private readonly now: () => number = Date.now,
  ) {
    this.position = this.lastSaved = Math.floor(Math.max(startSec, 0));
    this.lastSaveAt = this.now();
  }

  /** Every timeupdate. Saves at most once per interval, and only while playing. */
  onTime(sec: number, playing: boolean): void {
    if (!Number.isFinite(sec) || sec < 0) return;
    this.position = Math.floor(sec);
    if (playing && this.now() - this.lastSaveAt >= SAVE_INTERVAL_MS) this.flush(false);
  }

  onPause(sec: number): void {
    this.onTime(sec, false);
    this.flush(false);
  }

  /** Leaving the watch page inside the app: an ordinary save. */
  onLeave(): void {
    this.flush(false);
  }

  /** pagehide: the last chance. */
  onHide(): void {
    this.flush(true);
  }

  private flush(final: boolean): void {
    this.lastSaveAt = this.now();
    if (this.position === this.lastSaved) return;
    this.lastSaved = this.position;
    this.save(this.position, { final });
  }
}
