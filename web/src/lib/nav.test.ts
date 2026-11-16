import { describe, expect, it } from "vitest";

import { accountMenu, activeNav, FOOTER_NAV, MAIN_NAV, PUBLIC_PATHS } from "./nav";

const allLinks = (role: "user" | "admin") => [
  ...MAIN_NAV.map((i) => i.to),
  ...accountMenu(role).flatMap((i) => ("to" in i ? [i.to] : [])),
  ...FOOTER_NAV.map((i) => i.to),
];

describe("navigation per role", () => {
  it("gives a viewer no Admin anywhere, and the rest intact", () => {
    expect(allLinks("user")).not.toContain("/admin");
    expect(accountMenu("user").map((i) => i.label)).toEqual(["Account", "Sign out"]);
    expect(MAIN_NAV.map((i) => i.label)).toEqual(["Home", "Browse"]);
  });

  it("puts Admin in an admin's avatar menu, between Account and Sign out", () => {
    expect(accountMenu("admin").map((i) => i.label)).toEqual(["Account", "Admin", "Sign out"]);
    expect(accountMenu("admin")).toContainEqual({ key: "admin", label: "Admin", to: "/admin" });
  });

  it("keeps Admin out of the header for everyone", () => {
    expect(MAIN_NAV.map((i) => i.to)).not.toContain("/admin");
  });

  it("links About and Privacy from the footer", () => {
    expect(FOOTER_NAV).toEqual([
      { label: "About", to: "/about" },
      { label: "Privacy", to: "/about#privacy" },
    ]);
  });
});

describe("activeNav", () => {
  it.each([
    ["/", "/"],
    ["/browse", "/browse"],
    ["/browse/", "/browse"],
    ["/movies/1", null],
    ["/account", null],
    ["/admin", null],
    ["/about", null],
    ["/browsefoo", null],
  ])("%s highlights %s", (path, want) => {
    expect(activeNav(path)).toBe(want);
  });
});

describe("public paths", () => {
  it("lets /about be read logged out", () => {
    expect(PUBLIC_PATHS).toContain("/about");
  });

  it("keeps the catalog, account and admin behind a session", () => {
    for (const p of ["/", "/browse", "/account", "/admin", "/watch/x", "/movies/x"]) {
      expect(PUBLIC_PATHS as readonly string[]).not.toContain(p);
    }
  });
});
