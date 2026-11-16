import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import { continueWatching, featuredMovies, type Movie } from "../api/client";
import { once } from "../lib/mediaExpiry";
import { metaParts, playLabel, qualityLabel } from "../lib/movieMeta";
import { Spotlight as Rotation } from "../lib/spotlight";
import { POSTER_SIZES } from "../lib/poster";
import { Backdrop, PosterImage } from "./Poster";
import { ButtonLink } from "./ui/Button";
import { useReducedMotion } from "./ui/hooks";
import { ChevronLeftIcon, ChevronRightIcon, InfoIcon, PlayIcon } from "./ui/Icons";
import { Badge, EmptyState, Skeleton } from "./ui/Primitives";

/**
 * The home page's spotlight: featured movies (or the newest ready ones),
 * one at a time over their poster frame. It advances every 8 s; never with
 * reduced motion, and not while the pointer or focus is inside it, the tab
 * is hidden, or the viewer pressed pause. Arrows, dots and the Left/Right
 * keys move it; hidden slides are inert.
 */
export function Spotlight() {
  const featured = useQuery({ queryKey: ["featured"], queryFn: featuredMovies });
  // The same query as the Continue Watching row: no second request.
  const progress = useQuery({ queryKey: ["continue-watching"], queryFn: continueWatching });
  const [posterFailed] = useState(() => once(() => void featured.refetch()));

  if (featured.isPending) {
    return (
      <div className="relative -mt-16 h-[min(80vh,46rem)] min-h-[30rem]">
        <Skeleton className="absolute inset-0 rounded-none" />
      </div>
    );
  }
  const movies = featured.data?.items ?? [];
  if (movies.length === 0) {
    return (
      <div className="mx-auto max-w-[1600px] px-4 pt-10 sm:px-6">
        <EmptyState title="Nothing to watch yet">
          Movies appear here once they are transcoded. On a development machine,{" "}
          <code className="rounded bg-overlay px-1 text-fg">make demo-seed</code> adds eight demo movies.
        </EmptyState>
      </div>
    );
  }
  const resume = new Map((progress.data?.items ?? []).map((p) => [p.movie.id, p.position_sec]));
  return (
    <Carousel
      movies={movies}
      featured={featured.data?.featured ?? false}
      resume={resume}
      onPosterFailed={posterFailed}
    />
  );
}

function Carousel({
  movies,
  featured,
  resume,
  onPosterFailed,
}: {
  movies: Movie[];
  featured: boolean;
  resume: Map<string, number>;
  onPosterFailed: () => void;
}) {
  const reduced = useReducedMotion();
  const [index, setIndex] = useState(0);
  const [userPaused, setUserPaused] = useState(false);
  const [rotation] = useState(() => new Rotation(setIndex));
  const root = useRef<HTMLElement>(null);

  useEffect(() => {
    rotation.start(movies.length, reduced);
    return () => {
      rotation.stop();
    };
  }, [rotation, movies.length, reduced]);

  useEffect(() => {
    const onVisibility = () => {
      if (document.hidden) rotation.pause("hidden");
      else rotation.resume("hidden");
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [rotation]);

  const current = movies[Math.min(index, movies.length - 1)] ?? movies[0];
  if (!current) return null;
  const many = movies.length > 1;

  return (
    <section
      ref={root}
      aria-roledescription="carousel"
      aria-label={featured ? "Featured movies" : "New movies"}
      onPointerEnter={() => {
        rotation.pause("hover");
      }}
      onPointerLeave={() => {
        rotation.resume("hover");
      }}
      onFocus={() => {
        rotation.pause("focus");
      }}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget)) rotation.resume("focus");
      }}
      onKeyDown={(e) => {
        if (!many || (e.target as HTMLElement).closest("[data-dots]")) return;
        if (e.key === "ArrowLeft") rotation.prev();
        else if (e.key === "ArrowRight") rotation.next();
      }}
      className="relative -mt-16 h-[min(80vh,46rem)] min-h-[32rem] overflow-hidden bg-bg"
    >
      {movies.map((m, i) => {
        const shown = i === index;
        return (
          <div
            key={m.id}
            role="group"
            aria-roledescription="slide"
            aria-label={`${String(i + 1)} of ${String(movies.length)}: ${m.title}`}
            aria-hidden={!shown}
            inert={!shown}
            className={`absolute inset-0 transition-opacity duration-250 ${shown ? "opacity-100" : "opacity-0"}`}
          >
            <Slide movie={m} featured={featured} resumeSec={resume.get(m.id)} eager={i === 0} onPosterFailed={onPosterFailed} />
          </div>
        );
      })}

      {many && (
        <div className="absolute right-4 bottom-6 z-10 flex items-center gap-2 sm:right-6 sm:bottom-10">
          <div data-dots className="flex items-center gap-1.5" role="group" aria-label="Choose a slide">
            {movies.map((m, i) => (
              <button
                key={m.id}
                type="button"
                aria-label={`Show ${m.title}`}
                aria-current={i === index}
                onClick={() => {
                  rotation.goTo(i);
                }}
                className={`h-1.5 rounded-full transition-all duration-200 ${
                  i === index ? "w-6 bg-fg" : "w-1.5 bg-fg/40 hover:bg-fg/70"
                }`}
              />
            ))}
          </div>
          <RoundButton label="Previous movie" onClick={() => { rotation.prev(); }}>
            <ChevronLeftIcon size={18} />
          </RoundButton>
          <RoundButton label="Next movie" onClick={() => { rotation.next(); }}>
            <ChevronRightIcon size={18} />
          </RoundButton>
          {!reduced && (
            <RoundButton
              label={userPaused ? "Resume rotation" : "Pause rotation"}
              onClick={() => {
                if (userPaused) rotation.resume("user");
                else rotation.pause("user");
                setUserPaused(!userPaused);
              }}
            >
              {userPaused ? (
                <PlayIcon size={14} />
              ) : (
                <svg width="14" height="14" viewBox="0 0 24 24" aria-hidden="true" fill="currentColor">
                  <rect x="6" y="5" width="4" height="14" rx="1" />
                  <rect x="14" y="5" width="4" height="14" rx="1" />
                </svg>
              )}
            </RoundButton>
          )}
        </div>
      )}
    </section>
  );
}

