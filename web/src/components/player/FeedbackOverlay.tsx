import type { Feedback } from "../../lib/feedback";
import { PauseIcon, PlayIcon, SkipBackIcon, SkipForwardIcon, VolumeMutedIcon } from "../ui/Icons";

/**
 * The feedback over the video (lib/feedback.ts): a skip bubble on the left
 * or right, the volume in the middle, a play or pause flash. It never takes
 * a pointer, and screen readers get the same news from the controls
 * themselves. Keyed by seq, so each update restarts the fade.
 */
export function FeedbackOverlay({ feedback }: { feedback: Feedback | null }) {
  if (!feedback) return null;
  const bubble = "feedback flex items-center gap-2 rounded-full bg-scrim/60 px-5 py-3 text-lg font-semibold text-fg backdrop-blur-sm";
  switch (feedback.kind) {
    case "skip":
      return (
        <div
          key={feedback.seq}
          aria-hidden="true"
          className={`pointer-events-none absolute inset-y-0 flex w-1/3 items-center ${
            feedback.side === "back" ? "left-0 justify-center" : "right-0 justify-center"
          }`}
        >
          <div className={bubble}>
            {feedback.side === "back" ? (
              <>
                <SkipBackIcon size={26} seconds={5} />
                {feedback.seconds}s
              </>
            ) : (
              <>
                {feedback.seconds}s
                <SkipForwardIcon size={26} seconds={5} />
              </>
            )}
          </div>
        </div>
      );
    case "volume":
      return (
        <div key={feedback.seq} aria-hidden="true" className="pointer-events-none absolute inset-0 grid place-items-center">
          <div className={`${bubble} tabular-nums`}>
            {feedback.muted || feedback.percent === 0 ? <VolumeMutedIcon size={28} /> : `${String(feedback.percent)}%`}
          </div>
        </div>
      );
    case "play":
    case "pause":
      return (
        <div key={feedback.seq} aria-hidden="true" className="pointer-events-none absolute inset-0 grid place-items-center">
          <div className="feedback grid size-20 place-items-center rounded-full bg-scrim/55 text-fg backdrop-blur-sm">
            {feedback.kind === "play" ? <PlayIcon size={36} /> : <PauseIcon size={36} />}
          </div>
        </div>
      );
  }
}
