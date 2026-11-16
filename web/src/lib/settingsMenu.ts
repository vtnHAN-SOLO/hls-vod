// The player's settings menu: closed, or open on the main page or one of its
// sub-pages. Pure transitions, so what Escape and Back do is tested.

export type SettingsPage = "main" | "quality" | "subtitles" | "size" | "speed";

export type MenuState = { open: false } | { open: true; page: SettingsPage };

export const CLOSED: MenuState = { open: false };

export type MenuEvent =
  | { kind: "toggle" } // the gear button
  | { kind: "go"; page: Exclude<SettingsPage, "main"> } // a main-page row
  | { kind: "back" } // the back arrow of a sub-page
  | { kind: "escape" }
  | { kind: "chosen" } // a choice was made on a sub-page
  | { kind: "close" }; // a click outside, the controls hiding

/**
 * toggle opens the main page or closes; a row opens its page; back and
 * chosen return to the main page; Escape steps back one level (sub-page →
 * main → closed); close closes from anywhere.
 */
export function menuReduce(s: MenuState, e: MenuEvent): MenuState {
  switch (e.kind) {
    case "toggle":
      return s.open ? CLOSED : { open: true, page: "main" };
    case "go":
      return s.open ? { open: true, page: e.page } : s;
    case "back":
    case "chosen":
      return s.open ? { open: true, page: "main" } : s;
    case "escape":
      if (!s.open) return s;
      return s.page === "main" ? CLOSED : { open: true, page: "main" };
    case "close":
      return CLOSED;
  }
}

export const SIZE_LABELS = { s: "Small", m: "Medium", l: "Large" } as const;

/** "Normal" for 1, else "1.5×". */
export function speedLabel(speed: number): string {
  return speed === 1 ? "Normal" : `${String(speed)}×`;
}

/** The quality row: "Auto (720p)" while automatic, else the chosen height. */
export function qualityLabel(selectedHeight: number | null, playingHeight: number | null): string {
  if (selectedHeight !== null) return `${String(selectedHeight)}p`;
  return playingHeight ? `Auto (${String(playingHeight)}p)` : "Auto";
}
