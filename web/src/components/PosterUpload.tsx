import { useId, useRef, useState } from "react";

import { type AdminMovie, adminDeletePoster, adminUploadPoster } from "../api/client";
import { describeAdminError } from "../lib/admin";
import {
  checkPosterDimensions,
  checkPosterFile,
  cropFractions,
  percent,
  POSTER_ACCEPT,
  POSTER_SIZES,
} from "../lib/poster";
import { PosterImage } from "./Poster";
import { Button } from "./ui/Button";
import { ImageIcon, TrashIcon } from "./ui/Icons";

interface Preview {
  url: string; // a data: URL of the 2:3 crop (the CSP allows data:, not blob:)
  width: number;
  height: number;
}

/**
 * The crop the api will keep, drawn small: the centred 2:3 part of the
 * image. Decoding here also reads its size, so a too-small image is refused
 * before a byte is sent.
 */
async function previewOf(file: File): Promise<Preview> {
  const bmp = await createImageBitmap(file);
  try {
    const c = cropFractions(bmp.width, bmp.height);
    const canvas = document.createElement("canvas");
    canvas.width = 300;
    canvas.height = 450;
    const ctx = canvas.getContext("2d");
    if (ctx) {
      ctx.drawImage(bmp, c.x * bmp.width, c.y * bmp.height, c.w * bmp.width, c.h * bmp.height, 0, 0, 300, 450);
    }
    return { url: canvas.toDataURL("image/jpeg", 0.85), width: bmp.width, height: bmp.height };
  } finally {
    bmp.close();
  }
}

type State =
  | { kind: "idle" }
  | { kind: "uploading"; preview: Preview; pct: number }
  | { kind: "error"; message: string };

/**
 * The Edit dialog's poster: the current one, a drop zone and a file picker.
 * A chosen image is checked (type, 10 MB, 300×450, 25 MP), previewed as the
 * 2:3 crop the api keeps, and uploaded at once with a progress bar; the api
 * checks it again from its bytes. onChange gets the movie with its new
 * poster.
 */
export function PosterUpload({ movie, onChange }: { movie: AdminMovie; onChange: (m: AdminMovie) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const hintId = useId();
  const [state, setState] = useState<State>({ kind: "idle" });
  const [over, setOver] = useState(false);
  const [removing, setRemoving] = useState(false);
  const busy = state.kind === "uploading" || removing;
  const published = movie.published_at !== null;

  const take = async (file: File | undefined) => {
    if (!file || busy) return;
    const bad = checkPosterFile(file);
    if (bad) {
      setState({ kind: "error", message: bad });
      return;
    }
    let preview: Preview;
    try {
      preview = await previewOf(file);
    } catch {
      setState({ kind: "error", message: "This image could not be read. Choose a JPEG, PNG or WebP image." });
      return;
    }
    const tooSmall = checkPosterDimensions(preview.width, preview.height);
    if (tooSmall) {
      setState({ kind: "error", message: tooSmall });
      return;
    }
    setState({ kind: "uploading", preview, pct: 0 });
    try {
      const m = await adminUploadPoster(movie.id, file, (loaded, total) => {
        setState({ kind: "uploading", preview, pct: percent(loaded, total) });
      });
      setState({ kind: "idle" });
      onChange(m);
    } catch (err) {
      setState({ kind: "error", message: describeAdminError(err) });
    }
  };

  const remove = async () => {
    setRemoving(true);
    try {
      await adminDeletePoster(movie.id);
      onChange({ ...movie, poster: null });
      setState({ kind: "idle" });
    } catch (err) {
      setState({ kind: "error", message: describeAdminError(err) });
    } finally {
      setRemoving(false);
    }
  };

  return (
    <div className="flex flex-col gap-1.5 text-sm">
      <span className="font-medium text-fg">Poster</span>
      <div className="flex gap-4">
        <div className="w-28 shrink-0 overflow-hidden rounded-lg ring-1 ring-line">
          {state.kind === "uploading" ? (
            <img src={state.preview.url} alt="Preview of the new poster" className="aspect-[2/3] w-full object-cover opacity-70" />
          ) : (
            <PosterImage key={movie.poster?.hash} poster={movie.poster} title={movie.title} sizes={POSTER_SIZES.ranked} />
          )}
        </div>
        <div className="flex min-w-0 flex-1 flex-col gap-2">
          <button
            type="button"
            disabled={busy}
            aria-describedby={hintId}
            onClick={() => input.current?.click()}
            onDragOver={(e) => {
              e.preventDefault();
              setOver(true);
            }}
            onDragLeave={() => {
              setOver(false);
            }}
            onDrop={(e) => {
              e.preventDefault();
              setOver(false);
              void take(e.dataTransfer.files[0]);
            }}
            className={`flex flex-1 flex-col items-center justify-center gap-1.5 rounded-lg border border-dashed px-3 py-4 text-center transition-colors duration-150 disabled:opacity-60 ${
              over ? "border-accent bg-accent/10 text-fg" : "border-line-strong text-fg-muted hover:border-accent hover:text-fg"
            }`}
          >
            <ImageIcon size={20} />
            <span>
              {movie.poster ? "Replace: drop an image or " : "Drop an image or "}
              <span className="text-accent-soft underline">choose a file</span>
            </span>
          </button>
          <span id={hintId} className="text-xs text-fg-subtle">
            JPEG, PNG or WebP, up to 10 MB, at least 300×450. Cropped to 2:3 from the centre.
          </span>
          <input
            ref={input}
            type="file"
            accept={POSTER_ACCEPT}
            className="sr-only"
            tabIndex={-1}
            aria-hidden="true"
            onChange={(e) => {
              void take(e.target.files?.[0]);
              e.target.value = ""; // the same file again is a new choice
            }}
          />
          {state.kind === "uploading" && (
            <div className="flex items-center gap-2 text-xs text-fg-muted">
              <div
                role="progressbar"
                aria-label="Uploading the poster"
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={state.pct}
                className="h-1.5 flex-1 overflow-hidden rounded-full bg-fg/15"
              >
                <div className="h-full rounded-full bg-accent transition-[width] duration-150" style={{ width: `${String(state.pct)}%` }} />
              </div>
              {state.pct < 100 ? `${String(state.pct)}%` : "Processing…"}
            </div>
          )}
          {state.kind === "error" && (
            <span role="alert" className="text-xs text-danger">
              {state.message}
            </span>
          )}
          {movie.poster && (
            <Button
              size="sm"
              variant="ghost"
              className="self-start text-danger"
              disabled={busy || published}
              title={published ? "A published movie keeps its poster: unpublish it first." : undefined}
              onClick={() => void remove()}
            >
              <TrashIcon size={14} />
              Remove poster
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
