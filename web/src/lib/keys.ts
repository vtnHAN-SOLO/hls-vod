/**
 * Keyboard movement inside a group of items (tabs, a row of cards, a menu):
 * which item a key moves focus to. Pure, so the rules are unit tested; the
 * components only call focus() on the result.
 */

export type Orientation = "horizontal" | "vertical";

export interface MoveOptions {
  orientation: Orientation;
  /** Past the last item, back to the first (tabs, menus); a row stops. */
  wrap: boolean;
}

/**
 * The index key moves to from index among count items, or null when the key
 * means nothing here (the browser then handles it: Tab, Enter, letters…).
 * Home and End go to the ends. A key that would leave the group without
 * wrapping stays on the end item.
 */
export function moveIndex(key: string, index: number, count: number, opts: MoveOptions): number | null {
  if (count <= 0) return null;
  const prev = opts.orientation === "horizontal" ? "ArrowLeft" : "ArrowUp";
  const next = opts.orientation === "horizontal" ? "ArrowRight" : "ArrowDown";
  const last = count - 1;
  const at = Math.min(Math.max(index, 0), last);
  switch (key) {
    case "Home":
      return 0;
    case "End":
      return last;
    case prev:
      if (at > 0) return at - 1;
      return opts.wrap ? last : 0;
    case next:
      if (at < last) return at + 1;
      return opts.wrap ? 0 : last;
    default:
      return null;
  }
}
