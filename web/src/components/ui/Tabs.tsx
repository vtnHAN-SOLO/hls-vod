import { useId, useRef } from "react";

import { moveIndex } from "../../lib/keys";

export interface Tab<T extends string> {
  id: T;
  label: string;
}

/**
 * A tab strip (WAI-ARIA tabs, automatic activation): one tab in the Tab
 * order, the selected one; arrows, Home and End move and select. The panel
 * is the caller's; give it id={tabPanelId(baseId)} and aria-labelledby from
 * tabId(baseId, selected).
 */
export function Tabs<T extends string>({
  tabs,
  selected,
  onSelect,
  label,
  baseId,
  className = "",
}: {
  tabs: readonly Tab<T>[];
  selected: T;
  onSelect: (id: T) => void;
  label: string;
  baseId: string;
  className?: string;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const current = Math.max(
    0,
    tabs.findIndex((t) => t.id === selected),
  );
  return (
    <div role="tablist" aria-label={label} className={`inline-flex rounded-lg bg-raised p-1 ${className}`}>
      {tabs.map((t, i) => {
        const active = t.id === selected;
        return (
          <button
            key={t.id}
            ref={(el) => {
              refs.current[i] = el;
            }}
            type="button"
            role="tab"
            id={tabId(baseId, t.id)}
            aria-selected={active}
            aria-controls={tabPanelId(baseId)}
            tabIndex={active ? 0 : -1}
            onClick={() => {
              onSelect(t.id);
            }}
            onKeyDown={(e) => {
              const to = moveIndex(e.key, current, tabs.length, { orientation: "horizontal", wrap: true });
              const next = to === null ? undefined : tabs[to];
              if (next === undefined) return;
              e.preventDefault();
              onSelect(next.id);
              refs.current[to ?? 0]?.focus();
            }}
            className={`rounded-md px-3 py-1.5 text-xs font-semibold transition-colors duration-150 ${
              active ? "bg-overlay-hover text-fg" : "text-fg-muted hover:text-fg"
            }`}
          >
            {t.label}
          </button>
        );
      })}
    </div>
  );
}

export function tabId(baseId: string, id: string): string {
  return `${baseId}-tab-${id}`;
}

export function tabPanelId(baseId: string): string {
  return `${baseId}-panel`;
}

/** A stable id prefix for one Tabs + panel pair. */
export const useTabsId = useId;
