import { Fragment } from "react";
import { Link } from "react-router";

import { type Block, type Doc, type Inline, isInternal, plainText } from "../lib/markdown";

/**
 * A page from src/content/ (parsed at build time, src/lib/markdown.ts),
 * drawn with the design system. Only elements, never HTML from the file.
 * Headings below the title carry a "#" link to themselves (#privacy).
 */
export function MarkdownDoc({ doc }: { doc: Doc }) {
  return (
    <article className="flex flex-col gap-4 text-base leading-relaxed text-fg-muted">
      {doc.map((block, i) => (
        <BlockView key={i} block={block} />
      ))}
    </article>
  );
}

function BlockView({ block }: { block: Block }) {
  switch (block.t) {
    case "h":
      return <Heading level={block.level} id={block.id} c={block.c} />;
    case "p":
      return (
        <p>
          <Inlines c={block.c} />
        </p>
      );
    case "ul":
      return (
        <ul className="flex list-disc flex-col gap-2 pl-5 marker:text-fg-subtle">
          {block.items.map((item, i) => (
            <li key={i}>
              <Inlines c={item} />
            </li>
          ))}
        </ul>
      );
  }
}

function Heading({ level, id, c }: { level: 1 | 2 | 3; id: string; c: Inline[] }) {
  if (level === 1) {
    return (
      <h1 id={id} className="mb-2 scroll-mt-24 font-display text-3xl font-bold text-fg sm:text-4xl">
        <Inlines c={c} />
      </h1>
    );
  }
  const Tag = level === 2 ? "h2" : "h3";
  const size = level === 2 ? "mt-8 text-2xl font-bold" : "mt-4 text-lg font-semibold";
  return (
    <Tag id={id} className={`group scroll-mt-24 font-display text-fg ${size}`}>
      <Inlines c={c} />
      <a
        href={`#${id}`}
        aria-label={`Link to the section ${plainText(c)}`}
        className="ml-2 text-fg-subtle opacity-0 transition-opacity duration-150 group-hover:opacity-100 hover:text-accent-soft focus-visible:opacity-100"
      >
        #
      </a>
    </Tag>
  );
}

function Inlines({ c }: { c: Inline[] }) {
  return (
    <>
      {c.map((x, i) => (
        <Fragment key={i}>
          <InlineView x={x} />
        </Fragment>
      ))}
    </>
  );
}

const linkClass = "font-medium text-accent-soft underline decoration-accent-soft/40 underline-offset-2 hover:decoration-accent-soft";

function InlineView({ x }: { x: Inline }) {
  switch (x.t) {
    case "text":
      return x.v;
    case "strong":
      return (
        <strong className="font-semibold text-fg">
          <Inlines c={x.c} />
        </strong>
      );
    case "em":
      return (
        <em>
          <Inlines c={x.c} />
        </em>
      );
    case "code":
      return <code className="rounded bg-overlay px-1.5 py-0.5 text-sm text-fg">{x.v}</code>;
    case "link":
      return isInternal(x.href) ? (
        <Link to={x.href} className={linkClass}>
          <Inlines c={x.c} />
        </Link>
      ) : (
        <a href={x.href} rel="noreferrer" className={linkClass}>
          <Inlines c={x.c} />
        </a>
      );
  }
}
