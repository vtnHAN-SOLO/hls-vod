import { useState } from "react";

import { type Poster, posterSrc, posterSrcSet } from "../lib/poster";

interface BackdropProps {
  url: string | null;
  title: string;
  className?: string;
  /** Eager for the first, visible images (the spotlight); lazy otherwise. */
  eager?: boolean;
  /**
   * The image failed to load. Its signed URL may have expired while the
   * page was open: the page refetches its data once (lib/mediaExpiry.ts
   * once()), and a new url remounts this image, which tries again.
   */
  onFailed?: () => void;
}

/** The title's initial on a soft gradient: never a broken-image icon. */
function Placeholder({ title, className }: { title: string; className: string }) {
  return (
    <div
      role="img"
      aria-label={title}
      className={`${className} flex items-center justify-center bg-gradient-to-br from-overlay via-raised to-accent/25`}
    >
      <span aria-hidden="true" className="font-display text-4xl font-semibold text-fg-subtle select-none">
        {title.trim().charAt(0).toUpperCase() || "?"}
      </span>
    </div>
  );
}

/**
 * The movie's backdrop: the landscape video frame the transcoder made, or
 * a placeholder when there is none or it fails to load. Decorative (alt=""):
 * the title is always shown next to it. Key it by url so a new url retries.
 */
export function Backdrop({ url, title, className = "", eager = false, onFailed }: BackdropProps) {
  const [failed, setFailed] = useState(false);
  const base = `aspect-video w-full object-cover ${className}`;
  if (url === null || failed) return <Placeholder title={title} className={base} />;
  return (
    <img
      src={url}
      alt=""
      loading={eager ? "eager" : "lazy"}
      decoding="async"
      className={`${base} bg-raised`}
      onError={() => {
        setFailed(true);
        onFailed?.();
      }}
    />
  );
}

/**
 * The movie's portrait poster, 2:3, in a box of that shape whether or not
 * the image has loaded, so nothing below it moves. The browser picks the
 * variant from srcset and sizes (the width the layout gives it). alt is the
 * title. Key it by poster hash so a new upload, or a refetched URL, retries.
 */
export function PosterImage({
  poster,
  title,
  sizes,
  className = "",
  imgClassName = "",
  eager = false,
  onFailed,
}: {
  poster: Poster | null;
  title: string;
  /** One of POSTER_SIZES. */
  sizes: string;
  className?: string;
  imgClassName?: string;
  eager?: boolean;
  onFailed?: () => void;
}) {
  const [failed, setFailed] = useState(false);
  const box = `relative aspect-[2/3] w-full overflow-hidden bg-raised ${className}`;
  if (poster === null || failed) {
    return (
      <div className={box}>
        <Placeholder title={title} className="absolute inset-0 h-full w-full" />
      </div>
    );
  }
  return (
    <div className={box}>
      <img
        src={posterSrc(poster)}
        srcSet={posterSrcSet(poster)}
        sizes={sizes}
        alt={title}
        width={2}
        height={3}
        loading={eager ? "eager" : "lazy"}
        decoding="async"
        className={`absolute inset-0 h-full w-full object-cover ${imgClassName}`}
        onError={() => {
          setFailed(true);
          onFailed?.();
        }}
      />
    </div>
  );
}
