import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";

import {
  type AdminMovie,
  type AdminUser,
  adminDeleteMovie,
  adminListJobs,
  adminListMovies,
  adminListUsers,
  adminRetranscodeMovie,
  adminRetryMovie,
  adminSetDisabled,
  adminSetRole,
  ApiError,
  type JobPurpose,
  type JobStatus,
  type MovieStatus,
} from "../api/client";
import { useUser } from "../auth/AuthProvider";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { StatusBadge } from "../components/StatusBadge";
import {
  ADMIN_TABS,
  type AdminTab,
  actionLabel,
  canDelete,
  canRetranscode,
  canRetry,
  confirmDelete,
  confirmRetranscode,
  confirmUserAction,
  describeAdminError,
  jobBackend,
  POLL_MS,
  retranscodeNote,
  tabFrom,
  type UserAction,
  userActions,
} from "../lib/admin";
import { isMovieStatus, STATUS_LABELS } from "../lib/catalogParams";

const PAGE = 50;
const TAB_LABELS: Record<AdminTab, string> = { users: "Users", jobs: "Jobs", movies: "Movies" };
const JOB_STATUSES: JobStatus[] = ["queued", "running", "succeeded", "failed"];
const PURPOSE_LABELS: Record<JobPurpose, string> = { ingest: "Ingest", retranscode: "Retranscode" };
const JOB_LABELS: Record<JobStatus, string> = {
  queued: "Queued",
  running: "Running",
  succeeded: "Succeeded",
  failed: "Failed",
};
const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });
const fieldClass = "rounded-md border border-white/10 bg-neutral-900 px-3 py-2 text-sm outline-none focus:border-white/40";
const buttonClass = "rounded-md bg-white/10 px-2.5 py-1 text-xs hover:bg-white/20 disabled:opacity-40";

function when(iso: string | null) {
  return iso === null ? "—" : <time dateTime={iso}>{dateTime.format(new Date(iso))}</time>;
}

/** /admin: users, transcode jobs and movies. The api checks the role on every call. */
export function AdminPage() {
  const user = useUser();
  const [params, setParams] = useSearchParams();
  const tab = tabFrom(params.get("tab"));
  if (user.role !== "admin") return <Forbidden />;
  return (
    <main className="mx-auto flex max-w-7xl flex-col gap-6 px-4 py-6 sm:px-6">
      <h1 className="text-2xl font-semibold">Admin</h1>
      <nav aria-label="Admin sections" className="flex gap-1 border-b border-white/10">
        {ADMIN_TABS.map((t) => (
          <button
            key={t}
            type="button"
            aria-current={t === tab ? "page" : undefined}
            onClick={() => {
              setParams({ tab: t });
            }}
            className={`-mb-px border-b-2 px-4 py-2 text-sm ${
              t === tab ? "border-white font-medium" : "border-transparent text-neutral-400 hover:text-neutral-200"
            }`}
          >
            {TAB_LABELS[t]}
          </button>
        ))}
      </nav>
      {tab === "users" && <UsersTab />}
      {tab === "jobs" && <JobsTab />}
      {tab === "movies" && <MoviesTab />}
    </main>
  );
}

function Forbidden() {
  return (
    <main className="mx-auto flex max-w-7xl flex-col items-center gap-3 px-6 py-24 text-center">
      <h1 className="text-lg font-semibold">403: admins only</h1>
      <p className="text-neutral-300">This page is for admins.</p>
      <Link to="/" className="rounded-md bg-white/10 px-3 py-1.5 text-sm hover:bg-white/20">
        Browse movies
      </Link>
    </main>
  );
}

/** A failed list: a demoted admin's 403 says so plainly. */
function LoadError({ error }: { error: unknown }) {
  if (error instanceof ApiError && error.code === "forbidden") {
    return <p className="text-sm text-red-300">You are no longer an admin. Log out and in again to refresh your role.</p>;
  }
  return <p className="text-sm text-red-300">{describeAdminError(error)}</p>;
}

function Alert({ message }: { message: string | null }) {
  if (message === null) return null;
  return (
    <p role="alert" className="text-sm text-red-300">
      {message}
    </p>
  );
}

function Pager({ offset, total, onChange }: { offset: number; total: number; onChange: (o: number) => void }) {
  if (total <= PAGE) return null;
  return (
    <div className="flex items-center gap-3 text-sm text-neutral-400">
      <button type="button" className={buttonClass} disabled={offset === 0} onClick={() => { onChange(Math.max(0, offset - PAGE)); }}>
        Previous
      </button>
      <span>
        {offset + 1}–{Math.min(offset + PAGE, total)} of {total}
      </span>
      <button type="button" className={buttonClass} disabled={offset + PAGE >= total} onClick={() => { onChange(offset + PAGE); }}>
        Next
      </button>
    </div>
  );
}

