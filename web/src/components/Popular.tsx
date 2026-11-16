import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { Link } from "react-router";

import { popularMovies, type PopularItem } from "../api/client";
import { once } from "../lib/mediaExpiry";
import { metaParts, POPULAR_WINDOWS, type PopularWindow, viewersLabel } from "../lib/movieMeta";
import { POSTER_SIZES } from "../lib/poster";
import { PosterImage } from "./Poster";
import { TrendIcon } from "./ui/Icons";
import { EmptyState, Skeleton } from "./ui/Primitives";
import { tabId, tabPanelId, Tabs } from "./ui/Tabs";

const EMPTY: Record<PopularWindow, string> = {
  week: "Nobody has watched anything this week yet.",
  month: "Nobody has watched anything this month yet.",
  all: "Nobody has watched anything yet.",
};

/**
 * Most popular: ready movies ranked by how many people watched them in the
 * window (the api counts distinct viewers). A sidebar on wide screens, a
 * horizontal row on narrow ones: one list, laid out by breakpoint.
 */
export function Popular({ className = "" }: { className?: string }) {
  const [win, setWin] = useState<PopularWindow>("week");
  const baseId = useId();
  const headingId = useId();
  const popular = useQuery({
    queryKey: ["popular", win],
    queryFn: () => popularMovies({ window: win, limit: 10 }),
    // Switching tabs keeps the last ranking on screen until the next arrives.
    placeholderData: keepPreviousData,
  });
  const [posterFailed] = useState(() => once(() => void popular.refetch()));

  return (
    <section aria-labelledby={headingId} className={`flex min-w-0 flex-col gap-4 ${className}`}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id={headingId} className="flex items-center gap-2 text-lg font-semibold sm:text-xl">
          <span className="text-accent-soft">
            <TrendIcon size={20} />
          </span>
          Most popular
        </h2>
        <Tabs tabs={POPULAR_WINDOWS} selected={win} onSelect={setWin} label="Time window" baseId={baseId} />
      </div>
      <div
        id={tabPanelId(baseId)}
        role="tabpanel"
        aria-labelledby={tabId(baseId, win)}
        aria-busy={popular.isPlaceholderData}
        className={`transition-opacity duration-150 ${popular.isPlaceholderData ? "opacity-60" : ""}`}
      >
        {popular.isPending ? (
          <ol className="flex gap-3 overflow-hidden lg:flex-col">
            {Array.from({ length: 5 }, (_, i) => (
              <li key={i} className="w-[70%] shrink-0 sm:w-[40%] lg:w-full">
                <Skeleton className="h-22" />
              </li>
            ))}
          </ol>
        ) : popular.isError || popular.data.items.length === 0 ? (
          <EmptyState title="No ranking yet" className="py-8">
            {popular.isError ? "The ranking could not be loaded." : EMPTY[win]}
          </EmptyState>
        ) : (
          <ol className="scrollbar-none -mx-4 flex snap-x gap-3 overflow-x-auto px-4 pb-2 sm:-mx-6 sm:px-6 lg:mx-0 lg:flex-col lg:gap-1 lg:overflow-visible lg:px-0">
            {popular.data.items.map((item, i) => (
              <li key={item.id} className="w-[78%] shrink-0 snap-start sm:w-[42%] md:w-[32%] lg:w-full">
                <Ranked item={item} rank={i + 1} onPosterFailed={posterFailed} />
              </li>
            ))}
          </ol>
        )}
      </div>
    </section>
  );
}

function Ranked({ item, rank, onPosterFailed }: { item: PopularItem; rank: number; onPosterFailed: () => void }) {
  const meta = metaParts(item);
  return (
    <Link
      to={`/movies/${item.id}`}
      className="group flex items-center gap-3 rounded-xl p-2 transition-colors duration-150 hover:bg-raised"
    >
      <span
        aria-label={`Number ${String(rank)}`}
        className={`w-7 shrink-0 text-center font-display text-2xl font-bold tabular-nums ${
          rank <= 3 ? "text-accent-soft" : "text-fg-subtle"
        }`}
      >
        {rank}
      </span>
      <div className="w-12 shrink-0 overflow-hidden rounded-md ring-1 ring-line">
        <PosterImage
          key={item.poster?.variants[0]?.url}
          poster={item.poster}
          title={item.title}
          sizes={POSTER_SIZES.ranked}
          onFailed={onPosterFailed}
        />
      </div>
      <div className="flex min-w-0 flex-col gap-0.5">
        <span className="line-clamp-1 text-sm font-semibold text-fg group-hover:text-accent-soft">{item.title}</span>
        <span className="line-clamp-1 text-xs text-fg-subtle">
          {[...meta, viewersLabel(item.viewers)].join(" · ")}
        </span>
      </div>
    </Link>
  );
}
