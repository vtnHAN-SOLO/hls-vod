import { describe, expect, it } from "vitest";

import { type CrossTabLock, type Granted, type GrantResult, Session, Unauthenticated } from "./session";

const tick = () => new Promise<void>((r) => setTimeout(r, 1));

/**
 * The api's refresh rules, in memory: each refresh rotates the token, and
 * presenting a rotated token revokes the whole family. `cookie` is the
 * browser's cookie jar, shared by every tab of the origin.
 */
class FakeApi {
  cookie: string | null = null;
  refreshCalls = 0;
  revoked = false; // the family (one login)
  private rotated = new Set<string>();
  private live = new Set<string>(); // access tokens that verify
  private n = 0;
  failNetwork = false;
  always401 = false;
  private issued: string[] = []; // every refresh cookie ever set
  private killed = new Set<string>(); // revoked by a password change

  private setCookie(c: string) {
    this.cookie = c;
    this.issued.push(c);
  }

  login(): string {
    this.setCookie(`r${++this.n}`);
    this.revoked = false;
    return this.issueAccess();
  }

  expireAccessTokens() {
    this.live.clear();
  }

  async refresh(): Promise<GrantResult> {
    this.refreshCalls++;
    if (this.failNetwork) return { kind: "failed", error: new Error("network down") };
    const sent = this.cookie; // read when the request leaves, as a browser does
    await tick();
    if (sent === null || this.revoked || this.killed.has(sent)) return { kind: "refused" };
    if (this.rotated.has(sent)) {
      this.revoked = true; // reuse: the whole session dies
      return { kind: "refused" };
    }
    this.rotated.add(sent);
    this.setCookie(`r${++this.n}`);
    return {
      kind: "granted",
      grant: { accessToken: this.issueAccess(), user: { id: "u1", username: "alice", role: "user" } },
    };
  }

  async get(token: string): Promise<{ response: Response }> {
    await tick();
    const ok = !this.always401 && !this.revoked && this.live.has(token);
    return { response: new Response(null, { status: ok ? 200 : 401 }) };
  }

  /** POST /me/password: every refresh cookie dies, a new one is set. */
  async changePassword(token: string): Promise<{ response: Response; grant?: Granted }> {
    await tick();
    if (!this.live.has(token)) return { response: new Response(null, { status: 401 }) };
    for (const c of this.issued) this.killed.add(c);
    this.setCookie(`r${++this.n}`);
    const grant = { accessToken: this.issueAccess(), user: { id: "u1", username: "alice", role: "user" as const } };
    return { response: new Response(null, { status: 200 }), grant };
  }

  private issueAccess(): string {
    const t = `a${++this.n}`;
    this.live.add(t);
    return t;
  }
}

/** What navigator.locks gives every tab of an origin: one queue. */
function sharedLock(): CrossTabLock {
  let tail: Promise<unknown> = Promise.resolve();
  return <T,>(fn: () => Promise<T>) => {
    const run = tail.then(fn);
    tail = run.catch(() => undefined);
    return run;
  };
}

const noLock: CrossTabLock = (fn) => fn();

function tab(api: FakeApi, lock: CrossTabLock): Session {
  return new Session({ refresh: () => api.refresh(), logout: () => Promise.resolve() }, lock);
}

