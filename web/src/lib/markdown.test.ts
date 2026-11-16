import { describe, expect, it } from "vitest";

import { inline, MarkdownError, parse, safeHref, slug, substitute } from "./markdown";

describe("substitute", () => {
  it("replaces {{brand}}, spaces allowed", () => {
    expect(substitute("About {{brand}}, {{ brand }}.", { brand: "Example" })).toBe("About Example, Example.");
  });

  it("refuses an unknown placeholder", () => {
    expect(() => substitute("{{site}}", { brand: "Example" })).toThrow(MarkdownError);
  });
});

describe("parse", () => {
  it("parses headings, paragraphs and lists", () => {
    expect(parse("# Title\n\nOne\ntwo.\n\n- a\n  more\n- **b**\n\n## Privacy {#privacy}\n### What is stored")).toEqual([
      { t: "h", level: 1, id: "title", c: [{ t: "text", v: "Title" }] },
      { t: "p", c: [{ t: "text", v: "One two." }] },
      { t: "ul", items: [[{ t: "text", v: "a more" }], [{ t: "strong", c: [{ t: "text", v: "b" }] }]] },
      { t: "h", level: 2, id: "privacy", c: [{ t: "text", v: "Privacy" }] },
      { t: "h", level: 3, id: "what-is-stored", c: [{ t: "text", v: "What is stored" }] },
    ]);
  });

  it("drops section markers and keeps what is between them", () => {
    expect(parse("Public.\n<!-- extra:start -->\nKept.\n  <!--  extra:end  -->\nMore.")).toEqual([
      { t: "p", c: [{ t: "text", v: "Public." }] },
      { t: "p", c: [{ t: "text", v: "Kept." }] },
      { t: "p", c: [{ t: "text", v: "More." }] },
    ]);
  });

  it.each([
    ["a tag", "Hello <b>you</b>"],
    ["a script", "<script>alert(1)</script>"],
    ["an ordinary comment", "<!-- note -->"],
    ["a marker that is not alone on its line", "text <!-- extra:start -->"],
    ["a marker with a capital", "<!-- Extra:start -->"],
    ["an image tag", '<img src="x">'],
  ])("refuses raw HTML: %s", (_name, src) => {
    expect(() => parse(src)).toThrow(/raw HTML/);
  });

  it("keeps a plain < as text", () => {
    expect(parse("1 < 2")).toEqual([{ t: "p", c: [{ t: "text", v: "1 < 2" }] }]);
  });

  it.each([
    ["a heading too deep", "#### deep"],
    ["a duplicate id", "## A\n## A"],
    ["a code block", "```\nx\n```"],
    ["a quote", "> quoted"],
    ["a numbered list", "1. one"],
  ])("refuses %s", (_name, src) => {
    expect(() => parse(src)).toThrow(MarkdownError);
  });
});

describe("inline", () => {
  it("parses strong, emphasis, code and links", () => {
    expect(inline("**a** *b* `c` [d](/about#privacy)")).toEqual([
      { t: "strong", c: [{ t: "text", v: "a" }] },
      { t: "text", v: " " },
      { t: "em", c: [{ t: "text", v: "b" }] },
      { t: "text", v: " " },
      { t: "code", v: "c" },
      { t: "text", v: " " },
      { t: "link", href: "/about#privacy", c: [{ t: "text", v: "d" }] },
    ]);
  });

  it.each(["**open", "*open", "`open", "[text]"])("refuses an unbalanced %s", (src) => {
    expect(() => inline(src)).toThrow(MarkdownError);
  });

  it.each(["javascript:alert(1)", "//evil.example", "http://plain.example", "data:text/html,x"])("refuses a link to %s", (href) => {
    expect(() => inline(`[x](${href})`)).toThrow(/only \/paths/);
  });
});

describe("safeHref and slug", () => {
  it.each([
    ["/about", true],
    ["#privacy", true],
    ["https://example.com", true],
    ["mailto:someone@example.com", true],
    ["//example.com", false],
    ["javascript:x", false],
  ])("%s → %s", (href, ok) => {
    expect(safeHref(href)).toBe(ok);
  });

  it("slugs headings", () => {
    expect(slug("The name & the logo")).toBe("the-name-the-logo");
    expect(slug("Café")).toBe("cafe");
  });
});
