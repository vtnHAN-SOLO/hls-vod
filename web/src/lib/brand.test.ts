import { describe, expect, it } from "vitest";

import indexHtml from "../../index.html?raw";
import { manifestFor, resolveBrand, tokenValue, wordmarkParts } from "./brand";

describe("resolveBrand", () => {
  it("defaults to the neutral name with VOD accented", () => {
    expect(resolveBrand({})).toEqual({ name: "HLS VOD", accent: "VOD" });
    expect(resolveBrand({ VITE_BRAND_NAME: "  ", VITE_BRAND_ACCENT: "" })).toEqual({ name: "HLS VOD", accent: "VOD" });
  });

  it("takes a deployment's name and accent", () => {
    expect(resolveBrand({ VITE_BRAND_NAME: "Moviebox", VITE_BRAND_ACCENT: "box" })).toEqual({
      name: "Moviebox",
      accent: "box",
    });
  });

  it("accents nothing for a custom name without an accent", () => {
    expect(resolveBrand({ VITE_BRAND_NAME: "Moviebox" })).toEqual({ name: "Moviebox", accent: "" });
  });

  it.each([
    [{ VITE_BRAND_NAME: "Movie<script>" }],
    [{ VITE_BRAND_NAME: 'A "quoted" name' }],
    [{ VITE_BRAND_NAME: "x".repeat(41) }],
    [{ VITE_BRAND_NAME: "Moviebox", VITE_BRAND_ACCENT: "Movie" }], // not a suffix
    [{ VITE_BRAND_NAME: "Moviebox", VITE_BRAND_ACCENT: "Moviebox" }], // the whole name
  ])("refuses %o", (env) => {
    expect(() => resolveBrand(env)).toThrow();
  });
});

describe("wordmarkParts", () => {
  it("splits off the accented tail", () => {
    expect(wordmarkParts({ name: "HLS VOD", accent: "VOD" })).toEqual({ head: "HLS ", accent: "VOD" });
    expect(wordmarkParts({ name: "Moviebox", accent: "box" })).toEqual({ head: "Movie", accent: "box" });
    expect(wordmarkParts({ name: "Moviebox", accent: "" })).toEqual({ head: "Moviebox", accent: "" });
  });
});

describe("manifestFor", () => {
  it("names the app and takes the token colours", () => {
    const m = manifestFor({ name: "Moviebox", accent: "box" }, { background: "#000001", theme: "#000002" });
    expect(m).toMatchObject({ name: "Moviebox", short_name: "Moviebox", background_color: "#000001", theme_color: "#000002" });
    expect(m.icons.map((i) => i.sizes)).toEqual(["192x192", "512x512"]);
  });

  it("reads a token from the stylesheet", () => {
    expect(tokenValue(":root { --color-bg:  #010203 ; --color-fg: #fff; }", "color-bg")).toBe("#010203");
    expect(() => tokenValue("", "color-bg")).toThrow();
  });
});

describe("index.html", () => {
  it("takes its title from the brand and links the generated manifest", () => {
    expect(indexHtml).toContain("<title>%VITE_BRAND_NAME%</title>");
    expect(indexHtml).toContain('<link rel="manifest" href="/manifest.webmanifest" />');
  });
});
