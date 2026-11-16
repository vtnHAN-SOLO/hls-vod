// The About page as the build makes it: src/content/about.md through the
// markdown-pages plugin of vite.config.ts (vitest runs the same config).
import { describe, expect, it } from "vitest";

import about from "../content/about.md";
import { brand } from "../brand";
import { type Block, plainText } from "./markdown";

const text = JSON.stringify(about);
const headings = about.filter((b): b is Extract<Block, { t: "h" }> => b.t === "h");

describe("about.md", () => {
  it("has the title and the sections", () => {
    expect(headings.map((h) => h.id)).toEqual(
      expect.arrayContaining(["name", "privacy", "what-is-stored", "cookies-and-storage", "outside-services"]),
    );
    expect(headings[0]?.level).toBe(1);
  });

  it("has the #privacy anchor the footer links to", () => {
    expect(headings.find((h) => h.id === "privacy")?.level).toBe(2);
  });

  it("substitutes the brand name and leaves no placeholder", () => {
    expect(plainText(headings[0]?.c ?? [])).toBe(`About ${brand.name}`);
    expect(text).not.toContain("{{");
  });

  it("names what is stored and every outside service", () => {
    for (const word of ["username", "email", "argon2id", "Watch progress", "Sessions", "login cookie", "Resend", "Cloudflare Turnstile", "Google"]) {
      expect(text).toContain(word);
    }
  });
});

