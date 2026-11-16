// The player's keyboard map. Pure: which action a key press means, so the
// rules (and what is ignored) are unit tested; the player only carries
// actions out.

export const SKIP_SEC = 5;
export const VOLUME_STEP = 0.1;

export type PlayerAction =
  | { kind: "toggle-play" }
  | { kind: "skip"; seconds: number }
  | { kind: "volume"; delta: number }
  | { kind: "toggle-mute" }
  | { kind: "toggle-fullscreen" }
  | { kind: "toggle-subtitles" }
  | { kind: "jump"; fraction: number }
  | { kind: "escape" };

export interface KeyInput {
  key: string;
  ctrlKey?: boolean;
  metaKey?: boolean;
  altKey?: boolean;
  /** The element the key went to. */
  target: { tagName: string; isContentEditable?: boolean; getAttribute?: (name: string) => string | null } | null;
}

/** Elements that take typed text: their keys are never the player's. */
function typing(target: KeyInput["target"]): boolean {
  if (!target) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName.toUpperCase();
  if (tag === "TEXTAREA" || tag === "SELECT") return true;
  if (tag !== "INPUT") return false;
  // A range input (the volume slider) is not typing: its arrows are its own,
  // handled before the player sees them.
  const type = target.getAttribute?.("type")?.toLowerCase() ?? "text";
  return type !== "range" && type !== "checkbox" && type !== "radio" && type !== "button";
}

/**
 * What a key means: space/k play or pause, ←/→ 5 s, ↑/↓ volume 10 %, m mute,
 * f fullscreen, c subtitles, 0–9 jump to that tenth, Esc closes menus.
 * null for anything else, while typing, and with Ctrl, Meta or Alt (the
 * browser's and the system's shortcuts stay theirs).
 */
export function playerKey(e: KeyInput): PlayerAction | null {
  if (e.ctrlKey || e.metaKey || e.altKey) return null;
  if (e.key === "Escape") return { kind: "escape" };
  if (typing(e.target)) return null;
  switch (e.key) {
    case " ":
    case "k":
    case "K":
      return { kind: "toggle-play" };
    case "ArrowLeft":
      return { kind: "skip", seconds: -SKIP_SEC };
    case "ArrowRight":
      return { kind: "skip", seconds: SKIP_SEC };
    case "ArrowUp":
      return { kind: "volume", delta: VOLUME_STEP };
    case "ArrowDown":
      return { kind: "volume", delta: -VOLUME_STEP };
    case "m":
    case "M":
      return { kind: "toggle-mute" };
    case "f":
    case "F":
      return { kind: "toggle-fullscreen" };
    case "c":
    case "C":
      return { kind: "toggle-subtitles" };
    default:
      if (/^[0-9]$/.test(e.key)) return { kind: "jump", fraction: Number(e.key) / 10 };
      return null;
  }
}

/** A volume step, rounded to whole percent and kept in 0..1. */
export function stepVolume(volume: number, delta: number): number {
  return Math.min(1, Math.max(0, Math.round((volume + delta) * 100) / 100));
}
