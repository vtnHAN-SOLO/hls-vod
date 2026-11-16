import { useRef, useState } from "react";

import { formatTime } from "../../lib/format";
import { SKIP_SEC } from "../../lib/playerKeys";
import { cueAt, type ThumbnailCue } from "../../lib/thumbnails";

interface Props {
  time: number;
  duration: number;
  buffered: number;
  cues: readonly ThumbnailCue[];
  onSeek: (t: number) => void;
  /** Arrow keys: a skip, so the player shows its bubble like the buttons do. */
  onSkip: (seconds: number) => void;
}

/**
 * Seek bar with a sprite preview of the frame under the pointer. The preview
 * reads the WebVTT sprite index; with no index it still shows the time.
 */
export function SeekBar({ time, duration, buffered, cues, onSeek, onSkip }: Props) {
  const barRef = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<{ x: number; t: number; width: number } | null>(null);
  const [dragging, setDragging] = useState(false);

  const at = (clientX: number) => {
    const bar = barRef.current;
    if (!bar || duration <= 0) return null;
    const r = bar.getBoundingClientRect();
    const x = Math.min(Math.max(clientX - r.left, 0), r.width);
    return { x, t: (x / r.width) * duration, width: r.width };
  };

  const pct = (t: number) => (duration > 0 ? `${Math.min(100, (t / duration) * 100)}%` : "0%");
  const cue = hover ? cueAt(cues, hover.t) : null;
  const previewWidth = cue?.width ?? 64;
  const previewLeft = hover
    ? Math.min(Math.max(hover.x, previewWidth / 2), Math.max(previewWidth / 2, hover.width - previewWidth / 2))
    : 0;

  return (
    <div
      ref={barRef}
      role="slider"
      tabIndex={0}
      aria-label="Seek"
      aria-valuemin={0}
      aria-valuemax={Math.round(duration)}
      aria-valuenow={Math.round(time)}
      aria-valuetext={`${formatTime(time)} of ${formatTime(duration)}`}
      // A tall hit area (the line is thin) and a ring for keyboard focus.
      className="group relative flex h-6 cursor-pointer touch-none items-center rounded outline-none focus-visible:ring-2 focus-visible:ring-accent-soft"
      onPointerMove={(e) => {
        const p = at(e.clientX);
        setHover(p);
        if (dragging && p) onSeek(p.t);
      }}
      onPointerLeave={() => {
        if (!dragging) setHover(null);
      }}
      onPointerDown={(e) => {
        e.currentTarget.setPointerCapture(e.pointerId);
        setDragging(true);
        const p = at(e.clientX);
        if (p) onSeek(p.t);
      }}
      onPointerUp={(e) => {
        e.currentTarget.releasePointerCapture(e.pointerId);
        setDragging(false);
      }}
      onKeyDown={(e) => {
        if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
          e.preventDefault();
          e.stopPropagation();
          onSkip(e.key === "ArrowLeft" ? -SKIP_SEC : SKIP_SEC);
        } else if (e.key === "Home" || e.key === "End") {
          e.preventDefault();
          e.stopPropagation();
          onSeek(e.key === "Home" ? 0 : duration);
        }
      }}
    >
      <div className="relative h-1 w-full rounded-full bg-fg/20 transition-[height] duration-150 group-hover:h-1.5">
        <div className="absolute inset-y-0 left-0 rounded-full bg-fg/35" style={{ width: pct(buffered) }} />
        <div className="absolute inset-y-0 left-0 rounded-full bg-accent" style={{ width: pct(time) }} />
        <div
          className="absolute top-1/2 h-3.5 w-3.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-accent-soft opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-focus-visible:opacity-100"
          style={{ left: pct(time) }}
        />
      </div>

      {hover && (
        <div
          className="pointer-events-none absolute bottom-6 flex -translate-x-1/2 flex-col items-center gap-1"
          style={{ left: previewLeft }}
        >
          {cue && (
            <div
              className="rounded border border-line-strong bg-surface shadow-lg"
              style={{
                width: cue.width,
                height: cue.height,
                backgroundImage: `url("${cue.image}")`,
                backgroundPosition: `-${cue.x}px -${cue.y}px`,
                backgroundRepeat: "no-repeat",
              }}
            />
          )}
          <span className="rounded bg-scrim/80 px-1.5 py-0.5 text-xs tabular-nums">{formatTime(hover.t)}</span>
        </div>
      )}
    </div>
  );
}
