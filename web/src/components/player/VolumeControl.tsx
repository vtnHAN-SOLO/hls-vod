import { useEffect, useRef } from "react";

import { volumeIcon, wheelVolume } from "../../lib/volume";
import { VolumeHighIcon, VolumeLowIcon, VolumeMutedIcon } from "../ui/Icons";
import { ControlButton } from "./ControlButton";

/**
 * The speaker (its shape follows the level; a click mutes or unmutes) and,
 * where the page may set the volume, a slider next to it: always visible on
 * desktop, arrows and Home/End on the keyboard, the scroll wheel over it.
 * iOS and iPadOS ignore the page's volume, so there only the mute stays.
 */
export function VolumeControl({
  volume,
  muted,
  settable,
  onVolume,
  onToggleMute,
}: {
  volume: number;
  muted: boolean;
  settable: boolean;
  onVolume: (v: number) => void;
  onToggleMute: () => void;
}) {
  const slider = useRef<HTMLInputElement>(null);
  const shown = muted ? 0 : volume;
  const icon = volumeIcon(volume, muted);

  // A wheel listener that may preventDefault (the page must not scroll):
  // React's onWheel is passive.
  const volumeRef = useRef(shown);
  const onVolumeRef = useRef(onVolume);
  useEffect(() => {
    volumeRef.current = shown;
    onVolumeRef.current = onVolume;
  });
  useEffect(() => {
    const el = slider.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      onVolumeRef.current(wheelVolume(volumeRef.current, e.deltaY));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => {
      el.removeEventListener("wheel", onWheel);
    };
  }, [settable]);

  return (
    <div className="flex items-center">
      <ControlButton label={muted ? "Unmute" : "Mute"} tooltip={muted ? "Unmute (m)" : "Mute (m)"} onClick={onToggleMute}>
        {icon === "muted" ? <VolumeMutedIcon size={22} /> : icon === "low" ? <VolumeLowIcon size={22} /> : <VolumeHighIcon size={22} />}
      </ControlButton>
      {settable && (
        <input
          ref={slider}
          type="range"
          min={0}
          max={1}
          step={0.05}
          value={shown}
          aria-label="Volume"
          aria-valuetext={`${String(Math.round(shown * 100))}%`}
          onChange={(e) => {
            onVolume(Number(e.target.value));
          }}
          onKeyDown={(e) => {
            // Its own arrows: the player's ↑/↓ and ←/→ would act twice.
            if (["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End", "PageUp", "PageDown"].includes(e.key)) {
              e.stopPropagation();
            }
          }}
          className="player-range mr-2 hidden w-20 sm:block"
          style={{ ["--fill" as string]: `${String(shown * 100)}%` }}
        />
      )}
    </div>
  );
}
