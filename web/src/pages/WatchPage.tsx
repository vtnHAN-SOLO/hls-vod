import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef } from "react";
import { Link, useParams, useSearchParams } from "react-router";

import { getMovie, getPlayback, saveProgress, saveProgressOnExit } from "../api/client";
import { Player } from "../components/Player";
import { describeError } from "../lib/errors";
import { formatTime } from "../lib/format";
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

  const refreshUrls = useCallback(
    () => getPlayback(id).then((p) => ({ playlistUrl: p.playlist_url, thumbnailsUrl: p.thumbnails_url })),
    [id],
  );

  const error = playback.error ?? movie.error;
  // ?t= overrides the saved position; null until playback has answered.
  const startAt = fromUrl ?? playback.data?.resume_sec ?? null;
  const tracker = useProgressTracker(
    id,
    playback.data ? (startAt ?? 0) : null,
    movie.data?.runtime_sec ?? null,
  );

  return (
    <main className="mx-auto flex max-w-6xl flex-col gap-4 p-4 sm:p-6">
      <Link to={`/movies/${id}`} className="w-fit text-sm text-neutral-400 hover:text-neutral-100">
        ← Details
      </Link>
      {error ? (
        <div className="flex aspect-video w-full items-center justify-center rounded bg-neutral-900 p-6 text-center">
          <p className="text-neutral-300">{describeError(error)}</p>
        </div>
      ) : playback.data ? (
        <Player
          key={id}
          playlistUrl={playback.data.playlist_url}
          thumbnailsUrl={playback.data.thumbnails_url}
          posterUrl={movie.data?.poster_url ?? null}
          startAt={startAt}
          onTime={tracker.onTime}
          onPause={tracker.onPause}
          refreshUrls={refreshUrls}
        />
      ) : (
        <div className="aspect-video w-full animate-pulse rounded bg-neutral-900" aria-label="Loading" />
      )}

      {movie.data && (
        <header className="flex flex-col gap-1">
          <h1 className="text-2xl font-semibold">{movie.data.title}</h1>
          <p className="text-sm text-neutral-400">
            {[movie.data.year, movie.data.runtime_sec ? formatTime(movie.data.runtime_sec) : null]
              .filter((v) => v !== null)
              .join(" · ")}
            {startAt !== null && (
              <span>
                {" "}
                · {fromUrl === null ? "resuming" : "starting"} at {formatTime(startAt)}
              </span>
            )}
          </p>
          {movie.data.overview && <p className="mt-2 max-w-3xl text-neutral-300">{movie.data.overview}</p>}
        </header>
      )}
    </main>
  );
}
