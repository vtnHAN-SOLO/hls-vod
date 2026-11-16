// Volume rules for the player's speaker icon and slider.

export type VolumeIcon = "muted" | "low" | "high";

/** The speaker's shape: muted (or silent), low (under half), high. */
export function volumeIcon(volume: number, muted: boolean): VolumeIcon {
  if (muted || volume <= 0) return "muted";
  return volume < 0.5 ? "low" : "high";
}

/**
 * Whether the page may set the volume. iOS and iPadOS ignore it (only the
 * hardware buttons change it): setting it does nothing, so the slider would
 * lie. Probes a media element: set, read back, restore.
 */
export function volumeSettable(media: { volume: number }): boolean {
  const before = media.volume;
  try {
    const probe = before === 0.5 ? 0.25 : 0.5;
    media.volume = probe;
    const ok = Math.abs(media.volume - probe) < 0.01;
    media.volume = before;
    return ok;
  } catch {
    return false;
  }
}

/** A wheel step over the slider: up is louder, 5 % a notch, kept in 0..1. */
export function wheelVolume(volume: number, deltaY: number): number {
  if (deltaY === 0) return volume;
  const step = deltaY < 0 ? 0.05 : -0.05;
  return Math.min(1, Math.max(0, Math.round((volume + step) * 100) / 100));
}
