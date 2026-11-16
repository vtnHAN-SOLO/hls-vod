import { useQuery } from "@tanstack/react-query";
import Hls from "hls.js";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router";

import { type Feedback, FeedbackState } from "../../lib/feedback";
import { formatTime } from "../../lib/format";
import { ExpiryGuard } from "../../lib/mediaExpiry";
import { playerKey, SKIP_SEC, stepVolume } from "../../lib/playerKeys";
import { loadPrefs, type PlayerPrefs, savePrefs } from "../../lib/playerPrefs";
import { CLOSED, type MenuEvent, menuReduce, type MenuState } from "../../lib/settingsMenu";
import { clampStart } from "../../lib/startTime";
import { browserNamer, groupTracks, type Track, trackForLanguage } from "../../lib/subtitleMenu";
import { parseThumbnails, type ThumbnailCue } from "../../lib/thumbnails";
import { volumeSettable } from "../../lib/volume";
import { useReducedMotion } from "../ui/hooks";
import {
  ArrowLeftIcon,
  CollapseIcon,
  ExpandIcon,
  GearIcon,
  PauseIcon,
  PipIcon,
  PlayIcon,
  SkipBackIcon,
  SkipForwardIcon,
  SpinnerIcon,
  SubtitlesIcon,
} from "../ui/Icons";
import { ControlButton } from "./ControlButton";
import { FeedbackOverlay } from "./FeedbackOverlay";
import { SeekBar } from "./SeekBar";
import { type QualityLevel, SettingsMenu } from "./SettingsMenu";
import { VolumeControl } from "./VolumeControl";

/** One subtitle track from the playback API. */
export interface SubtitleSource {
  url: string;
  language: string;
  label: string | null;
}

/** Everything the playback API signed: the player loads only these. */
export interface PlaybackUrls {
  playlistUrl: string;
  thumbnailsUrl: string;
  subtitles: SubtitleSource[];
}

interface Props {
  urls: PlaybackUrls;
  posterUrl: string | null;
  title: string;
  /** The top bar's back arrow: the movie's page. */
  backTo: string;
  startAt: number | null; // seconds: ?t=, else the saved position
  /** Show "Resumed at 23:10 · Start over" (startAt came from the saved position). */
  resumed: boolean;
  /** Every timeupdate, with whether the video is playing. */
  onTime?: (sec: number, playing: boolean) => void;
  /** Pause, including the one at the end of the movie. */
  onPause?: (sec: number) => void;
  /**
   * Fresh signed URLs from the playback API, for when media-delivery answers
   * 410 (they expired while the movie played). They may point at a newer
   * version folder (a retranscode): playback goes on there.
   */
  refreshUrls: () => Promise<PlaybackUrls>;
}

/** What the player plays: the URLs, and where and how to (re)start. */
interface Source extends PlaybackUrls {
  startAt: number | null;
  autoplay: boolean;
}

const RENEW_FAILED = "Playback failed (the video link expired and could not be renewed).";
const NATIVE_HLS = "application/vnd.apple.mpegurl";
const MAX_NETWORK_RETRIES = 3;
/** Controls and cursor hide after this long without movement, while playing. */
const IDLE_MS = 3000;
const RESUME_TOAST_MS = 5000;
/** Two taps closer than this are a double tap. */
const DOUBLE_TAP_MS = 300;

type Engine = "hls.js" | "native" | "unsupported";

function detectEngine(): Engine {
  if (Hls.isSupported()) return "hls.js";
  // Safari on iOS has no MSE: it plays HLS itself (and picks quality itself).
  if (document.createElement("video").canPlayType(NATIVE_HLS) !== "") return "native";
  return "unsupported";
}

/** iOS Safari's own video fullscreen, where element fullscreen is missing. */
interface WebkitFullscreen {
  webkitEnterFullscreen: () => void;
}
function hasWebkitFullscreen(v: HTMLVideoElement): v is HTMLVideoElement & WebkitFullscreen {
  return typeof (v as HTMLVideoElement & Partial<WebkitFullscreen>).webkitEnterFullscreen === "function";
}

/**
 * Moves the cues of a showing track up while the controls show (line -4:
 * four lines from the bottom), and back when they hide. Only cues the file
 * left to the browser (line "auto") move; lifted remembers which.
 */
