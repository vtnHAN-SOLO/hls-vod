// The admin area's rules, kept out of the page so they are tested: which
// tab, which buttons a row gets, what each confirm dialog says, and the
// sentence for each refusal. The api decides in the end (the role check,
// self_change, last_admin): the page shows its answer plainly.
import { type AdminMovie, type AdminUser, ApiError, type Job } from "../api/client";
import { describeError } from "./errors";

export const ADMIN_TABS = ["users", "jobs", "movies"] as const;
export type AdminTab = (typeof ADMIN_TABS)[number];

/** Jobs and Movies refresh this often while the browser tab is visible. */
export const POLL_MS = 10_000;

/** The tab named by ?tab=, users when it names none. */
export function tabFrom(param: string | null): AdminTab {
  return ADMIN_TABS.find((t) => t === param) ?? "users";
}

/** Retry only a failed movie with nothing queued or running. */
export function canRetry(m: AdminMovie): boolean {
  return m.status === "failed" && m.active_job === null;
}

/** Retranscode a ready movie with nothing queued or running; it keeps playing meanwhile. */
export function canRetranscode(m: AdminMovie): boolean {
  return m.status === "ready" && m.active_job === null;
}

/** "retranscoding" while a retranscode job is queued or running, else null. */
export function retranscodeNote(m: AdminMovie): string | null {
  if (m.active_job_purpose !== "retranscode") return null;
  return m.active_job === "running" ? "retranscoding" : "retranscode queued";
}

export function confirmRetranscode(m: AdminMovie): string {
  return `Retranscode “${m.title}”? It keeps playing its current version until the new one is ready; the old version is removed about a day later.`;
}

/** Delete anything without an active job that is not already being deleted. */
export function canDelete(m: AdminMovie): boolean {
  return m.active_job === null && m.status !== "deleting";
}

export type UserAction = { kind: "disable" } | { kind: "enable" } | { kind: "role"; role: "admin" | "user" };

/** The two buttons of a user row: disable or enable, and the other role. */
export function userActions(u: AdminUser): [UserAction, UserAction] {
  return [
    u.disabled_at === null ? { kind: "disable" } : { kind: "enable" },
    { kind: "role", role: u.role === "admin" ? "user" : "admin" },
  ];
}

export function actionLabel(a: UserAction): string {
  switch (a.kind) {
    case "disable":
      return "Disable";
    case "enable":
      return "Enable";
    case "role":
      return a.role === "admin" ? "Make admin" : "Make user";
  }
}

/** The question the confirm dialog asks before the action runs. */
export function confirmUserAction(a: UserAction, u: AdminUser): string {
  switch (a.kind) {
    case "disable":
      return `Disable ${u.username}? Every session of theirs ends now (an open tab within 15 minutes), and they cannot log in until enabled.`;
    case "enable":
      return `Enable ${u.username}? They can log in again.`;
    case "role":
      return a.role === "admin"
        ? `Make ${u.username} an admin? They can then manage users and movies.`
        : `Make ${u.username} a user? They lose the admin area at once.`;
  }
}

export function confirmDelete(m: AdminMovie): string {
  return `Delete “${m.title}”? It disappears for users at once; its files and watch progress are removed within 5 minutes. This cannot be undone.`;
}

/** Where a job runs: the Kubernetes Job or the local process. */
export function jobBackend(j: Job): string {
  if (j.k8s_job_name !== null) return `Job ${j.k8s_job_name}`;
  if (j.local_pid !== null) return `pid ${String(j.local_pid)}`;
  return "—";
}

/** A sentence for a refused admin action. */
export function describeAdminError(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "last_admin":
        return "That is the last active admin. Make someone else an admin first.";
      case "self_change":
        return "You cannot disable your own account or change your own role.";
      case "not_failed":
        return "Only a failed movie can be retried. It may already be queued.";
      case "job_active":
        return "This movie has a queued or running transcode job. Wait for it to finish.";
      case "forbidden":
        return "Only admins can do this. Your admin role may have been removed.";
      case "not_found":
        return "It no longer exists. The list was out of date.";
      default:
        break;
    }
  }
  return describeError(err);
}