function Slide({
  movie,
  featured,
  resumeSec,
  eager,
  onPosterFailed,
}: {
  movie: Movie;
  featured: boolean;
  resumeSec: number | undefined;
  eager: boolean;
  onPosterFailed: () => void;
}) {
  const quality = qualityLabel(movie.top_height);
  const meta = metaParts(movie);
  return (
    <>
      <Backdrop
        key={movie.backdrop_url}
        url={movie.backdrop_url}
        title={movie.title}
        eager={eager}
        onFailed={onPosterFailed}
        className="absolute inset-0 h-full"
      />
      {/* Legibility: dark from the left (text) and from the bottom (rows). */}
      <div aria-hidden="true" className="absolute inset-0 bg-gradient-to-r from-bg via-bg/70 to-bg/0 sm:via-bg/55" />
      <div aria-hidden="true" className="absolute inset-0 bg-gradient-to-t from-bg via-bg/10 to-bg/0" />
      <div className="relative mx-auto flex h-full max-w-[1600px] items-end gap-8 px-4 pb-20 sm:px-6 sm:pb-24 lg:gap-10">
        {/* The poster stands beside the text from md on; phones keep the
            whole width for the title. */}
        <PosterImage
          key={movie.poster?.variants[0]?.url}
          poster={movie.poster}
          title={movie.title}
          sizes={POSTER_SIZES.spotlight}
          eager={eager}
          onFailed={onPosterFailed}
          className="hidden w-44 shrink-0 rounded-xl shadow-2xl shadow-scrim/60 ring-1 ring-line-strong md:block lg:w-52 xl:w-60"
        />
        <div className="flex max-w-xl flex-col gap-4">
          <Badge tone="accent" className="w-fit uppercase">
            {featured ? "Featured" : "New"}
          </Badge>
          <h2 className="text-4xl leading-tight font-bold text-fg sm:text-5xl lg:text-6xl">{movie.title}</h2>
          {(meta.length > 0 || quality) && (
            <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-fg-muted">
              {meta.length > 0 && <span>{meta.join(" · ")}</span>}
              {quality && <Badge className="border border-line-strong bg-scrim/30 text-fg">{quality}</Badge>}
            </p>
          )}
          {movie.overview && <p className="line-clamp-3 max-w-lg text-base leading-relaxed text-fg-muted">{movie.overview}</p>}
          <div className="mt-2 flex flex-wrap gap-3">
            <ButtonLink to={`/watch/${movie.id}`} variant="primary" size="lg">
              <PlayIcon size={18} />
              {playLabel(resumeSec)}
            </ButtonLink>
            <ButtonLink to={`/movies/${movie.id}`} variant="secondary" size="lg" className="bg-overlay/80 backdrop-blur">
              <InfoIcon size={18} />
              Details
            </ButtonLink>
          </div>
        </div>
      </div>
    </>
  );
}

function RoundButton({ label, onClick, children }: { label: string; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onClick}
      className="grid size-9 place-items-center rounded-full border border-line-strong bg-scrim/40 text-fg backdrop-blur transition-colors duration-150 hover:bg-scrim/70"
    >
      {children}
    </button>
  );
}
