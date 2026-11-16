import { describe, expect, it } from "vitest";

import {
  checkPosterDimensions,
  checkPosterFile,
  cropFractions,
  MAX_POSTER_BYTES,
  percent,
  type Poster,
  posterSrc,
  posterSrcSet,
  publishBlockers,
  publishHint,
} from "./poster";

const url = (w: number) => `/api/v1/posters/m/${String(w)}/h.jpg?exp=1&sig=s`;
const poster = (...widths: number[]): Poster => ({
  hash: "h",
  variants: widths.map((w) => ({ width: w as 300 | 600 | 1000, height: ((w * 3) / 2) as 450 | 900 | 1500, url: url(w) })),
});

describe("srcset and src", () => {
  it("lists every variant with its width", () => {
    expect(posterSrcSet(poster(300, 600, 1000))).toBe(`${url(300)} 300w, ${url(600)} 600w, ${url(1000)} 1000w`);
  });
  it("starts from 600, else the largest", () => {
    expect(posterSrc(poster(300, 600, 1000))).toBe(url(600));
    expect(posterSrc(poster(300))).toBe(url(300));
  });
});

describe("checkPosterFile", () => {
  it.each([
    [{ size: 1000, type: "image/jpeg" }, null],
    [{ size: 1000, type: "image/png" }, null],
    [{ size: 1000, type: "image/webp" }, null],
    [{ size: 1000, type: "image/gif" }, "Choose a JPEG, PNG or WebP image."],
    [{ size: 1000, type: "" }, "Choose a JPEG, PNG or WebP image."],
    [{ size: MAX_POSTER_BYTES + 1, type: "image/png" }, "The image is larger than 10 MB."],
    [{ size: MAX_POSTER_BYTES, type: "image/png" }, null],
    [{ size: 0, type: "image/png" }, "The file is empty."],
  ])("%o", (f, want) => {
    expect(checkPosterFile(f)).toBe(want);
  });
});

describe("checkPosterDimensions", () => {
  it("needs at least 300x450 and at most 25 MP", () => {
    expect(checkPosterDimensions(300, 450)).toBeNull();
    expect(checkPosterDimensions(299, 1000)).toMatch(/at least 300×450/);
    expect(checkPosterDimensions(1000, 449)).toMatch(/at least 300×450/);
    expect(checkPosterDimensions(5000, 5000)).toBeNull();
    expect(checkPosterDimensions(5001, 5000)).toMatch(/25 megapixels/);
  });
});

describe("cropFractions", () => {
  it("keeps the centred 2:3 part, as the api does", () => {
    const wide = cropFractions(2400, 1600); // 1066x1599 from x=667
    expect(wide.w * 2400).toBe(1066);
    expect(wide.h * 1600).toBe(1599);
    expect(Math.round(wide.x * 2400)).toBe(667);
    expect(wide.y).toBe(0);
    const tall = cropFractions(1000, 3000);
    expect(tall).toEqual({ x: 0, y: 750 / 3000, w: 1, h: 0.5 });
    const exact = cropFractions(600, 900);
    expect(exact).toEqual({ x: 0, y: 0, w: 1, h: 1 });
  });
});

describe("percent", () => {
  it("rounds and clamps", () => {
    expect(percent(0, 0)).toBe(0);
    expect(percent(1, 3)).toBe(33);
    expect(percent(5, 4)).toBe(100);
  });
});

describe("publishing", () => {
  const ready = { status: "ready" as const, poster: poster(300), overview: "Text" };
  it("lists what is missing, in the api's order", () => {
    expect(publishBlockers(ready)).toEqual([]);
    expect(publishBlockers({ ...ready, poster: null })).toEqual(["missing_poster"]);
    expect(publishBlockers({ ...ready, overview: "   " })).toEqual(["missing_overview"]);
    expect(publishBlockers({ status: "processing", poster: null, overview: null })).toEqual([
      "not_ready",
      "missing_poster",
      "missing_overview",
    ]);
  });
  it("explains a disabled Publish button", () => {
    expect(publishHint(ready)).toBeNull();
    expect(publishHint({ ...ready, poster: null, overview: null })).toBe("Upload a poster first. Write a description first.");
  });
});
