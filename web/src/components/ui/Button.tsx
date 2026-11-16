import type { ButtonHTMLAttributes } from "react";
import { Link, type LinkProps } from "react-router";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
export type ButtonSize = "sm" | "md" | "lg";

const VARIANTS: Record<ButtonVariant, string> = {
  primary: "bg-accent text-on-accent hover:bg-accent-hover",
  secondary: "bg-overlay text-fg hover:bg-overlay-hover border border-line",
  ghost: "text-fg-muted hover:bg-overlay hover:text-fg",
  danger: "bg-danger-strong text-on-accent hover:bg-danger-strong-hover",
};

const SIZES: Record<ButtonSize, string> = {
  sm: "h-8 gap-1.5 px-3 text-sm",
  md: "h-10 gap-2 px-4 text-sm",
  lg: "h-12 gap-2.5 px-6 text-base",
};

/** The classes of a button, for the rare control that is neither a <button> nor a <Link>. */
export function buttonClass(variant: ButtonVariant = "secondary", size: ButtonSize = "md", extra = ""): string {
  return [
    "inline-flex shrink-0 items-center justify-center rounded-lg font-medium whitespace-nowrap select-none",
    "transition-colors duration-150 disabled:pointer-events-none disabled:opacity-50",
    VARIANTS[variant],
    SIZES[size],
    extra,
  ].join(" ");
}

interface Common {
  variant?: ButtonVariant;
  size?: ButtonSize;
}

export function Button({
  variant,
  size,
  className = "",
  type = "button",
  ...rest
}: Common & ButtonHTMLAttributes<HTMLButtonElement>) {
  // type defaults to "button": a <button> inside a form never submits by accident.
  return <button type={type} className={buttonClass(variant, size, className)} {...rest} />;
}

/** A link that looks like a button (navigation stays a link: middle-click, open in new tab). */
export function ButtonLink({ variant, size, className = "", ...rest }: Common & LinkProps) {
  return <Link className={buttonClass(variant, size, className)} {...rest} />;
}
