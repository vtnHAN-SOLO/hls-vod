import type { MovieStatus } from "../api/client";
import { STATUS_LABELS } from "../lib/catalogParams";

const STYLES: Record<MovieStatus, string> = {
  ready: "bg-emerald-500/15 text-emerald-300",
  processing: "bg-sky-500/15 text-sky-300",
  pending: "bg-amber-500/15 text-amber-300",
  failed: "bg-red-500/15 text-red-300",
  deleting: "bg-neutral-500/20 text-neutral-400",
};

export function StatusBadge({ status }: { status: MovieStatus }) {
  return (
    <span className={`inline-block rounded px-1.5 py-0.5 text-xs font-medium ${STYLES[status]}`}>
      {STATUS_LABELS[status]}
    </span>
  );
}