function UsersTab() {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState("");
  const [q, setQ] = useState("");
  const [offset, setOffset] = useState(0);
  const [pending, setPending] = useState<{ action: UserAction; user: AdminUser } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const users = useQuery({
    queryKey: ["admin", "users", q, offset],
    queryFn: () => adminListUsers({ q: q || undefined, limit: PAGE, offset }),
    placeholderData: keepPreviousData,
  });
  const act = useMutation({
    mutationFn: ({ action, user }: { action: UserAction; user: AdminUser }) =>
      action.kind === "role" ? adminSetRole(user.id, action.role) : adminSetDisabled(user.id, action.kind === "disable"),
    onSuccess: () => {
      setError(null);
    },
    onError: (err) => {
      setError(describeAdminError(err));
    },
    onSettled: () => {
      setPending(null);
      void queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
    },
  });

  return (
    <section className="flex flex-col gap-4">
      <form
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
          setQ(draft.trim());
          setOffset(0);
        }}
        className="flex flex-wrap gap-3"
      >
        <input
          type="search"
          aria-label="Search users"
          placeholder="Username or email"
          maxLength={100}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
          }}
          className={`${fieldClass} w-full sm:w-72`}
        />
        <button type="submit" className="rounded-md bg-white px-4 py-2 text-sm font-medium text-neutral-950 hover:bg-white/90">
          Search
        </button>
      </form>
      <Alert message={error} />
      {users.isPending ? (
        <div className="h-32 animate-pulse rounded-md bg-neutral-900" aria-label="Loading users" />
      ) : users.isError ? (
        <LoadError error={users.error} />
      ) : (
        <>
          <div className="overflow-x-auto rounded-md border border-white/10">
            <table className="w-full text-left text-sm">
              <thead className="bg-white/5 text-xs text-neutral-400">
                <tr>
                  <th className="px-3 py-2 font-medium">User</th>
                  <th className="px-3 py-2 font-medium">Email</th>
                  <th className="px-3 py-2 font-medium">Role</th>
                  <th className="px-3 py-2 font-medium">Sign-in</th>
                  <th className="px-3 py-2 font-medium">Status</th>
                  <th className="px-3 py-2 font-medium">Created</th>
                  <th className="px-3 py-2 font-medium">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-white/10">
                {users.data.items.map((u) => (
                  <tr key={u.id} className={u.disabled_at === null ? "" : "text-neutral-500"}>
                    <td className="px-3 py-2 font-medium">{u.username}</td>
                    <td className="px-3 py-2">
                      <span className="break-all">{u.email ?? "—"}</span>
                      {u.email !== null && (
                        <span className="ml-1 text-xs text-neutral-500">
                          {u.email_verified_at === null ? "(unverified)" : "(verified)"}
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-2">{u.role === "admin" ? "Admin" : "User"}</td>
                    <td className="px-3 py-2 text-xs">
                      {[u.has_password && "Password", u.google_linked && "Google"].filter(Boolean).join(" · ") || "—"}
                    </td>
                    <td className="px-3 py-2 text-xs">{u.disabled_at === null ? "Active" : <>Disabled {when(u.disabled_at)}</>}</td>
                    <td className="px-3 py-2 text-xs">{when(u.created_at)}</td>
                    <td className="px-3 py-2">
                      <span className="flex justify-end gap-2">
                        {userActions(u).map((a) => (
                          <button
                            key={a.kind}
                            type="button"
                            className={buttonClass}
                            onClick={() => {
                              setError(null);
                              setPending({ action: a, user: u });
                            }}
                          >
                            {actionLabel(a)}
                          </button>
                        ))}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {users.data.total === 0 && <p className="text-sm text-neutral-400">No user matches.</p>}
          <Pager offset={offset} total={users.data.total} onChange={setOffset} />
        </>
      )}
      {pending && (
        <ConfirmDialog
          message={confirmUserAction(pending.action, pending.user)}
          confirmLabel={actionLabel(pending.action)}
          danger={pending.action.kind === "disable"}
          pending={act.isPending}
          onConfirm={() => {
            act.mutate(pending);
          }}
          onCancel={() => {
            setPending(null);
          }}
        />
      )}
    </section>
  );
}

function JobsTab() {
  const [status, setStatus] = useState<JobStatus | "">("");
  const [offset, setOffset] = useState(0);
  // refetchIntervalInBackground is off: no polling while the browser tab is hidden.
  const jobs = useQuery({
    queryKey: ["admin", "jobs", status, offset],
    queryFn: () => adminListJobs({ status: status || undefined, limit: PAGE, offset }),
    refetchInterval: POLL_MS,
    placeholderData: keepPreviousData,
  });
  return (
    <section className="flex flex-col gap-4">
      <select
        aria-label="Filter by status"
        value={status}
        onChange={(e) => {
          const v = e.target.value;
          setStatus(JOB_STATUSES.find((s) => s === v) ?? "");
          setOffset(0);
        }}
        className={`${fieldClass} self-start`}
      >
        <option value="">All statuses</option>
        {JOB_STATUSES.map((s) => (
          <option key={s} value={s}>
            {JOB_LABELS[s]}
          </option>
        ))}
      </select>
      {jobs.isPending ? (
        <div className="h-32 animate-pulse rounded-md bg-neutral-900" aria-label="Loading jobs" />
      ) : jobs.isError ? (
        <LoadError error={jobs.error} />
      ) : (
        <>
          <div className="overflow-x-auto rounded-md border border-white/10">
            <table className="w-full text-left text-sm">
              <thead className="bg-white/5 text-xs text-neutral-400">
                <tr>
                  <th className="px-3 py-2 font-medium">Movie</th>
                  <th className="px-3 py-2 font-medium">Status</th>
                  <th className="px-3 py-2 font-medium">Purpose</th>
                  <th className="px-3 py-2 font-medium">Queued</th>
                  <th className="px-3 py-2 font-medium">Started</th>
                  <th className="px-3 py-2 font-medium">Finished</th>
                  <th className="px-3 py-2 font-medium">Runs as</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-white/10">
                {jobs.data.items.map((j) => (
                  <tr key={j.id} className="align-top">
                    <td className="px-3 py-2">
                      <span className="font-medium">{j.movie_title}</span>
                      {j.error_msg !== null && (
                        <details className="mt-1 text-xs text-red-300">
                          <summary className="cursor-pointer select-none">Error</summary>
                          <pre className="mt-1 max-w-xl whitespace-pre-wrap break-words font-mono">{j.error_msg}</pre>
                        </details>
                      )}
                    </td>
                    <td className="px-3 py-2">
                      <JobBadge status={j.status} />
                    </td>
                    <td className="px-3 py-2 text-xs">{PURPOSE_LABELS[j.purpose]}</td>
                    <td className="px-3 py-2 text-xs">{when(j.created_at)}</td>
                    <td className="px-3 py-2 text-xs">{when(j.started_at)}</td>
                    <td className="px-3 py-2 text-xs">{when(j.finished_at)}</td>
                    <td className="px-3 py-2 font-mono text-xs">{jobBackend(j)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {jobs.data.total === 0 && <p className="text-sm text-neutral-400">No jobs.</p>}
          <Pager offset={offset} total={jobs.data.total} onChange={setOffset} />
        </>
      )}
    </section>
  );
}

const JOB_STYLES: Record<JobStatus, string> = {
  queued: "bg-amber-500/15 text-amber-300",
  running: "bg-sky-500/15 text-sky-300",
  succeeded: "bg-emerald-500/15 text-emerald-300",
  failed: "bg-red-500/15 text-red-300",
};

function JobBadge({ status }: { status: JobStatus }) {
  return <span className={`inline-block rounded px-1.5 py-0.5 text-xs font-medium ${JOB_STYLES[status]}`}>{status}</span>;
}

function MoviesTab() {
  const queryClient = useQueryClient();
  const [status, setStatus] = useState<MovieStatus | "">("");
  const [offset, setOffset] = useState(0);
  const [deleting, setDeleting] = useState<AdminMovie | null>(null);
  const [retranscoding, setRetranscoding] = useState<AdminMovie | null>(null);
  const [error, setError] = useState<string | null>(null);
  const movies = useQuery({
    queryKey: ["admin", "movies", status, offset],
    queryFn: () => adminListMovies({ status: status || undefined, limit: PAGE, offset }),
    refetchInterval: POLL_MS,
    placeholderData: keepPreviousData,
  });
  const done = {
    onSuccess: () => {
      setError(null);
    },
    onError: (err: unknown) => {
      setError(describeAdminError(err));
    },
    onSettled: () => {
      setDeleting(null);
      setRetranscoding(null);
      void queryClient.invalidateQueries({ queryKey: ["admin"] });
      // The catalog must not keep showing a deleted movie, nor an old status.
      void queryClient.invalidateQueries({ queryKey: ["movies"] });
      void queryClient.invalidateQueries({ queryKey: ["movie"] });
      void queryClient.invalidateQueries({ queryKey: ["continue-watching"] });
    },
  };
  const retry = useMutation({ mutationFn: (m: AdminMovie) => adminRetryMovie(m.id), ...done });
  const remove = useMutation({ mutationFn: (m: AdminMovie) => adminDeleteMovie(m.id), ...done });
  const retranscode = useMutation({ mutationFn: (m: AdminMovie) => adminRetranscodeMovie(m.id), ...done });

  return (
    <section className="flex flex-col gap-4">
      <select
        aria-label="Filter by status"
        value={status}
        onChange={(e) => {
          setStatus(isMovieStatus(e.target.value) ? e.target.value : "");
          setOffset(0);
        }}
        className={`${fieldClass} self-start`}
      >
        <option value="">All statuses</option>
        {Object.entries(STATUS_LABELS).map(([value, label]) => (
          <option key={value} value={value}>
            {label}
          </option>
        ))}
      </select>
      <Alert message={error} />
      {movies.isPending ? (
        <div className="h-32 animate-pulse rounded-md bg-neutral-900" aria-label="Loading movies" />
      ) : movies.isError ? (
        <LoadError error={movies.error} />
      ) : (
        <>
          <div className="overflow-x-auto rounded-md border border-white/10">
            <table className="w-full text-left text-sm">
              <thead className="bg-white/5 text-xs text-neutral-400">
                <tr>
                  <th className="px-3 py-2 font-medium">Title</th>
                  <th className="px-3 py-2 font-medium">Status</th>
                  <th className="px-3 py-2 font-medium">Job</th>
                  <th className="px-3 py-2 font-medium">Added</th>
                  <th className="px-3 py-2 font-medium">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-white/10">
                {movies.data.items.map((m) => (
                  <tr key={m.id}>
                    <td className="px-3 py-2">
                      {m.status === "deleting" ? (
                        <span className="text-neutral-500">{m.title}</span>
                      ) : (
                        <Link to={`/movies/${m.id}`} className="font-medium hover:underline">
                          {m.title}
                        </Link>
                      )}
                      {m.year !== null && <span className="ml-1 text-neutral-500">({m.year})</span>}
                    </td>
                    <td className="px-3 py-2">
                      <StatusBadge status={m.status} />
                      {retranscodeNote(m) !== null && (
                        <span className="ml-2 text-xs text-sky-300">{retranscodeNote(m)}</span>
                      )}
                      {m.library_cleanup_after !== null && m.status === "ready" && (
                        <span className="mt-0.5 block text-xs text-neutral-500">
                          old version cleanup at {when(m.library_cleanup_after)}
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-2">{m.active_job === null ? "—" : <JobBadge status={m.active_job} />}</td>
                    <td className="px-3 py-2 text-xs">{when(m.created_at)}</td>
                    <td className="px-3 py-2">
                      <span className="flex justify-end gap-2">
                        {canRetry(m) && (
                          <button
                            type="button"
                            className={buttonClass}
                            disabled={retry.isPending}
                            onClick={() => {
                              retry.mutate(m);
                            }}
                          >
                            Retry
                          </button>
                        )}
                        {canRetranscode(m) && (
                          <button
                            type="button"
                            className={buttonClass}
                            onClick={() => {
                              setError(null);
                              setRetranscoding(m);
                            }}
                          >
                            Retranscode
                          </button>
                        )}
                        {canDelete(m) && (
                          <button
                            type="button"
                            className={`${buttonClass} text-red-300`}
                            onClick={() => {
                              setError(null);
                              setDeleting(m);
                            }}
                          >
                            Delete
                          </button>
                        )}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {movies.data.total === 0 && <p className="text-sm text-neutral-400">No movies.</p>}
          <Pager offset={offset} total={movies.data.total} onChange={setOffset} />
        </>
      )}
      {retranscoding && (
        <ConfirmDialog
          message={confirmRetranscode(retranscoding)}
          confirmLabel="Retranscode"
          pending={retranscode.isPending}
          onConfirm={() => {
            retranscode.mutate(retranscoding);
          }}
          onCancel={() => {
            setRetranscoding(null);
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          message={confirmDelete(deleting)}
          confirmLabel="Delete"
          danger
          pending={remove.isPending}
          onConfirm={() => {
            remove.mutate(deleting);
          }}
          onCancel={() => {
            setDeleting(null);
          }}
        />
      )}
    </section>
  );
}
