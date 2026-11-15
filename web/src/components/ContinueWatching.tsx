import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";

import { continueWatching, type ContinueWatchingItem } from "../api/client";
import { formatRemaining, watchedFraction } from "../lib/format";
import { once } from "../lib/mediaExpiry";
import { Poster } from "./Poster";

/**
 * The caller's unfinished movies, most recent first. Optional: while it
 * loads, when it fails, or when there is nothing to continue, it shows
 * nothing and the catalog below is unaffected.
 */
export function ContinueWatching() {
  const { data, refetch } = useQuery({ queryKey: ["continue-watching"], queryFn: continueWatching });
  // A poster that fails to load may carry an expired URL: refetch once.
  const [posterFailed] = useState(() => once(() => void refetch()));
  if (!data || data.items.length === 0) return null;
  return (
    <section aria-labelledby="continue-watching" className="flex flex-col gap-3">
      <h2 id="continue-watching" className="text-lg font-semibold">
        Continue watching
      </h2>
      <ul className="grid grid-flow-col auto-cols-[minmax(10rem,14rem)] gap-4 overflow-x-auto pb-2">
        {data.items.map((item) => (
          <li key={item.movie.id}>
            <ResumeCard item={item} onPosterFailed={posterFailed} />
          </li>
        ))}
      </ul>
    </section>
  );
}

function ResumeCard({ item, onPosterFailed }: { item: ContinueWatchingItem; onPosterFailed: () => void }) {
  const { movie, position_sec: pos, runtime_sec: runtime } = item;
  return (
    <Link
      // No ?t=: the watch page resumes from the saved position itself.
      to={`/watch/${movie.id}`}
      className="group flex flex-col gap-2 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-white/70"
    >
      <div className="relative overflow-hidden rounded-md">
        <Poster key={movie.poster_url} url={movie.poster_url} title={movie.title} onFailed={onPosterFailed} />
        <div className="absolute inset-x-0 bottom-0 h-1 bg-white/20">
          <div className="h-full bg-red-500" style={{ width: `${watchedFraction(pos, runtime) * 100}%` }} />
        </div>
      </div>
      <div className="flex flex-col gap-0.5">
        <span className="line-clamp-1 text-sm font-medium group-hover:text-white/80">{movie.title}</span>
        <span className="text-xs text-neutral-400">{formatRemaining(pos, runtime)}</span>
      </div>
    </Link>
  );
}
