import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useParams } from "react-router";

import { getMovie, type MovieDetail } from "../api/client";
import { Poster } from "../components/Poster";
import { StatusBadge } from "../components/StatusBadge";
import { STATUS_LABELS } from "../lib/catalogParams";
import { describeError, isRetryable } from "../lib/errors";
import { formatBitrate, formatBytes, formatTime } from "../lib/format";
import { once } from "../lib/mediaExpiry";

function backTo(state: unknown): string {
  // Set by the catalog's movie cards, so "back" keeps search and filters.
  if (typeof state === "object" && state !== null && "catalogSearch" in state) {
    const search = String(state.catalogSearch);
    return search === "" ? "/" : `/?${search}`;
  }
  return "/";
}

export function MovieDetailPage() {
  const { id = "" } = useParams();
  const location = useLocation();
  // Same key as the watch page: going to /watch/:id reuses this response.
  const movie = useQuery({ queryKey: ["movie", id], queryFn: () => getMovie(id) });
  // A poster that fails to load may carry an expired URL: refetch once.
  const [posterFailed] = useState(() => once(() => void movie.refetch()));

  return (
    <main className="mx-auto flex max-w-5xl flex-col gap-6 px-4 py-6 sm:px-6">
      <Link to={backTo(location.state)} className="w-fit text-sm text-neutral-400 hover:text-neutral-100">
        ← All movies
      </Link>

      {movie.isPending ? (
        <DetailSkeleton />
      ) : movie.isError ? (
        <div className="flex flex-col items-center gap-3 rounded-md border border-white/10 px-6 py-16 text-center">
          <p className="text-neutral-300">{describeError(movie.error)}</p>
          {isRetryable(movie.error) ? (
            <button
              type="button"
              onClick={() => void movie.refetch()}
              className="rounded-md bg-white/10 px-3 py-1.5 text-sm hover:bg-white/20"
            >
              Try again
            </button>
          ) : (
            <Link to="/" className="rounded-md bg-white/10 px-3 py-1.5 text-sm hover:bg-white/20">
              Browse movies
            </Link>
          )}
        </div>
      ) : (
        <Detail movie={movie.data} onPosterFailed={posterFailed} />
      )}
    </main>
  );
}

function Detail({ movie, onPosterFailed }: { movie: MovieDetail; onPosterFailed: () => void }) {
  const meta = [movie.year, movie.runtime_sec ? formatTime(movie.runtime_sec) : null].filter((v) => v !== null);
  const ready = movie.status === "ready";

  return (
    <article className="grid gap-6 md:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
      <Poster key={movie.poster_url} url={movie.poster_url} title={movie.title} onFailed={onPosterFailed} />

      <div className="flex flex-col gap-4">
        <header className="flex flex-col gap-2">
          <h1 className="text-3xl font-semibold">{movie.title}</h1>
          <p className="flex items-center gap-2 text-sm text-neutral-400">
            {meta.join(" · ")}
            <StatusBadge status={movie.status} />
          </p>
        </header>

        {ready ? (
          <Link
            to={`/watch/${movie.id}`}
            className="w-fit rounded-md bg-white px-5 py-2.5 font-semibold text-neutral-950 hover:bg-white/85 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
          >
            {"\u25B6\uFE0E"} Play
          </Link>
        ) : (
          <div className="flex flex-col gap-1">
            <button
              type="button"
              disabled
              className="w-fit cursor-not-allowed rounded-md bg-white/10 px-5 py-2.5 font-semibold text-neutral-500"
            >
              {"\u25B6\uFE0E"} Play
            </button>
            <p className="text-sm text-neutral-400">
              Not playable yet: this movie is {STATUS_LABELS[movie.status].toLowerCase()}.
            </p>
          </div>
        )}

        <p className="max-w-prose text-neutral-300">{movie.overview ?? "No description yet."}</p>

        <section className="flex flex-col gap-2">
          <h2 className="text-sm font-semibold tracking-wide text-neutral-400 uppercase">Renditions</h2>
          {movie.renditions.length === 0 ? (
            <p className="text-sm text-neutral-500">None yet.</p>
          ) : (
            <table className="w-full max-w-md text-left text-sm">
              <thead className="text-neutral-500">
                <tr>
                  <th className="py-1 font-normal">Quality</th>
                  <th className="py-1 font-normal">Bitrate</th>
                  <th className="py-1 font-normal">Size</th>
                </tr>
              </thead>
              <tbody className="tabular-nums">
                {movie.renditions.map((r) => (
                  <tr key={r.height} className="border-t border-white/5">
                    <td className="py-1">{r.height}p</td>
                    <td className="py-1">{formatBitrate(r.bitrate_kbps)}</td>
                    <td className="py-1">{formatBytes(r.size_bytes)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      </div>
    </article>
  );
}

function DetailSkeleton() {
  return (
    <div aria-label="Loading movie" className="grid animate-pulse gap-6 md:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
      <div className="aspect-video rounded-md bg-neutral-800" />
      <div className="flex flex-col gap-3">
        <div className="h-8 w-2/3 rounded bg-neutral-800" />
        <div className="h-4 w-1/4 rounded bg-neutral-800" />
        <div className="h-10 w-28 rounded bg-neutral-800" />
        <div className="h-16 w-full rounded bg-neutral-800" />
      </div>
    </div>
  );
}
