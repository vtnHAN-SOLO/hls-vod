import { describe, expect, it } from "vitest";

import { ExpiryGuard, once, REFRESH_WINDOW_MS } from "./mediaExpiry";

describe("ExpiryGuard", () => {
  it("refreshes on a 410, at most once per window", () => {
    let now = 1_000_000;
    const g = new ExpiryGuard(() => now);
    expect(g.decide(410)).toBe("refresh");
    // More errors from the same expiry while the refresh runs.
    expect(g.decide(410)).toBe("wait");
    g.done();
    // A second 410 inside the window: the fresh URLs fail too.
    now += REFRESH_WINDOW_MS - 1;
    expect(g.decide(410)).toBe("fail");
    now += 1;
    expect(g.decide(410)).toBe("refresh");
  });

  it("never retries a 403", () => {
    const g = new ExpiryGuard(() => 0);
    expect(g.decide(403)).toBe("fail");
    expect(g.decide(403)).toBe("fail");
    // ...and a 403 does not use up the refresh.
    expect(g.decide(410)).toBe("refresh");
  });

  it("leaves other statuses to the usual retry rules", () => {
    const g = new ExpiryGuard(() => 0);
    for (const s of [undefined, 0, 404, 500, 502]) expect(g.decide(s)).toBe("other");
  });
});

describe("once", () => {
  it("calls fn the first time only", () => {
    let n = 0;
    const f = once(() => {
      n++;
    });
    f();
    f();
    f();
    expect(n).toBe(1);
  });
});
