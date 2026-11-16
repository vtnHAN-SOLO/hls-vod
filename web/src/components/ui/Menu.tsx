import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { Link } from "react-router";

import { moveIndex } from "../../lib/keys";

export type MenuItem =
  | { kind: "link"; label: string; to: string; icon?: ReactNode }
  | { kind: "action"; label: string; onSelect: () => void; icon?: ReactNode };

/**
 * A dropdown menu (WAI-ARIA menu button): the trigger opens it and focuses
 * the first item; arrows, Home and End move; Escape, Tab or a click outside
 * close it, Escape returning focus to the trigger.
 */
export function Menu({
  label,
  trigger,
  items,
  header,
  align = "end",
}: {
  /** The trigger's accessible name. */
  label: string;
  trigger: ReactNode;
  items: MenuItem[];
  /** Shown above the items, not focusable (who is signed in). */
  header?: ReactNode;
  align?: "start" | "end";
}) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const itemRefs = useRef<(HTMLElement | null)[]>([]);

  const focusItem = (i: number) => {
    itemRefs.current[i]?.focus();
  };
  const close = (restoreFocus: boolean) => {
    setOpen(false);
    if (restoreFocus) button.current?.focus();
  };

  useEffect(() => {
    if (!open) return;
    focusItem(0);
    const onDown = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onDown);
    return () => {
      document.removeEventListener("pointerdown", onDown);
    };
  }, [open]);

  const onItemKey = (e: React.KeyboardEvent, i: number) => {
    if (e.key === "Escape") {
      e.preventDefault();
      close(true);
      return;
    }
    if (e.key === "Tab") {
      setOpen(false);
      return;
    }
    const to = moveIndex(e.key, i, items.length, { orientation: "vertical", wrap: true });
    if (to !== null) {
      e.preventDefault();
      focusItem(to);
    }
  };

  const itemClass =
    "flex w-full items-center gap-3 rounded-md px-3 py-2 text-left text-sm text-fg-muted transition-colors duration-150 hover:bg-overlay hover:text-fg focus-visible:bg-overlay focus-visible:text-fg focus-visible:outline-none";

  return (
    <div ref={root} className="relative">
      <button
        ref={button}
        type="button"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={() => {
          setOpen((o) => !o);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            setOpen(true);
          }
        }}
        className="rounded-full"
      >
        {trigger}
      </button>
      {open && (
        <div
          id={id}
          role="menu"
          aria-label={label}
          className={`absolute top-full z-50 mt-2 w-56 rounded-xl border border-line bg-surface p-1.5 shadow-2xl shadow-scrim/60 ${
            align === "end" ? "right-0" : "left-0"
          }`}
        >
          {header && <div className="border-b border-line px-3 pt-1.5 pb-2.5 mb-1">{header}</div>}
          {items.map((item, i) =>
            item.kind === "link" ? (
              <Link
                key={item.label}
                ref={(el) => {
                  itemRefs.current[i] = el;
                }}
                role="menuitem"
                tabIndex={-1}
                to={item.to}
                onClick={() => {
                  setOpen(false);
                }}
                onKeyDown={(e) => {
                  onItemKey(e, i);
                }}
                className={itemClass}
              >
                {item.icon}
                {item.label}
              </Link>
            ) : (
              <button
                key={item.label}
                ref={(el) => {
                  itemRefs.current[i] = el;
                }}
                type="button"
                role="menuitem"
                tabIndex={-1}
                onClick={() => {
                  setOpen(false);
                  item.onSelect();
                }}
                onKeyDown={(e) => {
                  onItemKey(e, i);
                }}
                className={itemClass}
              >
                {item.icon}
                {item.label}
              </button>
            ),
          )}
        </div>
      )}
    </div>
  );
}
