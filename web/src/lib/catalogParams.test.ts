import { describe, expect, it } from "vitest";

import {
  type CatalogState,
  catalogRequest,
  describeFilters,
  draftFrom,
  EMPTY_STATE,
  hasFilters,
  MAX_QUERY_LENGTH,
  PAGE_SIZE,
  parseYear,
  readCatalogParams,
  submitDraft,
  writeCatalogParams,
} from "./catalogParams";

const read = (qs: string) => readCatalogParams(new URLSearchParams(qs));
const state = (s: Partial<CatalogState>): CatalogState => ({ ...EMPTY_STATE, ...s });

describe("readCatalogParams", () => {
  it("defaults to everything, newest first, page 1", () => {
    expect(read("")).toEqual(EMPTY_STATE);
    expect(EMPTY_STATE).toEqual({ q: "", sort: "added_desc", yearFrom: null, yearTo: null, status: null, page: 1 });
  });

  it("reads valid values", () => {
    expect(read("q=matrix&sort=year_desc&year_from=1990&year_to=1999&status=ready&page=3")).toEqual({
      q: "matrix",
      sort: "year_desc",
      yearFrom: 1990,
      yearTo: 1999,
      status: "ready",
      page: 3,
    });
  });

  it.each(["status=deleted", "status=READY", "status=constructor", "status="])("ignores an unknown status: %s", (qs) => {
    expect(read(qs).status).toBeNull();
  });

  it.each(["sort=rating", "sort=TITLE_ASC", "sort=constructor", "sort="])("falls back to the default sort for %s", (qs) => {
    expect(read(qs).sort).toBe("added_desc");
  });

  it.each(["year_from=1879", "year_from=2101", "year_from=19x9", "year_from=1999.5", "year_from=", "year_from=-1990"])(
    "ignores a year outside 1880..2100 or not a year: %s",
    (qs) => {
      expect(read(qs).yearFrom).toBeNull();
    },
  );

  it.each(["page=0", "page=-2", "page=1.5", "page=abc", "page="])("falls back to page 1 for %s", (qs) => {
    expect(read(qs).page).toBe(1);
  });

  it("keeps % and _ as typed and caps the length", () => {
    expect(read("q=100%25_x").q).toBe("100%_x");
    expect(read(`q=${"a".repeat(150)}`).q).toHaveLength(MAX_QUERY_LENGTH);
  });
});

describe("writeCatalogParams", () => {
  it("omits defaults", () => {
    expect(writeCatalogParams(EMPTY_STATE).toString()).toBe("");
  });

  it("round-trips", () => {
    const s = state({ q: "50% off_", sort: "title_asc", yearFrom: 1880, yearTo: 2100, status: "failed", page: 2 });
    expect(readCatalogParams(writeCatalogParams(s))).toEqual(s);
  });
});

describe("catalogRequest", () => {
  it("pages, filters and sorts", () => {
    expect(
      catalogRequest(state({ q: " matrix ", sort: "year_asc", yearFrom: 1990, yearTo: 1999, status: "ready", page: 3 })),
    ).toEqual({
      q: "matrix",
      sort: "year_asc",
      year_from: 1990,
      year_to: 1999,
      status: "ready",
      limit: PAGE_SIZE,
      offset: 2 * PAGE_SIZE,
    });
  });

  it("sends nothing it does not need: no blank q, no default sort, no open bounds", () => {
    expect(catalogRequest(state({ q: "   " }))).toEqual({ limit: PAGE_SIZE, offset: 0 });
  });
});

describe("hasFilters", () => {
  it.each([
    [state({ page: 4 }), false],
    [state({ q: "  " }), false],
    [state({ sort: "title_asc" }), false], // a sort orders, it does not filter
    [state({ q: "x" }), true],
    [state({ status: "pending" }), true],
    [state({ yearFrom: 1990 }), true],
    [state({ yearTo: 1990 }), true],
  ])("%j -> %s", (s, want) => {
    expect(hasFilters(s)).toBe(want);
  });
});

describe("describeFilters", () => {
  it.each([
    [state({ q: " matrix " }), "matching “matrix”"],
    [state({ yearFrom: 1990, yearTo: 1999 }), "from 1990 to 1999"],
    [state({ yearFrom: 1999, yearTo: 1999 }), "from 1999"],
    [state({ yearFrom: 1990 }), "from 1990 or later"],
    [state({ yearTo: 1990 }), "from 1990 or earlier"],
    [state({ status: "failed" }), "with status failed"],
    [
      state({ q: "x", yearFrom: 2000, yearTo: 2010, status: "ready", sort: "title_asc" }),
      "matching “x” from 2000 to 2010 with status ready",
    ],
  ])("%j -> %s", (s, want) => {
    expect(describeFilters(s)).toBe(want);
  });
});

describe("parseYear", () => {
  it.each([
    ["1880", 1880],
    ["2100", 2100],
    [" 1999 ", 1999],
    ["1879", null],
    ["2101", null],
    ["199", null],
    ["1999.0", null],
    ["1e3", null],
    ["", null],
    [null, null],
  ])("%j -> %j", (s, want) => {
    expect(parseYear(s)).toBe(want);
  });
});

describe("draft and submit", () => {
  it("a draft starts as the applied search", () => {
    expect(draftFrom(state({ q: "m", sort: "year_desc", yearFrom: 1990, status: "ready", page: 5 }))).toEqual({
      q: "m",
      sort: "year_desc",
      yearFrom: "1990",
      yearTo: "",
      status: "ready",
    });
  });

  it("submitting goes back to page 1 and keeps everything else", () => {
    const applied = state({ q: "m", sort: "year_desc", yearFrom: 1990, yearTo: 2000, status: "ready", page: 5 });
    expect(submitDraft(draftFrom(applied))).toEqual({ ok: true, state: { ...applied, page: 1 } });
  });

  it("empty year fields are no bound", () => {
    expect(submitDraft({ q: "", sort: "added_desc", yearFrom: " ", yearTo: "", status: null })).toEqual({
      ok: true,
      state: EMPTY_STATE,
    });
  });

  it.each([
    [{ yearFrom: "1879", yearTo: "" }, "From year must be a year between 1880 and 2100."],
    [{ yearFrom: "", yearTo: "21000" }, "To year must be a year between 1880 and 2100."],
    [{ yearFrom: "abcd", yearTo: "" }, "From year must be a year between 1880 and 2100."],
    [{ yearFrom: "2001", yearTo: "2000" }, "The From year must not be after the To year."],
  ])("refuses %j: the api would answer 400", (years, error) => {
    expect(submitDraft({ q: "", sort: "added_desc", status: null, ...years })).toEqual({ ok: false, error });
  });
});
