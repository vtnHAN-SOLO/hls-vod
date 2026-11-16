// Small building blocks: Badge, Skeleton, EmptyState, Panel, Field.
import type { ReactNode } from "react";

export type Tone = "neutral" | "accent" | "success" | "warning" | "danger";

const TONES: Record<Tone, string> = {
  neutral: "bg-overlay text-fg-muted",
  accent: "bg-accent/20 text-accent-soft",
  success: "bg-success/15 text-success",
  warning: "bg-warning/15 text-warning",
  danger: "bg-danger/15 text-danger",
};

export function Badge({ tone = "neutral", children, className = "" }: { tone?: Tone; children: ReactNode; className?: string }) {
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-semibold tracking-wide ${TONES[tone]} ${className}`}
    >
      {children}
    </span>
  );
}

/** A grey placeholder shape while data loads. Pulses only without reduced motion. */
export function Skeleton({ className = "" }: { className?: string }) {
  return <div aria-hidden="true" className={`rounded-md bg-raised motion-safe:animate-pulse ${className}`} />;
}

/** A calm message where content would be: an icon, a sentence, maybe one action. */
export function EmptyState({
  icon,
  title,
  children,
  action,
  className = "",
}: {
  icon?: ReactNode;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={`flex flex-col items-center gap-3 rounded-xl border border-dashed border-line px-6 py-14 text-center ${className}`}
    >
      {icon && <div className="text-fg-subtle">{icon}</div>}
      <p className="font-display text-base font-semibold text-fg">{title}</p>
      {children && <div className="max-w-md text-sm text-fg-muted">{children}</div>}
      {action}
    </div>
  );
}

/** A raised box for forms and settings sections. */
export function Panel({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <section className={`rounded-xl border border-line bg-surface p-5 sm:p-6 ${className}`}>{children}</section>;
}

/** An alert line under a form: errors in danger, notices in success. */
export function Notice({ tone, children }: { tone: "danger" | "success" | "muted"; children: ReactNode }) {
  const cls = tone === "danger" ? "text-danger" : tone === "success" ? "text-success" : "text-fg-muted";
  return (
    <p role={tone === "danger" ? "alert" : "status"} className={`text-sm ${cls}`}>
      {children}
    </p>
  );
}
