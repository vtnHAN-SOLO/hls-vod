import type { ButtonHTMLAttributes, ReactNode } from "react";

/**
 * A player control: an icon only, named by aria-label, with a tooltip on
 * hover and keyboard focus. The tooltip repeats the label for sighted
 * users and is hidden from screen readers (they read the label).
 */
export function ControlButton({
  label,
  tooltip,
  tooltipAlign = "center",
  children,
  className = "",
  ...rest
}: {
  label: string;
  /** Defaults to the label; may add the shortcut ("Mute (m)"). */
  tooltip?: string;
  tooltipAlign?: "start" | "center" | "end";
  children: ReactNode;
} & Omit<ButtonHTMLAttributes<HTMLButtonElement>, "aria-label">) {
  const align =
    tooltipAlign === "start" ? "left-0" : tooltipAlign === "end" ? "right-0" : "left-1/2 -translate-x-1/2";
  return (
    <button
      type="button"
      aria-label={label}
      className={`group/btn relative grid size-10 shrink-0 place-items-center rounded-full text-fg transition-colors duration-150 hover:bg-fg/15 focus-visible:bg-fg/15 ${className}`}
      {...rest}
    >
      {children}
      <span
        aria-hidden="true"
        className={`pointer-events-none absolute bottom-full mb-2 hidden rounded-md bg-scrim/85 px-2 py-1 text-xs font-medium whitespace-nowrap text-fg group-hover/btn:block group-focus-visible/btn:block ${align}`}
      >
        {tooltip ?? label}
      </span>
    </button>
  );
}
