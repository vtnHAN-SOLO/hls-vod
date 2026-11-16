// The design system's rule: colours come from the tokens in src/index.css,
// never from Tailwind's default palette (switched off there) or a hex value
// in a component. A class like bg-neutral-800 would silently render nothing,
// so this test is what catches it.
import { describe, expect, it } from "vitest";

// Every source file of the SPA as text (Vite's glob import, so no Node APIs),
// tests and the generated API types left out.
const files = Object.entries(
  import.meta.glob<string>(["../**/*.{ts,tsx}", "!../**/*.test.ts", "!../api/schema.ts"], {
    query: "?raw",
    import: "default",
    eager: true,
  }),
);

const PALETTE =
  /\b(?:bg|text|border|ring|outline|from|via|to|fill|stroke|divide|decoration|shadow|accent|caret|placeholder)-(?:white|black|(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d{2,3})\b/;
const HEX = /#[0-9a-fA-F]{3,8}\b/;

describe("design tokens", () => {
  it("finds the components", () => {
    expect(files.length).toBeGreaterThan(20);
  });

  it.each(files)("%s uses only token colours", (_name, text) => {
    const lines = text.split("\n");
    const bad = lines
      .map((line, i) => ({ line: line.trim(), n: i + 1 }))
      .filter(({ line }) => PALETTE.test(line) || (HEX.test(line) && !line.startsWith("//") && !line.startsWith("*")));
    expect(bad).toEqual([]);
  });
});