function liftCues(el: HTMLTrackElement, up: boolean, lifted: WeakSet<VTTCue>): void {
  const cues = el.track.cues;
  if (!cues) return;
  let moved = false;
  for (const c of Array.from(cues)) {
    if (!(c instanceof VTTCue)) continue;
    if (up && c.line === "auto") {
      c.line = -4;
      lifted.add(c);
      moved = true;
    } else if (!up && lifted.has(c)) {
      c.line = "auto";
      lifted.delete(c);
      moved = true;
    }
  }
  // A cue already on screen is redrawn only on the next time update; while
  // paused that never comes, so redraw now.
  if (moved && el.track.mode === "showing") {
    el.track.mode = "hidden";
    el.track.mode = "showing";
  }
}

async function fetchThumbnails(url: string): Promise<ThumbnailCue[]> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`thumbnails: HTTP ${String(res.status)}`);
  return parseThumbnails(await res.text(), new URL(url, window.location.href).href);
}

/**
 * The HLS player: hls.js where MSE exists, native HLS otherwise (Safari on
 * iOS; quality and some controls then come from the browser). Every URL
 * comes from the playback API. The parent keys it by the movie id.
 *
 * Expired URLs: a 410 from media-delivery (playlist or segment) fetches new
 * ones and reloads at the same position, playing if it was playing; at most
 * once a minute, and never for a 403 (lib/mediaExpiry.ts).
 *
 * Controls are icons only (each with a label and a tooltip); the keyboard
 * map is lib/playerKeys.ts; feedback over the video is lib/feedback.ts;
 * the viewer's volume, speed and subtitle choices are remembered in this
 * browser (lib/playerPrefs.ts).
 */
