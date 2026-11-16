import type { MovieStatus } from "../api/client";
import { STATUS_LABELS } from "../lib/catalogParams";

const STYLES: Record<MovieStatus, string> = {
  ready: "bg-success/15 text-success",
  processing: "bg-accent/20 text-accent-soft",
  pending: "bg-warning/15 text-warning",
  failed: "bg-danger/15 text-danger",
  deleting: "bg-overlay text-fg-muted",
};

export function StatusBadge({ status }: { status: MovieStatus }) {
  return (
    <span className={`inline-block rounded px-1.5 py-0.5 text-xs font-medium ${STYLES[status]}`}>
      {STATUS_LABELS[status]}
    </span>
  );
}
