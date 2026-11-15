import { useQuery } from "@tanstack/react-query";
import Hls from "hls.js";
import { useEffect, useRef, useState } from "react";

import { formatTime } from "../lib/format";
import { ExpiryGuard } from "../lib/mediaExpiry";
import { clampStart } from "../lib/startTime";
import { parseThumbnails, type ThumbnailCue } from "../lib/thumbnails";
import { QualityMenu, type QualityLevel } from "./QualityMenu";
import { SeekBar } from "./SeekBar";

interface Props {
  playlistUrl: string;
  thumbnailsUrl: string;
  posterUrl: string | null;
  startAt: number | null; // seconds: ?t=, else the saved position
  /** Every timeupdate, with whether the video is playing. */
  onTime?: (sec: number, playing: boolean) => void;
  /** Pause, including the one at the end of the movie. */
  onPause?: (sec: number) => void;
  /**
   * Fresh signed URLs from the playback API, for when media-delivery answers
   * 410 (they expired while the movie played). They may point at a newer
   * version folder (a retranscode): playback goes on there.
   */
  refreshUrls: () => Promise<{ playlistUrl: string; thumbnailsUrl: string }>;
}

/** What the player plays: the URLs, and where and how to (re)start. */
interface Source {
  url: string;
  thumbnails: string;
  startAt: number | null;
  autoplay: boolean;
}

const RENEW_FAILED = "Playback failed (the video link expired and could not be renewed).";

const NATIVE_HLS = "application/vnd.apple.mpegurl";
const MAX_NETWORK_RETRIES = 3;

type Engine = "hls.js" | "native" | "unsupported";

function detectEngine(): Engine {
  if (Hls.isSupported()) return "hls.js";
  // Safari on iOS has no MSE: it plays HLS itself (and picks quality itself).
  if (document.createElement("video").canPlayType(NATIVE_HLS) !== "") return "native";
  return "unsupported";
}

async function fetchThumbnails(url: string): Promise<ThumbnailCue[]> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`thumbnails: HTTP ${res.status}`);
  return parseThumbnails(await res.text(), new URL(url, window.location.href).href);
}

/**
 * HLS player: hls.js where MSE exists, native HLS otherwise. Every URL comes
 * from the playback API. Parent should key it by the movie id, so a new movie
 * gets a fresh player instead of resetting state in an effect; the URLs it
 * is given are only the first ones (see refreshUrls).
 *
 * Expired URLs: a 410 from media-delivery (playlist or segment) fetches new
 * ones and reloads at the same position, playing if it was playing; at most
 * once a minute, and never for a 403 (lib/mediaExpiry.ts).
 */