describe("Session", () => {
  it("start: the refresh cookie decides, and it refreshes only once", async () => {
    const api = new FakeApi();
    const anon = tab(api, sharedLock());
    await Promise.all([anon.start(), anon.start()]);
    expect(anon.getState()).toEqual({ status: "anonymous" });
    expect(api.refreshCalls).toBe(1);

    api.login();
    const s = tab(api, sharedLock());
    await s.start();
    await s.start();
    expect(s.getState()).toMatchObject({ status: "authenticated", user: { username: "alice" } });
    expect(api.refreshCalls).toBe(2);
  });

  it("a 401 refreshes once for all concurrent requests, then retries", async () => {
    const api = new FakeApi();
    api.login();
    const s = tab(api, sharedLock());
    await s.start();
    api.expireAccessTokens();
    const before = api.refreshCalls;

    const results = await Promise.all([1, 2, 3].map(() => s.withAuth((t) => api.get(t))));
    expect(results.map((r) => r.response.status)).toEqual([200, 200, 200]);
    expect(api.refreshCalls - before).toBe(1);
  });

  it("two tabs refreshing at once stay logged in under the cross-tab lock", async () => {
    const api = new FakeApi();
    api.login();
    const lock = sharedLock();
    const a = tab(api, lock);
    const b = tab(api, lock);
    await Promise.all([a.start(), b.start()]);
    expect(api.revoked).toBe(false);

    // Both access tokens expire; both tabs are used at the same moment.
    api.expireAccessTokens();
    const [ra, rb] = await Promise.all([a.withAuth((t) => api.get(t)), b.withAuth((t) => api.get(t))]);
    expect([ra.response.status, rb.response.status]).toEqual([200, 200]);
    expect(api.revoked).toBe(false);
    expect(a.getState().status).toBe("authenticated");
    expect(b.getState().status).toBe("authenticated");
  });

  it("without the lock the same race is token reuse and logs both tabs out (why the lock exists)", async () => {
    const api = new FakeApi();
    api.login();
    const a = tab(api, noLock);
    const b = tab(api, noLock);
    await Promise.all([a.start(), b.start()]);
    expect(api.revoked).toBe(true);
    expect([a.getState().status, b.getState().status]).toContain("anonymous");
  });

  it("a late 401 retries with the token another request already refreshed", async () => {
    const api = new FakeApi();
    api.login();
    const s = tab(api, sharedLock());
    await s.start();
    api.expireAccessTokens();
    const before = api.refreshCalls;

    // Both send the expired token. The fast one's 401 refreshes; the slow
    // one's 401 arrives after that refresh is done, and must not refresh
    // a second time.
    const slow = s.withAuth(async (t) => {
      const r = await api.get(t);
      await new Promise((done) => setTimeout(done, 30));
      return r;
    });
    const fast = s.withAuth((t) => api.get(t));
    const [rs, rf] = await Promise.all([slow, fast]);
    expect([rs.response.status, rf.response.status]).toEqual([200, 200]);
    expect(api.refreshCalls - before).toBe(1);
  });

  it("a second 401 ends the session", async () => {
    const api = new FakeApi();
    api.login();
    const s = tab(api, sharedLock());
    await s.start();
    api.always401 = true;
    const r = await s.withAuth((t) => api.get(t));
    expect(r.response.status).toBe(401);
    expect(s.getState()).toEqual({ status: "anonymous" });
  });

  it("a revoked session cannot refresh: logged out", async () => {
    const api = new FakeApi();
    api.login();
    const s = tab(api, sharedLock());
    await s.start();
    api.revoked = true;
    api.expireAccessTokens();
    await expect(s.withAuth((t) => api.get(t))).rejects.toBeInstanceOf(Unauthenticated);
    expect(s.getState()).toEqual({ status: "anonymous" });
  });

  it("a network failure while refreshing keeps the session and reports the error", async () => {
    const api = new FakeApi();
    api.login();
    const s = tab(api, sharedLock());
    await s.start();
    api.expireAccessTokens();
    api.failNetwork = true;
    await expect(s.withAuth((t) => api.get(t))).rejects.toThrow("network down");
    expect(s.getState().status).toBe("authenticated");
  });

  it("a refresh that throws is a failure, not a stuck lock", async () => {
    const lock = sharedLock();
    const s = new Session({ refresh: () => Promise.reject(new Error("boom")), logout: () => Promise.resolve() }, lock);
    await s.start();
    expect(s.getState()).toEqual({ status: "anonymous" });
    // The lock is free again.
    await expect(lock(() => Promise.resolve("next"))).resolves.toBe("next");
  });

  it("logout forgets the token at once and tells the api", async () => {
    let loggedOut = 0;
    const api = new FakeApi();
    api.login();
    const s = new Session(
      {
        refresh: () => api.refresh(),
        logout: () => {
          loggedOut++;
          return Promise.resolve();
        },
      },
      sharedLock(),
    );
    await s.start();
    const seen: string[] = [];
    s.subscribe(() => seen.push(s.getState().status));
    await s.logout();
    expect(s.accessToken()).toBeNull();
    expect(seen).toEqual(["anonymous"]);
    expect(loggedOut).toBe(1);
  });

  describe("reissue (password change)", () => {
    const change = (api: FakeApi, s: Session) =>
      s.reissue(
        (t) => api.changePassword(t),
        (r) => r.grant ?? null,
      );

    it("keeps this tab logged in with the new token", async () => {
      const api = new FakeApi();
      api.login();
      const s = tab(api, sharedLock());
      await s.start();
      const before = s.accessToken();
      const r = await change(api, s);
      expect(r.response.status).toBe(200);
      expect(s.accessToken()).not.toBe(before);
      expect((await s.withAuth((t) => api.get(t))).response.status).toBe(200);
      // The cookie it set works: the next refresh is fine.
      expect((await s.refresh()).kind).toBe("granted");
    });

    it("a 401 refreshes inside the lock without waiting for itself", async () => {
      const api = new FakeApi();
      api.login();
      const s = tab(api, sharedLock()); // not re-entrant, like Web Locks
      await s.start();
      api.expireAccessTokens();
      const r = await change(api, s);
      expect(r.response.status).toBe(200);
      expect(s.getState().status).toBe("authenticated");
    });

    it("another tab refreshing at the same moment waits, then uses the new cookie", async () => {
      const api = new FakeApi();
      api.login();
      const lock = sharedLock();
      const a = tab(api, lock);
      const b = tab(api, lock);
      await a.start();
      await b.start();
      const [changed, refreshed] = await Promise.all([change(api, a), b.refresh()]);
      expect(changed.response.status).toBe(200);
      expect(refreshed.kind).toBe("granted");
      expect([a.getState().status, b.getState().status]).toEqual(["authenticated", "authenticated"]);
    });

    it("without the lock that refresh sends the revoked cookie and logs its tab out (why reissue locks)", async () => {
      const api = new FakeApi();
      api.login();
      const a = tab(api, noLock);
      const b = tab(api, noLock);
      await a.start();
      await b.start();
      const [, refreshed] = await Promise.all([change(api, a), b.refresh()]);
      expect(refreshed.kind).toBe("refused");
      expect(b.getState().status).toBe("anonymous");
    });

    it("a failed change keeps the session as it was", async () => {
      const api = new FakeApi();
      api.login();
      const s = tab(api, sharedLock());
      await s.start();
      const before = s.accessToken();
      const r = await s.reissue(
        () => Promise.resolve({ response: new Response(null, { status: 400 }) }),
        (): Granted | null => null,
      );
      expect(r.response.status).toBe(400);
      expect(s.accessToken()).toBe(before);
      expect(s.getState().status).toBe("authenticated");
    });
  });
});
