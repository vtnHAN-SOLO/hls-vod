/**
 * The browser side of a login session. Framework-free so it can be unit
 * tested; src/api/client.ts wires it to the real endpoints and
 * src/auth/AuthProvider.tsx to React.
 *
 * Rules:
 *  - The access token lives in this object only: never localStorage or
 *    sessionStorage, where any injected script could read it. A reload
 *    loses it, and start() gets a new one from the HttpOnly refresh cookie.
 *  - Refresh is single-flight within the tab (one shared promise) AND across
 *    tabs (a Web Lock). Every tab shares one refresh cookie, and each
 *    refresh rotates it: two tabs sending the same cookie at once would
 *    look like token reuse to the api, which then revokes the session in
 *    every tab. Under the lock the second tab waits, then sends the cookie
 *    the first one received, and both stay logged in.
 *  - A request that gets a 401 refreshes once and retries once; a second
 *    401 ends the session here (the UI then shows the login page).
 *  - A request that replaces the session itself (a password change: the api
 *    revokes every refresh token and sets a new cookie) runs through
 *    reissue(), under the same lock: a refresh racing it would send the old
 *    cookie, now revoked, and log that tab out.
 */

export type Role = "user" | "admin";

export interface SessionUser {
  id: string;
  username: string;
  role: Role;
}

export interface Granted {
  accessToken: string;
  user: SessionUser;
}

/** What a refresh or login attempt came back with. */
export type GrantResult =
  | { kind: "granted"; grant: Granted }
  | { kind: "refused" } // 401: no valid refresh cookie, or the session was revoked
  | { kind: "failed"; error: unknown }; // network or server failure: the session may still be fine

export interface SessionTransport {
  /** POST /api/v1/auth/refresh with the X-Requested-With header. */
  refresh: () => Promise<GrantResult>;
  /** POST /api/v1/auth/logout with the X-Requested-With header. */
  logout: () => Promise<void>;
}

/**
 * Runs fn while holding a lock shared by every tab of this origin
 * (navigator.locks.request). Without Web Locks, refresh is single-flight
 * within a tab only.
 */
export type CrossTabLock = <T>(fn: () => Promise<T>) => Promise<T>;

export type SessionState =
  | { status: "unknown" } // start() has not answered yet
  | { status: "anonymous" }
  | { status: "authenticated"; user: SessionUser };

export class Unauthenticated extends Error {
  constructor() {
    super("not logged in");
    this.name = "Unauthenticated";
  }
}

export const REFRESH_LOCK = "hls-vod-auth-refresh";

/** navigator.locks where the browser has it (secure contexts, including http://127.0.0.1). */
export function webLock(): CrossTabLock {
  const locks = typeof navigator !== "undefined" ? (navigator as Partial<Navigator>).locks : undefined;
  if (!locks) return (fn) => fn();
  // request() resolves with what the callback's promise resolves with; its
  // typings say Promise<Promise<T>>.
  return <T,>(fn: () => Promise<T>) => locks.request(REFRESH_LOCK, fn) as unknown as Promise<T>;
}

export class Session {
  private token: string | null = null;
  private state: SessionState = { status: "unknown" };
  private readonly listeners = new Set<() => void>();
  private inflight: Promise<GrantResult> | null = null;
  private started: Promise<void> | null = null;

  constructor(
    private readonly transport: SessionTransport,
    private readonly lock: CrossTabLock,
  ) {}

  getState = (): SessionState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  /** The current access token, if any. */
  accessToken(): string | null {
    return this.token;
  }

  /**
   * On app start: refresh once, so the HttpOnly cookie decides whether we
   * are logged in. Idempotent (React StrictMode runs effects twice).
   */
  start(): Promise<void> {
    this.started ??= this.refresh().then(() => undefined);
    return this.started;
  }

  /** Stores the result of a successful login. */
  granted(g: Granted): void {
    this.token = g.accessToken;
    this.set({ status: "authenticated", user: g.user });
  }

  /** Ends the session here and on the api. */
  async logout(): Promise<void> {
    this.clear();
    await this.transport.logout();
  }

  /**
   * Gets a new access token from the refresh cookie. Concurrent callers in
   * this tab share one request; other tabs wait for the lock. A refusal ends
   * the session; a network or server failure leaves it as it was (except on
   * start, where there is no session yet to keep).
   */
  refresh(): Promise<GrantResult> {
    this.inflight ??= this.lock(() => this.refreshHoldingLock()).finally(() => {
      this.inflight = null;
    });
    return this.inflight;
  }

  /**
   * Runs a request that hands out a new session (a password change) while
   * holding the refresh lock, so no tab refreshes with the cookie it is
   * about to revoke. `grantOf` picks the new session out of the answer; on
   * a 401 it refreshes (inside the lock: Web Locks are not re-entrant, so
   * refresh() here would wait for itself) and retries once.
   */
  reissue<R extends { response: Response }>(
    send: (accessToken: string) => Promise<R>,
    grantOf: (r: R) => Granted | null,
  ): Promise<R> {
    return this.lock(async () => {
      const renew = async () => this.tokenFrom(await this.refreshHoldingLock());
      let r = await send(this.token ?? (await renew()));
      if (r.response.status === 401) {
        r = await send(await renew());
        if (r.response.status === 401) this.clear();
      }
      const g = grantOf(r);
      if (g) this.granted(g);
      return r;
    });
  }

  /** One refresh; the caller holds the lock. */
  private async refreshHoldingLock(): Promise<GrantResult> {
    let r: GrantResult;
    try {
      r = await this.transport.refresh();
    } catch (error: unknown) {
      r = { kind: "failed", error };
    }
    if (r.kind === "granted") this.granted(r.grant);
    else if (r.kind === "refused" || this.state.status === "unknown") this.clear();
    return r;
  }

  /**
   * Runs an authenticated request. On a 401 it refreshes (unless another
   * request already did, with a token newer than the one that failed) and
   * retries once; a second 401 ends the session. Throws Unauthenticated when
   * there is no session to use, or the refresh's error when it could not
   * reach the api.
   */
  async withAuth<R extends { response: Response }>(send: (accessToken: string) => Promise<R>): Promise<R> {
    const token = this.token ?? (await this.renewed());
    const first = await send(token);
    if (first.response.status !== 401) return first;

    const next = this.token !== null && this.token !== token ? this.token : await this.renewed();
    const second = await send(next);
    if (second.response.status === 401) this.clear();
    return second;
  }

  /** Refreshes and returns the new token, or throws. */
  private async renewed(): Promise<string> {
    return this.tokenFrom(await this.refresh());
  }

  /** The token a refresh produced, or why there is none. */
  private tokenFrom(r: GrantResult): string {
    if (r.kind === "failed") throw r.error;
    if (r.kind === "refused" || this.token === null) throw new Unauthenticated();
    return this.token;
  }

  private clear(): void {
    this.token = null;
    this.set({ status: "anonymous" });
  }

  private set(s: SessionState): void {
    this.state = s;
    for (const l of this.listeners) l();
  }
}
