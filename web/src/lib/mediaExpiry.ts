/**
 * What the player does about an HTTP status from media-delivery. Signed
 * media URLs expire (6 h, or DEV_MEDIA_URL_TTL locally): media-delivery then
 * answers 410, and the player asks the api for fresh URLs and carries on.
 * A 403 means the token is wrong or was tampered with: asking again would
 * not help, so it is an error at once.
 *
 * At most one refresh per REFRESH_WINDOW_MS: a second 410 that soon means
 * the fresh URLs are refused too (a clock off by hours, a key mismatch), and
 * looping would only hammer the api. Pure, so the rules are unit tested.
 */

export const REFRESH_WINDOW_MS = 60_000;

export type ExpiryAction =
  /** Fetch new playback URLs now, then call done(). */
  | "refresh"
  /** A refresh is already under way: ignore this error, it replaces the source. */
  | "wait"
  /** Show the normal playback error. */
  | "fail"
  /** Not about the URL: the usual retry rules apply. */
  | "other";

export class ExpiryGuard {
  private lastRefresh = Number.NEGATIVE_INFINITY;
  private refreshing = false;

  constructor(private readonly now: () => number = Date.now) {}

  decide(status: number | undefined): ExpiryAction {
    if (status === 403) return "fail";
    if (status !== 410) return "other";
    if (this.refreshing) return "wait";
    if (this.now() - this.lastRefresh < REFRESH_WINDOW_MS) return "fail";
    this.lastRefresh = this.now();
    this.refreshing = true;
    return "refresh";
  }

  /** The refresh finished (with new URLs or not). */
  done(): void {
    this.refreshing = false;
  }
}

/**
 * fn on the first call only; later calls do nothing. A page whose poster
 * fails to load refetches its data once (the signed URL may have expired),
 * not once per poster, and not in a loop when the poster is simply missing.
 */
export function once(fn: () => void): () => void {
  let called = false;
  return () => {
    if (called) return;
    called = true;
    fn();
  };
}
