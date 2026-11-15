import { useState } from "react";

interface Props {
  url: string | null;
  title: string;
  className?: string;
  /**
   * The image failed to load. Its signed URL may have expired while the
   * page was open: the page refetches its data once (lib/mediaExpiry.ts
   * once()), and a new url remounts this poster, which tries again.
   */
  onFailed?: () => void;
}

/**
 * The movie's poster frame, or a plain placeholder when there is none or it
 * fails to load: never a broken-image icon. Decorative (alt=""): the title is
 * always shown next to it. Key it by url so a new url retries.
 */
export function Poster({ url, title, className = "", onFailed }: Props) {
  const [failed, setFailed] = useState(false);
  const base = `aspect-video w-full rounded-md bg-neutral-800 object-cover ${className}`;

  if (url === null || failed) {
    return (
      <div aria-hidden className={`${base} flex items-center justify-center`}>
        <span className="text-3xl font-semibold text-neutral-600 select-none">
          {title.trim().charAt(0).toUpperCase() || "?"}
        </span>
      </div>
    );
  }
  return (
    <img
      src={url}
      alt=""
      loading="lazy"
      decoding="async"
      className={base}
      onError={() => {
        setFailed(true);
        onFailed?.();
      }}
    />
  );
}
