import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef } from "react";
import { useParams, useSearchParams } from "react-router";

import { getMovie, getPlayback, type Playback, saveProgress, saveProgressOnExit } from "../api/client";
import { type PlaybackUrls, Player } from "../components/player/Player";
import { ButtonLink } from "../components/ui/Button";
import { ArrowLeftIcon } from "../components/ui/Icons";
import { EmptyState, Skeleton } from "../components/ui/Primitives";
import { describeError } from "../lib/errors";
import { metaParts } from "../lib/movieMeta";
import { ProgressTracker } from "../lib/progress";
import { parseStartTime } from "../lib/startTime";

/**
 * Saves watch progress while this movie plays (see lib/progress.ts). A
 * failed save is dropped: the next one, at most 10 s later, replaces it.
 */
function useProgressTracker(movieId: string, startSec: number | null, runtimeSec: number | null) {
  const queryClient = useQueryClient();
  const tracker = useRef<ProgressTracker | null>(null);
  // The api refuses a position past runtime_sec, and the stream can run a
  // fraction of a second longer than ffprobe's runtime.
  const runtime = useRef(runtimeSec);
  useEffect(() => {
    runtime.current = runtimeSec;
  }, [runtimeSec]);
  useEffect(() => {
    if (startSec === null) return;
    const t = new ProgressTracker((sec, { final }) => {
      const pos = runtime.current === null ? sec : Math.min(sec, runtime.current);
      if (final) saveProgressOnExit(movieId, pos);
      else
        saveProgress(movieId, pos)
          // The save on leaving can land after the catalog fetched its row.
          .then(() => queryClient.invalidateQueries({ queryKey: ["continue-watching"] }))
          .catch(() => undefined);
    }, startSec);
    tracker.current = t;
    const onHide = () => {
      t.onHide();
    };
    // pagehide, not unload or beforeunload: it fires on tab close, reload
    // and navigation, and on mobile when the page is put away.
    window.addEventListener("pagehide", onHide);
    return () => {
      window.removeEventListener("pagehide", onHide);
      // Leaving the watch page inside the app: the page lives on, so an
      // ordinary save (which may refresh the token) is safe.
      t.onLeave();
      tracker.current = null;
    };
  }, [movieId, startSec, queryClient]);
  return {
    onTime: (sec: number, playing: boolean) => tracker.current?.onTime(sec, playing),
    onPause: (sec: number) => tracker.current?.onPause(sec),
  };
}

export function WatchPage() {
  const { id = "" } = useParams();
  const [params] = useSearchParams();
  const fromUrl = parseStartTime(params.get("t"));

  const movie = useQuery({ queryKey: ["movie", id], queryFn: () => getMovie(id) });
  // Fetched once per page view: the player keeps its own copy of the URLs
  // and asks for fresh ones itself when they expire (refreshUrls), without
  // a remount.
  const playback = useQuery({
    queryKey: ["playback", id],
    queryFn: () => getPlayback(id),
    staleTime: Infinity,
    gcTime: 0,
  });

  const refreshUrls = useCallback(() => getPlayback(id).then(urlsOf), [id]);

  const error = playback.error ?? movie.error;
  // ?t= overrides the saved position; null until playback has answered.
  const startAt = fromUrl ?? playback.data?.resume_sec ?? null;
  const tracker = useProgressTracker(
    id,
    playback.data ? (startAt ?? 0) : null,
    movie.data?.runtime_sec ?? null,
  );

  const back = `/movies/${id}`;
  return (
    <main className="mx-auto flex max-w-[1600px] flex-col gap-5 px-0 py-0 sm:px-6 sm:py-6">
      {error ? (
        <div className="px-4 py-16 sm:px-0">
          <EmptyState title="This movie cannot be played" action={<ButtonLink to={back}>Back to the movie</ButtonLink>}>
            {describeError(error)}
          </EmptyState>
        </div>
      ) : playback.data ? (
        <Player
          key={id}
          urls={urlsOf(playback.data)}
          posterUrl={movie.data?.backdrop_url ?? null}
          title={movie.data?.title ?? ""}
          backTo={back}
          startAt={startAt}
          resumed={fromUrl === null && startAt !== null}
          onTime={tracker.onTime}
          onPause={tracker.onPause}
          refreshUrls={refreshUrls}
        />
      ) : (
        <div aria-label="Loading">
          <Skeleton className="aspect-video w-full sm:rounded-xl" />
        </div>
      )}

      {movie.data && (
        <header className="flex flex-col gap-2 px-4 sm:px-0">
          <h1 className="text-2xl font-bold sm:text-3xl">{movie.data.title}</h1>
          <p className="text-sm text-fg-muted">{metaParts(movie.data).join(" · ")}</p>
          {movie.data.overview && <p className="mt-1 max-w-3xl text-fg-muted">{movie.data.overview}</p>}
          <ButtonLink to={back} variant="ghost" size="sm" className="mt-2 w-fit">
            <ArrowLeftIcon size={16} />
            Movie details
          </ButtonLink>
        </header>
      )}
    </main>
  );
}

/** The playback response as the player takes it. */
function urlsOf(p: Playback): PlaybackUrls {
  return {
    playlistUrl: p.playlist_url,
    thumbnailsUrl: p.thumbnails_url,
    subtitles: p.subtitles.map((s) => ({ url: s.url, language: s.language, label: s.label })),
  };
}