export function Player({ playlistUrl, thumbnailsUrl, posterUrl, startAt, onTime, onPause, refreshUrls }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const hlsRef = useRef<Hls | null>(null);

  const [source, setSource] = useState<Source>({ url: playlistUrl, thumbnails: thumbnailsUrl, startAt, autoplay: false });
  const [expiry] = useState(() => new ExpiryGuard());
  // While a renewed source loads, the video element is reset (time 0,
  // paused): none of that is the viewer's, so no progress is saved for it.
  const switching = useRef(false);
  const refreshRef = useRef(refreshUrls);
  useEffect(() => {
    refreshRef.current = refreshUrls;
  }, [refreshUrls]);

  const [engine] = useState(detectEngine);
  const [error, setError] = useState<string | null>(
    engine === "unsupported" ? "This browser cannot play HLS video." : null,
  );
  const [levels, setLevels] = useState<QualityLevel[]>([]);
  const [selected, setSelected] = useState(-1);
  const [playingHeight, setPlayingHeight] = useState<number | null>(null);
  const [time, setTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [buffered, setBuffered] = useState(0);
  const [paused, setPaused] = useState(true);
  const [muted, setMuted] = useState(false);

  // Previews are optional: a missing or broken index just means no sprites.
  const { data: cues = [] } = useQuery({
    queryKey: ["thumbnails", source.thumbnails],
    queryFn: () => fetchThumbnails(source.thumbnails),
    staleTime: Infinity,
    retry: false,
  });

  useEffect(() => {
    const video = videoRef.current;
    if (!video || engine === "unsupported") return;
    const { url, startAt: start, autoplay } = source;
    let live = true;

    // Asks the api for new URLs and restarts there, where the viewer was.
    const renew = () => {
      const at = video.currentTime;
      const playing = !video.paused;
      switching.current = true;
      refreshRef
        .current()
        .then(
          (u) => {
            if (live) setSource({ url: u.playlistUrl, thumbnails: u.thumbnailsUrl, startAt: at, autoplay: playing });
          },
          () => {
            switching.current = false;
            if (live) setError(RENEW_FAILED);
          },
        )
        .finally(() => {
          expiry.done();
        });
    };
    const resumed = () => {
      switching.current = false;
      if (autoplay) video.play().catch(() => undefined);
    };
    video.addEventListener("canplay", resumed, { once: true });

    if (engine === "native") {
      const onMeta = () => {
        video.currentTime = clampStart(start, video.duration);
      };
      // Native HLS does not say why a load failed: ask the playlist URL.
      const onError = () => {
        fetch(url)
          .then(
            (r) => r.status,
            () => undefined,
          )
          .then((status) => {
            if (!live) return;
            const action = expiry.decide(status);
            if (action === "refresh") renew();
            else if (action !== "wait") setError("Playback failed.");
          })
          .catch(() => undefined);
      };
      video.addEventListener("loadedmetadata", onMeta, { once: true });
      video.addEventListener("error", onError);
      video.src = url;
      return () => {
        live = false;
        video.removeEventListener("loadedmetadata", onMeta);
        video.removeEventListener("error", onError);
        video.removeEventListener("canplay", resumed);
        video.removeAttribute("src");
        video.load();
      };
    }

    // Start loading at the start position directly rather than at 0 and seeking.
    const hls = new Hls({ startPosition: start ?? -1 });
    hlsRef.current = hls;
    let networkRetries = 0;
    let mediaRecovered = false;

    hls.on(Hls.Events.MANIFEST_PARSED, (_e, data) => {
      setLevels(data.levels.map((l, index) => ({ index, height: l.height, bitrate: l.bitrate })));
    });
    hls.on(Hls.Events.LEVEL_SWITCHED, (_e, data) => {
      setPlayingHeight(hls.levels[data.level]?.height ?? null);
    });
    // A start past the end: start from the beginning instead of stalling.
    hls.once(Hls.Events.LEVEL_LOADED, (_e, data) => {
      if (start !== null && clampStart(start, data.details.totalduration) === 0) hls.startLoad(0);
    });
    hls.on(Hls.Events.FRAG_LOADED, () => {
      networkRetries = 0;
    });
    hls.on(Hls.Events.ERROR, (_e, data) => {
      // 410 (expired) and 403 (bad token) first, fatal or not: hls.js would
      // only retry the same URL.
      const action = expiry.decide(data.response?.code);
      if (action === "refresh") {
        hls.stopLoad();
        renew();
        return;
      }
      if (action === "wait") return;
      if (action === "fail") {
        setError(`Playback failed (${data.details}).`);
        hls.destroy();
        return;
      }
      if (!data.fatal) return;
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR && networkRetries < MAX_NETWORK_RETRIES) {
        networkRetries++;
        hls.startLoad();
      } else if (data.type === Hls.ErrorTypes.MEDIA_ERROR && !mediaRecovered) {
        mediaRecovered = true;
        hls.recoverMediaError();
      } else {
        setError(`Playback failed (${data.details}).`);
        hls.destroy();
      }
    });

    hls.loadSource(url);
    hls.attachMedia(video);
    return () => {
      live = false;
      video.removeEventListener("canplay", resumed);
      hls.destroy();
      hlsRef.current = null;
    };
  }, [engine, source, expiry]);

  const video = () => videoRef.current;
  const togglePlay = () => {
    const v = video();
    if (!v) return;
    if (v.paused) void v.play();
    else v.pause();
  };
  const seek = (t: number) => {
    const v = video();
    if (v) v.currentTime = t;
  };
  const selectLevel = (index: number) => {
    setSelected(index);
    const hls = hlsRef.current;
    if (hls) hls.currentLevel = index; // -1 = automatic; immediate switch
  };
  const toggleFullscreen = () => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void containerRef.current?.requestFullscreen();
  };

  return (
    <div
      ref={containerRef}
      tabIndex={0}
      className="group/player relative aspect-video w-full overflow-hidden bg-black outline-none"
      onKeyDown={(e) => {
        const v = video();
        if (!v) return;
        switch (e.key) {
          case " ":
          case "k":
            e.preventDefault();
            togglePlay();
            break;
          case "ArrowLeft":
            seek(Math.max(v.currentTime - 10, 0));
            break;
          case "ArrowRight":
            seek(Math.min(v.currentTime + 10, v.duration || 0));
            break;
          case "f":
            toggleFullscreen();
            break;
          case "m":
            v.muted = !v.muted;
            break;
        }
      }}
    >
      <video
        ref={videoRef}
        className="h-full w-full"
        poster={posterUrl ?? undefined}
        playsInline
        onClick={togglePlay}
        onTimeUpdate={(e) => {
          if (switching.current) return;
          setTime(e.currentTarget.currentTime);
          onTime?.(e.currentTarget.currentTime, !e.currentTarget.paused);
        }}
        onDurationChange={(e) => {
          setDuration(e.currentTarget.duration);
        }}
        onProgress={(e) => {
          const b = e.currentTarget.buffered;
          setBuffered(b.length > 0 ? b.end(b.length - 1) : 0);
        }}
        onPlay={() => {
          setPaused(false);
        }}
        onPause={(e) => {
          if (switching.current) return;
          setPaused(true);
          onPause?.(e.currentTarget.currentTime);
        }}
        onVolumeChange={(e) => {
          setMuted(e.currentTarget.muted);
        }}
      />

      {error && (
        <div className="absolute inset-0 flex items-center justify-center bg-black/80 p-6 text-center text-red-300">
          {error}
        </div>
      )}

      <div
        className={`absolute inset-x-0 bottom-0 flex flex-col gap-1 bg-gradient-to-t from-black/90 to-transparent px-4 pt-10 pb-3 transition-opacity ${
          paused ? "opacity-100" : "opacity-0 group-hover/player:opacity-100 group-focus-within/player:opacity-100"
        }`}
      >
        <SeekBar time={time} duration={duration} buffered={buffered} cues={cues} onSeek={seek} />
        <div className="flex items-center gap-3 text-sm">
          <button
            type="button"
            className="w-16 rounded px-2 py-1 text-left hover:bg-white/10"
            onClick={togglePlay}
            aria-label={paused ? "Play" : "Pause"}
          >
            {paused ? "\u25B6\uFE0E Play" : "\u275A\u275A Pause"}
          </button>
          <button
            type="button"
            className="rounded px-2 py-1 hover:bg-white/10"
            onClick={() => {
              const v = video();
              if (v) v.muted = !v.muted;
            }}
          >
            {muted ? "Unmute" : "Mute"}
          </button>
          <span className="tabular-nums text-neutral-300">
            {formatTime(time)} / {formatTime(duration)}
          </span>
          <span className="ml-auto" />
          <QualityMenu levels={levels} selected={selected} playingHeight={playingHeight} onSelect={selectLevel} />
          <button type="button" className="rounded px-2 py-1 hover:bg-white/10" onClick={toggleFullscreen}>
            Fullscreen
          </button>
        </div>
      </div>
    </div>
  );
}
