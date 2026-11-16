import { type ReactNode, useCallback, useEffect, useId, useRef, useState } from "react";

import { moveIndex } from "../../lib/keys";
import { ChevronLeftIcon, ChevronRightIcon } from "./Icons";
import { useReducedMotion } from "./hooks";

/**
 * A titled horizontal row of cards. It scrolls by touch or trackpad (with
 * scroll-snap), by the arrow buttons (a page at a time), and by keyboard:
 * Tab reaches each card in order, and with focus on a card the Left and
 * Right arrows (Home, End) move to the neighbouring card, which the browser
 * scrolls into view.
 */
export function Row({
  title,
  icon,
  action,
  children,
  className = "",
}: {
  title: string;
  icon?: ReactNode;
  /** A link at the end of the title line ("See all"). */
  action?: ReactNode;
  /** <RowItem>s. */
  children: ReactNode;
  className?: string;
}) {
  const headingId = useId();
  const list = useRef<HTMLUListElement>(null);
  const reduced = useReducedMotion();
  const [edges, setEdges] = useState({ start: true, end: true });

  const measure = useCallback(() => {
    const el = list.current;
    if (!el) return;
    setEdges({
      start: el.scrollLeft <= 1,
      end: el.scrollLeft + el.clientWidth >= el.scrollWidth - 1,
    });
  }, []);

  useEffect(() => {
    const el = list.current;
    if (!el) return;
    measure();
    el.addEventListener("scroll", measure, { passive: true });
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => {
      el.removeEventListener("scroll", measure);
      ro.disconnect();
    };
  }, [measure]);

  const page = (dir: 1 | -1) => {
    const el = list.current;
    if (!el) return;
    el.scrollBy({ left: dir * el.clientWidth * 0.9, behavior: reduced ? "auto" : "smooth" });
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLUListElement>) => {
    const items = Array.from(e.currentTarget.children);
    const at = items.findIndex((li) => li.contains(document.activeElement));
    if (at < 0) return;
    const to = moveIndex(e.key, at, items.length, { orientation: "horizontal", wrap: false });
    if (to === null || to === at) return;
    const target = items[to]?.querySelector<HTMLElement>("a, button");
    if (!target) return;
    e.preventDefault();
    target.focus();
  };

  return (
    <section aria-labelledby={headingId} className={`flex flex-col gap-3 ${className}`}>
      <div className="flex items-center gap-3">
        <h2 id={headingId} className="flex items-center gap-2 text-lg font-semibold text-fg sm:text-xl">
          {icon && <span className="text-accent-soft">{icon}</span>}
          {title}
        </h2>
        {action && <div className="text-sm">{action}</div>}
        <div className="ml-auto hidden gap-1.5 sm:flex">
          <ArrowButton label={`Scroll ${title} back`} disabled={edges.start} onClick={() => { page(-1); }}>
            <ChevronLeftIcon size={18} />
          </ArrowButton>
          <ArrowButton label={`Scroll ${title} forward`} disabled={edges.end} onClick={() => { page(1); }}>
            <ChevronRightIcon size={18} />
          </ArrowButton>
        </div>
      </div>
      <ul
        ref={list}
        onKeyDown={onKeyDown}
        className="scrollbar-none -mx-4 flex snap-x snap-mandatory scroll-px-4 gap-3 overflow-x-auto px-4 pt-1 pb-3 sm:-mx-6 sm:scroll-px-6 sm:px-6"
      >
        {children}
      </ul>
    </section>
  );
}

/**
 * One slot of a Row; its width scales with the viewport. portrait (poster
 * cards): ~3 to 8 show; landscape (backdrop cards): ~2 to 6; wide: ~1 to 3.
 * POSTER_SIZES.row follows the portrait widths.
 */
export function RowItem({ children, shape = "portrait" }: { children: ReactNode; shape?: "portrait" | "landscape" | "wide" }) {
  const width = {
    portrait: "w-[34%] sm:w-[23%] md:w-[18%] lg:w-[14%] 2xl:w-[12%]",
    landscape: "w-[44%] sm:w-[30%] md:w-[23%] lg:w-[18.5%] 2xl:w-[15.5%]",
    wide: "w-[78%] sm:w-[45%] lg:w-[31%]",
  }[shape];
  return <li className={`shrink-0 snap-start ${width}`}>{children}</li>;
}

function ArrowButton({
  label,
  disabled,
  onClick,
  children,
}: {
  label: string;
  disabled: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      disabled={disabled}
      onClick={onClick}
      // Mouse users get the buttons; keyboard users move card to card, so the
      // buttons stay out of the Tab order.
      tabIndex={-1}
      className="grid size-8 place-items-center rounded-full border border-line bg-surface text-fg-muted transition-colors duration-150 hover:bg-overlay hover:text-fg disabled:opacity-30"
    >
      {children}
    </button>
  );
}
