import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useParams } from "react-router";

import { continueWatching, getMovie, listMovies, type MovieDetail } from "../api/client";
import { Backdrop, PosterImage } from "../components/Poster";
import { StatusBadge } from "../components/StatusBadge";
import { Button, ButtonLink } from "../components/ui/Button";
import { MovieCard } from "../components/ui/Card";
import { ArrowLeftIcon, FilmIcon, PlayIcon, SubtitlesIcon } from "../components/ui/Icons";
import { Badge, EmptyState, Skeleton } from "../components/ui/Primitives";
import { Row, RowItem } from "../components/ui/Row";
import { STATUS_LABELS } from "../lib/catalogParams";
import { describeError, isRetryable } from "../lib/errors";
import { formatBitrate, formatBytes, formatRemaining, watchedFraction } from "../lib/format";
import { once } from "../lib/mediaExpiry";
import { POSTER_SIZES } from "../lib/poster";
import { metaParts, playLabel, qualityLabel } from "../lib/movieMeta";
import { browserNamer, languageName } from "../lib/subtitleMenu";

function backTo(state: unknown): { to: string; label: string } {
  // Set by the Browse page's cards, so "back" keeps search and filters.
  if (typeof state === "object" && state !== null && "catalogSearch" in state) {
    const search = String(state.catalogSearch);
    return { to: search === "" ? "/browse" : `/browse?${search}`, label: "Back to Browse" };
  }
  return { to: "/", label: "Home" };
}

export function MovieDetailPage() {
  const { id = "" } = useParams();
  const location = useLocation();
  // Same key as the watch page: going to /watch/:id reuses this response.
  const movie = useQuery({ queryKey: ["movie", id], queryFn: () => getMovie(id) });
  // A poster that fails to load may carry an expired URL: refetch once.
  const [posterFailed] = useState(() => once(() => void movie.refetch()));
  const back = backTo(location.state);

  return (
    <main>
      {movie.isPending ? (
        <DetailSkeleton />
      ) : movie.isError ? (
        <div className="mx-auto max-w-3xl px-4 py-16 sm:px-6">
          <EmptyState
            title="This movie could not be shown"
            action={
              isRetryable(movie.error) ? (
                <Button onClick={() => void movie.refetch()}>Try again</Button>
              ) : (
                <ButtonLink to="/browse">Browse movies</ButtonLink>
              )
            }
          >
            {describeError(movie.error)}
          </EmptyState>
        </div>
      ) : (
        <>
          <Header movie={movie.data} back={back} onPosterFailed={posterFailed} />
          <div className="mx-auto flex max-w-[1600px] flex-col gap-12 px-4 pt-10 sm:px-6">
            <Technical movie={movie.data} />
            <MoreMovies exclude={movie.data.id} />
          </div>
        </>
      )}
    </main>
  );
}

function Header({
  movie,
  back,
  onPosterFailed,
}: {
  movie: MovieDetail;
  back: { to: string; label: string };
  onPosterFailed: () => void;
}) {
  // The caller's position, from the Continue Watching data (no extra call).
  const progress = useQuery({ queryKey: ["continue-watching"], queryFn: continueWatching });
  const saved = progress.data?.items.find((p) => p.movie.id === movie.id);
  const ready = movie.status === "ready";
  const quality = qualityLabel(movie.top_height);
  const meta = metaParts(movie);

  return (
    <header className="relative overflow-hidden border-b border-line">
      {/* The video frame, blurred: colour behind the poster, never a second picture. */}
      <Backdrop
        key={movie.backdrop_url}
        url={movie.backdrop_url}
        title={movie.title}
        eager
        onFailed={onPosterFailed}
        className="absolute inset-0 h-full scale-110 opacity-55 blur-2xl"
      />
      <div aria-hidden="true" className="absolute inset-0 bg-gradient-to-r from-bg/90 via-bg/70 to-bg/40" />
      <div aria-hidden="true" className="absolute inset-0 bg-gradient-to-t from-bg via-bg/25 to-bg/0" />

      <div className="relative mx-auto flex max-w-[1600px] flex-col gap-6 px-4 pt-6 pb-12 sm:px-6 lg:pt-10 lg:pb-16">
        <Link
          to={back.to}
          className="flex w-fit items-center gap-2 rounded-md text-sm text-fg-muted transition-colors duration-150 hover:text-fg"
        >
          <ArrowLeftIcon size={16} />
          {back.label}
        </Link>
        <div className="grid gap-8 md:grid-cols-[15rem_minmax(0,1fr)] md:items-start lg:grid-cols-[18rem_minmax(0,1fr)] lg:gap-12">
        <PosterImage
          key={movie.poster?.variants[0]?.url}
          poster={movie.poster}
          title={movie.title}
          sizes={POSTER_SIZES.detail}
          eager
          onFailed={onPosterFailed}
          className="w-40 rounded-2xl shadow-2xl shadow-scrim/60 ring-1 ring-line-strong sm:w-48 md:w-full"
        />
        <div className="flex min-w-0 flex-col gap-5 md:pt-4">
          <h1 className="text-4xl leading-tight font-bold sm:text-5xl lg:text-6xl">{movie.title}</h1>
          <p className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-sm text-fg-muted">
            {meta.map((part, i) => (
              <span key={part} className="flex items-center gap-3">
                {i > 0 && <span aria-hidden="true" className="text-fg-subtle">·</span>}
                {part}
              </span>
            ))}
            {quality && <Badge className="border border-line-strong bg-scrim/30 text-fg">{quality}</Badge>}
            {!ready && <StatusBadge status={movie.status} />}
          </p>
          {movie.overview && <p className="max-w-2xl text-base leading-relaxed text-fg-muted sm:text-lg">{movie.overview}</p>}
          {movie.subtitle_languages.length > 0 && <SubtitleLanguages languages={movie.subtitle_languages} />}

          {ready ? (
            <div className="flex flex-col gap-3">
              <ButtonLink to={`/watch/${movie.id}`} variant="primary" size="lg" className="w-fit">
                <PlayIcon size={18} />
                {playLabel(saved?.position_sec)}
              </ButtonLink>
              {saved && saved.position_sec > 0 && (
                <div className="flex max-w-sm items-center gap-3 text-xs text-fg-muted">
                  <div
                    role="progressbar"
                    aria-label="Watched"
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={Math.round(watchedFraction(saved.position_sec, saved.runtime_sec) * 100)}
                    className="h-1 flex-1 overflow-hidden rounded-full bg-fg/15"
                  >
                    <div
                      className="h-full rounded-full bg-accent"
                      style={{ width: `${String(watchedFraction(saved.position_sec, saved.runtime_sec) * 100)}%` }}
                    />
                  </div>
                  {formatRemaining(saved.position_sec, saved.runtime_sec)}
                </div>
              )}
            </div>
          ) : (
            <div className="flex flex-col gap-2">
              <Button variant="secondary" size="lg" disabled className="w-fit">
                <PlayIcon size={18} />
                Play
              </Button>
              <p className="text-sm text-fg-muted">
                Not playable yet: this movie is {STATUS_LABELS[movie.status].toLowerCase()}.
              </p>
            </div>
          )}
        </div>
        </div>
      </div>
    </header>
  );
}

