// The small Markdown subset of the site's own pages (src/content/*.md),
// turned into a tree at build time by vite.config.ts, so the bundle carries
// data, not a Markdown library, and React renders it without ever setting
// HTML. Pure, so the rules are unit tested; vite.config.ts and the tests
// both use it.
//
// Supported, and nothing else:
//   # / ## / ### headings, optionally ending in {#id} (else the id is a slug)
//   paragraphs (lines joined), "- " lists (an indented line continues an item)
//   **strong**, *emphasis*, `code`, [text](href)
//   {{brand}}: the product's name (VITE_BRAND_NAME)
// Raw HTML is refused, except section markers: a comment line that holds
// only "<!-- name:start -->" or "<!-- name:end -->" (lowercase name). They
// are dropped and the text between them is kept, so a copy of the file can
// cut a section out by its markers.

export type Inline =
  | { t: "text"; v: string }
  | { t: "strong"; c: Inline[] }
  | { t: "em"; c: Inline[] }
  | { t: "code"; v: string }
  | { t: "link"; href: string; c: Inline[] };

export type Block =
  | { t: "h"; level: 1 | 2 | 3; id: string; c: Inline[] }
  | { t: "p"; c: Inline[] }
  | { t: "ul"; items: Inline[][] };

export type Doc = Block[];

export class MarkdownError extends Error {}

const MARKER = /^\s*<!--\s*[a-z][a-z-]*:(?:start|end)\s*-->\s*$/;
// A tag, a comment, a doctype or a processing instruction: "a < b" stays text.
const RAW_HTML = /<[A-Za-z!?/]/;
const PLACEHOLDER = /\{\{\s*([^}]*?)\s*\}\}/g;

/** Replaces {{brand}}; any other {{…}} is a mistake and throws. */
export function substitute(src: string, vars: Record<string, string>): string {
  return src.replace(PLACEHOLDER, (_m, name: string) => {
    const v = vars[name];
    if (v === undefined) throw new MarkdownError(`unknown placeholder {{${name}}}`);
    return v;
  });
}

/** The id a heading gets without an explicit {#id}: lowercase words joined by "-". */
export function slug(text: string): string {
  return text
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

/** Where a link may point: the site's own paths and fragments, https and mailto. */
export function safeHref(href: string): boolean {
  return /^(\/(?!\/)|#|https:\/\/|mailto:)/.test(href) && !/\s/.test(href);
}

export function isInternal(href: string): boolean {
  return href.startsWith("/");
}

/** Parses the subset above; throws MarkdownError with the line number on anything else. */
export function parse(src: string): Doc {
  const doc: Doc = [];
  const ids = new Set<string>();
  let para: string[] = [];
  let list: string[] | null = null;
  let paraLine = 0;

  const flush = () => {
    if (para.length > 0) doc.push({ t: "p", c: inline(para.join(" "), paraLine) });
    para = [];
    if (list) doc.push({ t: "ul", items: list.map((item) => inline(item, paraLine)) });
    list = null;
  };

  src.split(/\r?\n/).forEach((raw, i) => {
    const n = i + 1;
    if (MARKER.test(raw)) {
      flush();
      return;
    }
    if (RAW_HTML.test(raw)) throw new MarkdownError(`line ${String(n)}: raw HTML is not allowed`);
    const line = raw.trimEnd();
    if (line.trim() === "") {
      flush();
      return;
    }
    const h = /^(#{1,6})\s+(.*)$/.exec(line);
    if (h) {
      flush();
      const level = (h[1] ?? "").length;
      if (level > 3) throw new MarkdownError(`line ${String(n)}: headings go down to ### only`);
      let text = (h[2] ?? "").trim();
      let id: string;
      const explicit = /\s*\{#([a-z0-9-]+)\}$/.exec(text);
      if (explicit) {
        id = explicit[1] ?? "";
        text = text.slice(0, explicit.index).trim();
      } else {
        id = slug(text);
      }
      if (text === "" || id === "") throw new MarkdownError(`line ${String(n)}: a heading needs text`);
      if (ids.has(id)) throw new MarkdownError(`line ${String(n)}: heading id #${id} used twice`);
      ids.add(id);
      doc.push({ t: "h", level: level as 1 | 2 | 3, id, c: inline(text, n) });
      return;
    }
    const item = /^[-*]\s+(.*)$/.exec(line);
    if (item) {
      if (para.length > 0) {
        doc.push({ t: "p", c: inline(para.join(" "), paraLine) });
        para = [];
      }
      list ??= [];
      list.push(item[1] ?? "");
      paraLine = n;
      return;
    }
    if (list && /^\s+\S/.test(line)) {
      list[list.length - 1] = `${list.at(-1) ?? ""} ${line.trim()}`;
      return;
    }
    if (list) flush();
    if (/^\s{4}|^\t|^```|^>|^\d+\.\s|^\|/.test(line)) {
      throw new MarkdownError(`line ${String(n)}: not in the supported subset (code blocks, quotes, numbered lists, tables)`);
    }
    if (para.length === 0) paraLine = n;
    para.push(line.trim());
  });
  flush();
  return doc;
}

/** The inline subset; markers must be balanced or the line throws. */
export function inline(text: string, line = 0): Inline[] {
  const out: Inline[] = [];
  let i = 0;
  let buf = "";
  const err = (what: string) => new MarkdownError(`line ${String(line)}: ${what}`);
  const pushText = () => {
    if (buf !== "") out.push({ t: "text", v: buf });
    buf = "";
  };
  while (i < text.length) {
    const rest = text.slice(i);
    if (rest.startsWith("`")) {
      const end = text.indexOf("`", i + 1);
      if (end < 0) throw err("unclosed `");
      pushText();
      out.push({ t: "code", v: text.slice(i + 1, end) });
      i = end + 1;
    } else if (rest.startsWith("**")) {
      const end = text.indexOf("**", i + 2);
      if (end < 0) throw err("unclosed **");
      pushText();
      out.push({ t: "strong", c: inline(text.slice(i + 2, end), line) });
      i = end + 2;
    } else if (rest.startsWith("*")) {
      const end = text.indexOf("*", i + 1);
      if (end < 0) throw err("unclosed *");
      pushText();
      out.push({ t: "em", c: inline(text.slice(i + 1, end), line) });
      i = end + 1;
    } else if (rest.startsWith("[")) {
      const m = /^\[([^\]]+)\]\(([^)\s]*)\)/.exec(rest);
      if (!m) throw err("a [ that is not a link [text](href)");
      const href = m[2] ?? "";
      if (!safeHref(href)) throw err(`link to ${JSON.stringify(href)}: only /paths, #fragments, https: and mailto:`);
      pushText();
      out.push({ t: "link", href, c: inline(m[1] ?? "", line) });
      i += m[0].length;
    } else {
      buf += text.charAt(i);
      i++;
    }
  }
  pushText();
  return out;
}

/** A tree's plain text (headings' ids, tests). */
export function plainText(c: Inline[]): string {
  return c.map((x) => (x.t === "text" || x.t === "code" ? x.v : plainText(x.c))).join("");
}
