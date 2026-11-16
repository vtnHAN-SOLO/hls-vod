import { brand } from "../../brand";
import { wordmarkParts } from "../../lib/brand";

/**
 * The site's wordmark: the logo (a dog, /logo-64.png from web/public, same
 * origin) followed by the name in the display font.
 *
 * With the name shown the image is decorative (alt=""): the text beside it
 * names the site. `compact` (the phone header, where there is no room for
 * the name) shows the logo alone, so there the image carries the name.
 */
export function Wordmark({
  className = "",
  compact = false,
  size = 36,
}: {
  className?: string;
  compact?: boolean;
  /** The logo's size in CSS pixels; the 64 px file stays sharp up to ~1.8x screens at 36. */
  size?: number;
}) {
  const { head, accent } = wordmarkParts(brand);
  return (
    <span className={`inline-flex items-center gap-2 ${className}`}>
      <img
        src="/logo-64.png"
        alt={compact ? brand.name : ""}
        width={size}
        height={size}
        decoding="async"
        draggable={false}
        className="shrink-0 select-none"
      />
      {!compact && (
        <span className="font-display leading-none font-bold tracking-wide whitespace-nowrap text-fg">
          {head}
          {accent && <span className="font-medium text-accent-soft">{accent}</span>}
        </span>
      )}
    </span>
  );
}