/** "Subtitles: English, Vietnamese", each name in the viewer's language. */
function SubtitleLanguages({ languages }: { languages: string[] }) {
  const namer = browserNamer();
  return (
    <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-fg-muted">
      <SubtitlesIcon size={18} className="text-fg-subtle" />
      <span className="sr-only">Subtitles:</span>
      {languages.map((l, i) => (
        <span key={l}>
          <span lang={l === "und" ? undefined : l}>{languageName(l, namer)}</span>
          {i < languages.length - 1 && <span aria-hidden="true">,</span>}
        </span>
      ))}
    </p>
  );
}

/** The HLS rungs, for the curious: quality, bitrate, size. */
function Technical({ movie }: { movie: MovieDetail }) {
  if (movie.renditions.length === 0) return null;
  return (
    <section aria-labelledby="qualities" className="flex flex-col gap-3">
      <h2 id="qualities" className="text-lg font-semibold">
        Available qualities
      </h2>
      <ul className="flex flex-wrap gap-3">
        {movie.renditions.map((r) => (
          <li key={r.height} className="flex min-w-36 flex-col gap-0.5 rounded-xl border border-line bg-surface px-4 py-3">
            <span className="font-display text-lg font-semibold text-fg">{qualityLabel(r.height)}</span>
            <span className="text-xs text-fg-subtle tabular-nums">
              {formatBitrate(r.bitrate_kbps)} · {formatBytes(r.size_bytes)}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function MoreMovies({ exclude }: { exclude: string }) {
  const movies = useQuery({
    queryKey: ["movies", { sort: "added_desc", limit: 13 }],
    queryFn: () => listMovies({ sort: "added_desc", limit: 13 }),
  });
  const [posterFailed] = useState(() => once(() => void movies.refetch()));
  const items = (movies.data?.items ?? []).filter((m) => m.id !== exclude).slice(0, 12);
  if (items.length === 0) return null;
  return (
    <Row
      title="More movies"
      icon={<FilmIcon size={20} />}
      action={
        <Link to="/browse" className="text-accent-soft hover:underline">
          See all
        </Link>
      }
    >
      {items.map((m) => (
        <RowItem key={m.id}>
          <MovieCard movie={m} onPosterFailed={posterFailed} />
        </RowItem>
      ))}
    </Row>
  );
}

function DetailSkeleton() {
  return (
    <div aria-label="Loading movie" className="mx-auto flex max-w-[1600px] flex-col gap-6 px-4 pt-6 pb-12 sm:px-6 lg:pt-10">
      <Skeleton className="h-4 w-24" />
      <div className="grid gap-8 md:grid-cols-[15rem_minmax(0,1fr)] lg:grid-cols-[18rem_minmax(0,1fr)] lg:gap-12">
        <Skeleton className="aspect-[2/3] w-40 rounded-2xl sm:w-48 md:w-full" />
        <div className="flex flex-col gap-5 md:pt-4">
          <Skeleton className="h-14 w-2/3 max-w-xl" />
          <Skeleton className="h-4 w-48" />
          <Skeleton className="h-20 w-full max-w-2xl" />
          <Skeleton className="h-12 w-40" />
        </div>
      </div>
    </div>
  );
}