export function Player({ urls, posterUrl, title, backTo, startAt, resumed, onTime, onPause, refreshUrls }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const hlsRef = useRef<Hls | null>(null);
  const trackRefs = useRef<(HTMLTrackElement | null)[]>([]);
  const reduced = useReducedMotion();

  const [source, setSource] = useState<Source>({ ...urls, startAt, autoplay: false });
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
  const [selectedLevel, setSelectedLevel] = useState(-1);
  const [playingHeight, setPlayingHeight] = useState<number | null>(null);
  const [time, setTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [buffered, setBuffered] = useState(0);
  const [paused, setPaused] = useState(true);
  const [buffering, setBuffering] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [pip, setPip] = useState(false);
  const [prefs, setPrefs] = useState<PlayerPrefs>(loadPrefs);
  const [canSetVolume] = useState(() => volumeSettable(document.createElement("video")));
  const [pipSupported] = useState(() => "pictureInPictureEnabled" in document && document.pictureInPictureEnabled);
  const [menu, setMenu] = useState<MenuState>(CLOSED);
  const dispatchMenu = useCallback((e: MenuEvent) => {
    setMenu((m) => menuReduce(m, e));
  }, []);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [fb] = useState(() => new FeedbackState(setFeedback));
  useEffect(() => () => {
    fb.dispose();
  }, [fb]);
  const [active, setActive] = useState(true); // controls shown
  const [toast, setToast] = useState(resumed && startAt !== null && startAt > 0);

  // Subtitles: the tracks, named and grouped; the one showing (null: off);
  // the last one shown, which c and the CC button bring back.
  const tracks: Track[] = useMemo(
    () => source.subtitles.map((s, index) => ({ index, language: s.language, label: s.label })),
    [source.subtitles],
  );
  const groups = useMemo(() => groupTracks(tracks, browserNamer()), [tracks]);
  const [subtitle, setSubtitle] = useState<number | null>(() => trackForLanguage(tracks, prefs.subtitleLang));
  const [lastSubtitle, setLastSubtitle] = useState<number | null>(() => subtitle ?? (tracks.length > 0 ? 0 : null));

  // Previews are optional: a missing or broken index just means no sprites.
  const { data: cues = [] } = useQuery({
    queryKey: ["thumbnails", source.thumbnailsUrl],
    queryFn: () => fetchThumbnails(source.thumbnailsUrl),
    staleTime: Infinity,
    retry: false,
  });

  // The stored prefs reach the element, and every change is saved.
  useEffect(() => {
    savePrefs(prefs);
    const v = videoRef.current;
    if (!v) return;
    if (canSetVolume) v.volume = prefs.volume;
    v.muted = prefs.muted;
    v.playbackRate = prefs.speed;
    v.defaultPlaybackRate = prefs.speed;
  }, [prefs, canSetVolume]);

  // The chosen subtitle shows; the others are disabled (never fetched).
  useEffect(() => {
    trackRefs.current.forEach((el, i) => {
      if (el) el.track.mode = i === subtitle ? "showing" : "disabled";
    });
  }, [subtitle, source.subtitles]);

  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => {
      setToast(false);
    }, RESUME_TOAST_MS);
    return () => {
      window.clearTimeout(t);
    };
  }, [toast]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video || engine === "unsupported") return;
    const { playlistUrl: url, startAt: start, autoplay } = source;
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
            if (live) setSource({ ...u, startAt: at, autoplay: playing });
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
    const resumedPlay = () => {
      switching.current = false;
      if (autoplay) video.play().catch(() => undefined);
    };
    video.addEventListener("canplay", resumedPlay, { once: true });

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
        video.removeEventListener("canplay", resumedPlay);
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
      video.removeEventListener("canplay", resumedPlay);
      hls.destroy();
      hlsRef.current = null;
    };
  }, [engine, source, expiry]);

  // Fullscreen and picture-in-picture state follow the browser.
  useEffect(() => {
    const onFs = () => {
      setFullscreen(document.fullscreenElement === containerRef.current);
    };
    document.addEventListener("fullscreenchange", onFs);
    const v = videoRef.current;
    const onEnterPip = () => {
      setPip(true);
    };
    const onLeavePip = () => {
      setPip(false);
    };
    v?.addEventListener("enterpictureinpicture", onEnterPip);
    v?.addEventListener("leavepictureinpicture", onLeavePip);
    return () => {
      document.removeEventListener("fullscreenchange", onFs);
      v?.removeEventListener("enterpictureinpicture", onEnterPip);
      v?.removeEventListener("leavepictureinpicture", onLeavePip);
    };
  }, []);

  // Controls hide after IDLE_MS without movement while playing (and no menu).
  const idle = useRef<number | null>(null);
  const wake = useCallback(() => {
    setActive(true);
    if (idle.current !== null) window.clearTimeout(idle.current);
    idle.current = window.setTimeout(() => {
      idle.current = null;
      setActive(false);
    }, IDLE_MS);
  }, []);
  useEffect(() => () => {
    if (idle.current !== null) window.clearTimeout(idle.current);
  }, []);
  const shown = active || paused || menu.open || error !== null;

  // While the controls show, the subtitles move up above them: cues the
  // file left to the browser (line "auto") go to 4 lines from the bottom,
  // and back when the controls hide. Cues the file placed itself stay put.
  const lifted = useRef(new WeakSet<VTTCue>());
  useEffect(() => {
    const el = subtitle === null ? null : trackRefs.current[subtitle];
    if (!el) return;
    const apply = () => {
      liftCues(el, shown, lifted.current);
    };
    apply();
    // Cues arrive with the file (load); cuechange also covers a track whose
    // file loaded before this effect listened.
    el.addEventListener("load", apply);
    el.track.addEventListener("cuechange", apply);
    return () => {
      el.removeEventListener("load", apply);
      el.track.removeEventListener("cuechange", apply);
    };
  }, [shown, subtitle]);

  // --- actions ---------------------------------------------------------------

  const video = () => videoRef.current;
  const togglePlay = useCallback((flash: boolean) => {
    const v = videoRef.current;
    if (!v) return;
    if (v.paused) {
      void v.play().catch(() => undefined);
      if (flash) fb.playPause(true);
    } else {
      v.pause();
      if (flash) fb.playPause(false);
    }
  }, [fb]);
  const seek = useCallback((t: number) => {
    const v = videoRef.current;
    if (v) v.currentTime = Math.min(Math.max(t, 0), Number.isFinite(v.duration) ? v.duration : t);
  }, []);
  const skip = useCallback((seconds: number) => {
    const v = videoRef.current;
    if (!v) return;
    seek(v.currentTime + seconds);
    fb.skip(seconds);
  }, [fb, seek]);
  const setVolume = useCallback((v: number, show: boolean) => {
    setPrefs((p) => ({ ...p, volume: v, muted: v === 0 ? p.muted : false }));
    if (show) fb.volume(v, false);
  }, [fb]);
  const toggleMute = useCallback((show: boolean) => {
    setPrefs((p) => {
      const muted = !p.muted;
      if (show) fb.volume(p.volume, muted);
      return { ...p, muted };
    });
  }, [fb]);
  const chooseSubtitle = useCallback((index: number | null) => {
    setSubtitle(index);
    if (index !== null) setLastSubtitle(index);
    setPrefs((p) => ({ ...p, subtitleLang: index === null ? null : (tracks[index]?.language ?? null) }));
  }, [tracks]);
  const toggleSubtitles = useCallback(() => {
    chooseSubtitle(subtitle === null ? lastSubtitle : null);
  }, [chooseSubtitle, subtitle, lastSubtitle]);
  const toggleFullscreen = useCallback(() => {
    const c = containerRef.current;
    const v = videoRef.current;
    if (document.fullscreenElement) {
      void document.exitFullscreen();
    } else if (c && document.fullscreenEnabled) {
      void c.requestFullscreen().catch(() => undefined);
    } else {
      // iOS Safari: no element fullscreen, but the video's own (absent elsewhere).
      if (v && hasWebkitFullscreen(v)) v.webkitEnterFullscreen();
    }
  }, []);
  const togglePip = () => {
    const v = video();
    if (!v) return;
    if (document.pictureInPictureElement) void document.exitPictureInPicture();
    else void v.requestPictureInPicture().catch(() => undefined);
  };
  const selectLevel = (index: number) => {
    setSelectedLevel(index);
    const hls = hlsRef.current;
    if (hls) hls.currentLevel = index; // -1 = automatic; immediate switch
  };

  // The keyboard, page-wide while the player is on the page (lib/playerKeys).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target instanceof HTMLElement ? e.target : null;
      // Space and Enter on a focused control press that control.
      if ((e.key === " " || e.key === "Enter") && target?.closest("button, a, [role=slider], input[type=range]")) return;
      const action = playerKey({ key: e.key, ctrlKey: e.ctrlKey, metaKey: e.metaKey, altKey: e.altKey, target });
      if (!action) return;
      const v = videoRef.current;
      if (!v) return;
      if (action.kind === "escape") {
        if (menu.open) {
          e.preventDefault();
          dispatchMenu({ kind: "escape" });
        }
        return;
      }
      e.preventDefault();
      wake();
      switch (action.kind) {
        case "toggle-play":
          togglePlay(true);
          break;
        case "skip":
          skip(action.seconds);
          break;
        case "volume": {
          const level = stepVolume(prefs.muted ? 0 : prefs.volume, action.delta);
          if (canSetVolume) setVolume(level, true);
          else toggleMute(true);
          break;
        }
        case "toggle-mute":
          toggleMute(true);
          break;
        case "toggle-fullscreen":
          toggleFullscreen();
          break;
        case "toggle-subtitles":
          toggleSubtitles();
          break;
        case "jump":
          if (Number.isFinite(v.duration)) seek(v.duration * action.fraction);
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
    };
  }, [menu.open, dispatchMenu, wake, togglePlay, skip, prefs, canSetVolume, setVolume, toggleMute, toggleFullscreen, toggleSubtitles, seek]);

  // Taps and clicks on the picture (not on the controls).
  const lastTap = useRef<{ at: number; side: "left" | "middle" | "right" } | null>(null);
  const singleTap = useRef<number | null>(null);
  const onSurfaceUp = (e: React.PointerEvent<HTMLDivElement>) => {
    if (menu.open) {
      dispatchMenu({ kind: "close" });
      return;
    }
    if (e.pointerType === "mouse" || e.pointerType === "pen") {
      togglePlay(true); // a double-click toggles twice: no net change
      return;
    }
    // Touch: one tap shows or hides the controls; a double tap on the left
    // or right third skips (and further quick taps there keep skipping).
    const r = e.currentTarget.getBoundingClientRect();
    const x = (e.clientX - r.left) / r.width;
    const side = x < 1 / 3 ? "left" : x > 2 / 3 ? "right" : "middle";
    const now = e.timeStamp;
    const prev = lastTap.current;
    lastTap.current = { at: now, side };
    if (prev && now - prev.at < DOUBLE_TAP_MS && prev.side === side && side !== "middle") {
      if (singleTap.current !== null) window.clearTimeout(singleTap.current);
      singleTap.current = null;
      skip(side === "left" ? -SKIP_SEC : SKIP_SEC);
      return;
    }
    if (singleTap.current !== null) window.clearTimeout(singleTap.current);
    singleTap.current = window.setTimeout(() => {
      singleTap.current = null;
      if (shown && !paused) {
        if (idle.current !== null) window.clearTimeout(idle.current);
        setActive(false);
      } else wake();
    }, DOUBLE_TAP_MS);
  };

  const remaining = `${formatTime(time)} / ${formatTime(duration)}`;

  return (
    <div
      ref={containerRef}
      className={`group/player relative aspect-video w-full overflow-hidden bg-scrim select-none ${
        fullscreen ? "h-full" : "rounded-xl"
      } ${shown ? "" : "cursor-none"}`}
      onPointerMove={(e) => {
        if (e.pointerType !== "touch") wake();
      }}
      onFocus={wake}
    >
      <video
        ref={videoRef}
        className="h-full w-full"
        poster={posterUrl ?? undefined}
        playsInline
        data-cue-size={prefs.subtitleSize}
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
          wake();
        }}
        onPause={(e) => {
          if (switching.current) return;
          setPaused(true);
          onPause?.(e.currentTarget.currentTime);
        }}
        onWaiting={() => {
          setBuffering(true);
        }}
        onPlaying={() => {
          setBuffering(false);
        }}
        onCanPlay={() => {
          setBuffering(false);
        }}
        onSeeked={() => {
          setBuffering(false);
        }}
        onVolumeChange={(e) => {
          // The browser's own UI (fullscreen on iOS) may change these too.
          const v = e.currentTarget;
          setPrefs((p) => (p.muted === v.muted && (!canSetVolume || p.volume === v.volume) ? p : { ...p, muted: v.muted, volume: canSetVolume ? v.volume : p.volume }));
        }}
        onRateChange={(e) => {
          const r = e.currentTarget.playbackRate;
          setPrefs((p) => (p.speed === r ? p : { ...p, speed: r }));
        }}
      >
        {source.subtitles.map((s, i) => (
          <track
            key={s.url}
            ref={(el) => {
              trackRefs.current[i] = el;
            }}
            kind="subtitles"
            src={s.url}
            srcLang={s.language === "und" ? undefined : s.language}
            label={groups.flatMap((g) => g.tracks).find((c) => c.index === i)?.name ?? s.language}
          />
        ))}
      </video>

      {/* The picture's surface: clicks and taps (the controls sit above it). */}
      <div
        className="absolute inset-0"
        onPointerUp={onSurfaceUp}
        onDoubleClick={(e) => {
          if (e.detail === 2) toggleFullscreen();
        }}
      />

      <FeedbackOverlay feedback={feedback} />

      {buffering && !paused && !error && (
        <div role="status" aria-label="Loading" className="pointer-events-none absolute inset-0 grid place-items-center">
          <SpinnerIcon size={56} className="text-fg motion-safe:animate-spin" />
        </div>
      )}

      {paused && !buffering && !error && (
        <button
          type="button"
          aria-label="Play"
          onClick={() => {
            togglePlay(false);
          }}
          className="absolute top-1/2 left-1/2 grid size-20 -translate-x-1/2 -translate-y-1/2 place-items-center rounded-full bg-accent/90 text-on-accent shadow-2xl shadow-scrim/60 transition-colors duration-150 hover:bg-accent sm:size-24"
        >
          <PlayIcon size={reduced ? 36 : 40} className="translate-x-0.5" />
        </button>
      )}

      {error && (
        <div className="absolute inset-0 flex items-center justify-center bg-scrim/80 p-6 text-center text-danger">
          {error}
        </div>
      )}

      {toast && (
        <div
          role="status"
          className="absolute top-16 left-1/2 flex -translate-x-1/2 items-center gap-3 rounded-full bg-scrim/80 px-4 py-2 text-sm text-fg shadow-lg backdrop-blur-sm"
        >
          Resumed at {formatTime(startAt ?? 0)}
          <span aria-hidden="true" className="text-fg-subtle">·</span>
          <button
            type="button"
            className="font-semibold text-accent-soft hover:underline"
            onClick={() => {
              seek(0);
              setToast(false);
            }}
          >
            Start over
          </button>
        </div>
      )}

      {/* Top bar: back to the movie's page, and its title. The gradient lets
          taps through to the picture; only the controls take them. */}
      <div
        className={`pointer-events-none absolute inset-x-0 top-0 flex items-center gap-2 bg-gradient-to-b from-scrim/80 to-scrim/0 px-2 pt-2 pb-8 transition-opacity duration-200 sm:px-3 ${
          shown ? "opacity-100 [&>*]:pointer-events-auto" : "opacity-0"
        }`}
      >
        <Link
          to={backTo}
          aria-label="Back to the movie's page"
          className="grid size-10 place-items-center rounded-full text-fg transition-colors duration-150 hover:bg-fg/15"
        >
          <ArrowLeftIcon size={22} />
        </Link>
        <span className="truncate font-display text-base font-semibold text-fg sm:text-lg">{title}</span>
      </div>

      {/* Bottom: seek bar, then the control row. */}
      <div
        className={`pointer-events-none absolute inset-x-0 bottom-0 flex flex-col bg-gradient-to-t from-scrim/90 via-scrim/50 to-scrim/0 px-2 pt-12 pb-1.5 transition-opacity duration-200 sm:px-3 ${
          shown ? "opacity-100 [&>*]:pointer-events-auto" : "opacity-0"
        }`}
      >
        <div className="px-2">
          <SeekBar time={time} duration={duration} buffered={buffered} cues={cues} onSeek={seek} onSkip={skip} />
        </div>
        <div className="flex items-center gap-0.5 sm:gap-1">
          <ControlButton
            label={paused ? "Play" : "Pause"}
            tooltip={paused ? "Play (k)" : "Pause (k)"}
            tooltipAlign="start"
            onClick={() => {
              togglePlay(false);
            }}
          >
            {paused ? <PlayIcon size={22} /> : <PauseIcon size={22} />}
          </ControlButton>
          <ControlButton label={`Back ${String(SKIP_SEC)} seconds`} tooltip={`Back ${String(SKIP_SEC)} s (←)`} onClick={() => { skip(-SKIP_SEC); }}>
            <SkipBackIcon size={24} seconds={SKIP_SEC} />
          </ControlButton>
          <ControlButton label={`Forward ${String(SKIP_SEC)} seconds`} tooltip={`Forward ${String(SKIP_SEC)} s (→)`} onClick={() => { skip(SKIP_SEC); }}>
            <SkipForwardIcon size={24} seconds={SKIP_SEC} />
          </ControlButton>
          <VolumeControl
            volume={prefs.volume}
            muted={prefs.muted}
            settable={canSetVolume}
            onVolume={(v) => {
              setVolume(v, false);
            }}
            onToggleMute={() => {
              toggleMute(false);
            }}
          />
          <span className="ml-1 text-xs whitespace-nowrap text-fg tabular-nums sm:text-sm">{remaining}</span>

          <span className="ml-auto" />
          {tracks.length > 0 && (
            <ControlButton
              label={subtitle === null ? "Subtitles on" : "Subtitles off"}
              tooltip={subtitle === null ? "Subtitles (c)" : "Subtitles off (c)"}
              aria-pressed={subtitle !== null}
              onClick={toggleSubtitles}
            >
              <SubtitlesIcon size={22} on={subtitle !== null} />
            </ControlButton>
          )}
          <div className="relative">
            <ControlButton
              label="Settings"
              aria-expanded={menu.open}
              aria-haspopup="dialog"
              onClick={() => {
                dispatchMenu({ kind: "toggle" });
              }}
            >
              <GearIcon size={22} className={`transition-transform duration-200 ${menu.open ? "motion-safe:rotate-45" : ""}`} />
            </ControlButton>
          </div>
          {pipSupported && (
            <ControlButton label={pip ? "Exit picture-in-picture" : "Picture-in-picture"} onClick={togglePip} className="hidden sm:grid">
              <PipIcon size={22} />
            </ControlButton>
          )}
          <ControlButton
            label={fullscreen ? "Exit fullscreen" : "Fullscreen"}
            tooltip={fullscreen ? "Exit fullscreen (f)" : "Fullscreen (f)"}
            tooltipAlign="end"
            onClick={toggleFullscreen}
          >
            {fullscreen ? <CollapseIcon size={22} /> : <ExpandIcon size={22} />}
          </ControlButton>
        </div>
      </div>

      {menu.open && (
        <SettingsMenu
          state={menu}
          dispatch={dispatchMenu}
          levels={levels}
          selectedLevel={selectedLevel}
          playingHeight={playingHeight}
          onLevel={selectLevel}
          groups={groups}
          subtitle={subtitle}
          onSubtitle={chooseSubtitle}
          size={prefs.subtitleSize}
          onSize={(s) => {
            setPrefs((p) => ({ ...p, subtitleSize: s }));
          }}
          speed={prefs.speed}
          onSpeed={(s) => {
            setPrefs((p) => ({ ...p, speed: s }));
          }}
        />
      )}
    </div>
  );
}
