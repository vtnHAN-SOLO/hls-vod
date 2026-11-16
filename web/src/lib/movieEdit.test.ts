import { describe, expect, it } from "vitest";

import { checkEdit, draftOf, MAX_OVERVIEW, MAX_TITLE } from "./movieEdit";

const original = { title: "Slow Light", year: 2023, overview: "Two keepers." };

describe("checkEdit", () => {
  it("sends nothing when nothing changed", () => {
    expect(checkEdit(draftOf(original), original)).toEqual({ ok: true, body: null });
  });

  it("sends only the changed fields, trimmed", () => {
    expect(checkEdit({ ...draftOf(original), title: "  Slower Light " }, original)).toEqual({
      ok: true,
      body: { title: "Slower Light" },
    });
    expect(checkEdit({ ...draftOf(original), year: "1999" }, original)).toEqual({ ok: true, body: { year: 1999 } });
  });

  it("clears the year and the overview with null", () => {
    expect(checkEdit({ title: "Slow Light", year: "", overview: "   " }, original)).toEqual({
      ok: true,
      body: { year: null, overview: null },
    });
  });

  it("keeps line breaks in the overview", () => {
    const r = checkEdit({ ...draftOf(original), overview: "One.\nTwo." }, original);
    expect(r).toEqual({ ok: true, body: { overview: "One.\nTwo." } });
  });

  it.each([
    [{ title: "  " }, "title"],
    [{ title: "x".repeat(MAX_TITLE + 1) }, "title"],
    [{ title: "a\u0007b" }, "title"],
    [{ title: "a\nb" }, "title"],
    [{ year: "1879" }, "year"],
    [{ year: "2101" }, "year"],
    [{ year: "20x0" }, "year"],
    [{ year: "999" }, "year"],
    [{ overview: "x".repeat(MAX_OVERVIEW + 1) }, "overview"],
    [{ overview: "a\u0000b" }, "overview"],
  ])("refuses %o in %s", (change, field) => {
    const r = checkEdit({ ...draftOf(original), ...change }, original);
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.field).toBe(field);
  });

  it("counts characters, not UTF-16 units, like the api", () => {
    const emoji = "🎬".repeat(MAX_TITLE); // 200 characters, 400 UTF-16 units
    expect(checkEdit({ ...draftOf(original), title: emoji }, original).ok).toBe(true);
  });

  it("accepts the bounds", () => {
    expect(checkEdit({ ...draftOf(original), year: "1880" }, original).ok).toBe(true);
    expect(checkEdit({ ...draftOf(original), year: "2100" }, original).ok).toBe(true);
  });
});

describe("draftOf", () => {
  it("shows a missing year and overview as empty fields", () => {
    expect(draftOf({ title: "T", year: null, overview: null })).toEqual({ title: "T", year: "", overview: "" });
  });
});
