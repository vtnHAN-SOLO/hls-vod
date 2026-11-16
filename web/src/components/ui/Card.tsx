import type { ReactNode } from "react";
import { Link } from "react-router";

import type { Movie } from "../../api/client";
import { metaParts, qualityLabel } from "../../lib/movieMeta";
import { POSTER_SIZES } from "../../lib/poster";
import { Backdrop, PosterImage } from "../Poster";
import { PlayIcon } from "./Icons";
import { Badge } from "./Primitives";

/**
 * A movie as a card: its portrait poster (or, for continue watching, the
 * landscape backdrop) with a soft gradient, the title and what we know
 * (year, runtime, top quality). Optional: a watch-progress bar and a line
 * under the title ("23 min left"), a corner badge (status for admins). The
 * whole card is one link.
 */
export function MovieCard({
  movie,
  to,
  linkState,
  progress,
  subtitle,
  corner,
  onPosterFailed,
  shape = "portrait",
  sizes = POSTER_SIZES.row,
}: {
  movie: Movie;
  /** portrait: the uploaded 2:3 poster; landscape: the video frame. */
  shape?: "portrait" | "landscape";
  /** The poster's `sizes` (one of POSTER_SIZES): where the card sits. */
  sizes?: string;
  /** Where the card goes; the detail page by default. */
  to?: string;
  linkState?: unknown;
  /** Share watched, 0..1: a bar along the bottom of the image. */
  progress?: number;
  /** Replaces the year · runtime line. */
  subtitle?: ReactNode;
  corner?: ReactNode;
  onPosterFailed?: () => void;
}) {
  const quality = qualityLabel(movie.top_height);
  const meta = metaParts(movie).join(" · ");
  return (
    <Link
      to={to ?? `/movies/${movie.id}`}
      state={linkState}
      className="group flex flex-col gap-2.5 rounded-xl focus-visible:outline-offset-4"
    >
      <div className="relative overflow-hidden rounded-xl bg-raised ring-1 ring-line transition-shadow duration-200 group-hover:ring-line-strong">
        {shape === "portrait" ? (
          <PosterImage
            key={movie.poster?.variants[0]?.url}
            poster={movie.poster}
            title={movie.title}
            sizes={sizes}
            onFailed={onPosterFailed}
            imgClassName="transition-transform duration-250 ease-out-soft motion-safe:group-hover:scale-[1.04]"
          />
        ) : (
          <Backdrop
            key={movie.backdrop_url}
            url={movie.backdrop_url}
            title={movie.title}
            onFailed={onPosterFailed}
            className="transition-transform duration-250 ease-out-soft motion-safe:group-hover:scale-[1.04]"
          />
        )}
        <div
          aria-hidden="true"
          className="absolute inset-0 bg-gradient-to-t from-scrim/70 via-scrim/0 to-scrim/0 opacity-70 transition-opacity duration-200 group-hover:opacity-100"
        />
        <span
          aria-hidden="true"
          className="absolute inset-0 grid place-items-center opacity-0 transition-opacity duration-200 group-hover:opacity-100 group-focus-visible:opacity-100"
        >
          <span className="grid size-11 place-items-center rounded-full bg-accent/90 text-on-accent shadow-lg shadow-scrim/50">
            <PlayIcon size={18} />
          </span>
        </span>
        {quality && (
          <Badge className="absolute top-2 right-2 bg-scrim/60 text-fg backdrop-blur-sm">{quality}</Badge>
        )}
        {corner && <div className="absolute top-2 left-2">{corner}</div>}
        {progress !== undefined && (
          <div className="absolute inset-x-0 bottom-0 h-1 bg-fg/20">
            <div className="h-full bg-accent" style={{ width: `${String(Math.round(progress * 100))}%` }} />
          </div>
        )}
      </div>
      <div className="flex min-w-0 flex-col gap-0.5 px-0.5">
        <span className="line-clamp-1 text-sm font-semibold text-fg transition-colors duration-150 group-hover:text-accent-soft">
          {movie.title}
        </span>
        <span className="line-clamp-1 text-xs text-fg-subtle">{subtitle ?? (meta || " ")}</span>
      </div>
    </Link>
  );
}

/** The card's shape while loading. */
export function MovieCardSkeleton({ shape = "portrait" }: { shape?: "portrait" | "landscape" }) {
  return (
    <div aria-hidden="true" className="flex flex-col gap-2.5">
      <div
        className={`${shape === "portrait" ? "aspect-[2/3]" : "aspect-video"} rounded-xl bg-raised motion-safe:animate-pulse`}
      />
      <div className="h-3.5 w-3/4 rounded bg-raised motion-safe:animate-pulse" />
      <div className="h-3 w-1/3 rounded bg-raised motion-safe:animate-pulse" />
    </div>
  );
}
