import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";

import {
  type AdminMovie,
  type AdminUser,
  adminDeleteMovie,
  adminEditMovie,
  adminListJobs,
  adminListMovies,
  adminListUsers,
  adminRetranscodeMovie,
  adminRetryMovie,
  adminSetDisabled,
  adminSetFeatured,
  adminSetPublished,
  adminSetRole,
  ApiError,
  type JobPurpose,
  type JobStatus,
  type MovieStatus,
} from "../api/client";
import { useUser } from "../auth/AuthProvider";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { PosterImage } from "../components/Poster";
import { PosterUpload } from "../components/PosterUpload";
import { StatusBadge } from "../components/StatusBadge";
import { Button, ButtonLink } from "../components/ui/Button";
import { Dialog } from "../components/ui/Dialog";
import {
  EyeIcon,
  EyeOffIcon,
  PencilIcon,
  RefreshIcon,
  SearchIcon,
  ShieldIcon,
  SparkIcon,
  TrashIcon,
} from "../components/ui/Icons";
import { Badge, EmptyState, Notice, Skeleton, type Tone } from "../components/ui/Primitives";
import {
  ADMIN_TABS,
  type AdminTab,
  actionLabel,
  canDelete,
  canFeature,
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
import { checkEdit, draftOf, MAX_OVERVIEW, MAX_TITLE, type MovieEditDraft } from "../lib/movieEdit";
import { POSTER_SIZES, publishBlockers, publishHint } from "../lib/poster";

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
const fieldClass = "field h-10";

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
    <main className="mx-auto flex max-w-[1600px] flex-col gap-6 px-4 py-8 sm:px-6">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <h1 className="flex items-center gap-3 text-3xl font-bold">
          <span className="text-accent-soft">
            <ShieldIcon size={28} />
          </span>
          Admin
        </h1>
      </div>
      <nav aria-label="Admin sections" className="flex w-fit gap-1 rounded-xl border border-line bg-surface p-1">
        {ADMIN_TABS.map((t) => (
          <button
            key={t}
            type="button"
            aria-current={t === tab ? "page" : undefined}
            onClick={() => {
              setParams({ tab: t });
            }}
            className={`rounded-lg px-4 py-2 text-sm font-medium transition-colors duration-150 ${
              t === tab ? "bg-overlay-hover text-fg" : "text-fg-muted hover:text-fg"
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
    <main className="mx-auto max-w-3xl px-4 py-24 sm:px-6">
      <h1 className="sr-only">403: admins only</h1>
      <EmptyState
        icon={<ShieldIcon size={28} />}
        title="403: admins only"
        action={<ButtonLink to="/browse">Browse movies</ButtonLink>}
      >
        This page is for admins.
      </EmptyState>
    </main>
  );
}

/** A failed list: a demoted admin's 403 says so plainly. */
function LoadError({ error }: { error: unknown }) {
  if (error instanceof ApiError && error.code === "forbidden") {
    return <Notice tone="danger">You are no longer an admin. Log out and in again to refresh your role.</Notice>;
  }
  return <Notice tone="danger">{describeAdminError(error)}</Notice>;
}

function Alert({ message }: { message: string | null }) {
  if (message === null) return null;
  return <Notice tone="danger">{message}</Notice>;
}

function Pager({ offset, total, onChange }: { offset: number; total: number; onChange: (o: number) => void }) {
  if (total <= PAGE) return null;
  return (
    <div className="flex items-center gap-3 text-sm text-fg-muted">
      <Button size="sm" disabled={offset === 0} onClick={() => { onChange(Math.max(0, offset - PAGE)); }}>
        Previous
      </Button>
      <span>
        {offset + 1}–{Math.min(offset + PAGE, total)} of {total}
      </span>
      <Button size="sm" disabled={offset + PAGE >= total} onClick={() => { onChange(offset + PAGE); }}>
        Next
      </Button>
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
        <div className="relative w-full sm:w-72">
        <SearchIcon size={16} className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-subtle" />
        <input
          type="search"
          aria-label="Search users"
          placeholder="Username or email"
          maxLength={100}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
          }}
          className={`${fieldClass} w-full pl-9`}
        />
        </div>
        <Button type="submit" variant="primary">
          Search
        </Button>
      </form>
      <Alert message={error} />
      {users.isPending ? (
        <div aria-label="Loading users"><Skeleton className="h-40 rounded-xl" /></div>
      ) : users.isError ? (
        <LoadError error={users.error} />
      ) : (
        <>
          <div className="overflow-x-auto rounded-xl border border-line bg-surface">
            <table className="w-full text-left text-sm">
              <thead className="bg-raised text-xs tracking-wide text-fg-subtle uppercase">
                <tr>
                  <th className="px-4 py-3 font-medium">User</th>
                  <th className="px-4 py-3 font-medium">Email</th>
                  <th className="px-4 py-3 font-medium">Role</th>
                  <th className="px-4 py-3 font-medium">Sign-in</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-4 py-3 font-medium">Created</th>
                  <th className="px-4 py-3 font-medium">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {users.data.items.map((u) => (
                  <tr key={u.id} className={u.disabled_at === null ? "" : "text-fg-subtle"}>
                    <td className="px-4 py-3 font-medium">{u.username}</td>
                    <td className="px-4 py-3">
                      <span className="break-all">{u.email ?? "—"}</span>
                      {u.email !== null && (
                        <span className="ml-1 text-xs text-fg-subtle">
                          {u.email_verified_at === null ? "(unverified)" : "(verified)"}
                        </span>
                      )}
                    </td>
                    <td className="px-4 py-3">{u.role === "admin" ? <Badge tone="accent">Admin</Badge> : "User"}</td>
                    <td className="px-4 py-3 text-xs">
                      {[u.has_password && "Password", u.google_linked && "Google"].filter(Boolean).join(" · ") || "—"}
                    </td>
                    <td className="px-4 py-3 text-xs">
                      {u.disabled_at === null ? <Badge tone="success">Active</Badge> : <>Disabled {when(u.disabled_at)}</>}
                    </td>
                    <td className="px-4 py-3 text-xs">{when(u.created_at)}</td>
                    <td className="px-4 py-3">
                      <span className="flex justify-end gap-2">
                        {userActions(u).map((a) => (
                          <Button
                            key={a.kind}
                            size="sm"
                            variant={a.kind === "disable" ? "ghost" : "secondary"}
                            className={a.kind === "disable" ? "text-danger" : ""}
                            onClick={() => {
                              setError(null);
                              setPending({ action: a, user: u });
                            }}
                          >
                            {actionLabel(a)}
                          </Button>
                        ))}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {users.data.total === 0 && <EmptyState title="No user matches" className="py-8" />}
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
        <div aria-label="Loading jobs"><Skeleton className="h-40 rounded-xl" /></div>
      ) : jobs.isError ? (
        <LoadError error={jobs.error} />
      ) : (
        <>
          <div className="overflow-x-auto rounded-xl border border-line bg-surface">
            <table className="w-full text-left text-sm">
              <thead className="bg-raised text-xs tracking-wide text-fg-subtle uppercase">
                <tr>
                  <th className="px-4 py-3 font-medium">Movie</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-4 py-3 font-medium">Purpose</th>
                  <th className="px-4 py-3 font-medium">Queued</th>
                  <th className="px-4 py-3 font-medium">Started</th>
                  <th className="px-4 py-3 font-medium">Finished</th>
                  <th className="px-4 py-3 font-medium">Runs as</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {jobs.data.items.map((j) => (
                  <tr key={j.id} className="align-top">
                    <td className="px-4 py-3">
                      <span className="font-medium">{j.movie_title}</span>
                      {j.error_msg !== null && (
                        <details className="mt-1 text-xs text-danger">
                          <summary className="cursor-pointer select-none">Error</summary>
                          <pre className="mt-1 max-w-xl whitespace-pre-wrap break-words font-mono">{j.error_msg}</pre>
                        </details>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      <JobBadge status={j.status} />
                    </td>
                    <td className="px-4 py-3 text-xs">{PURPOSE_LABELS[j.purpose]}</td>
                    <td className="px-4 py-3 text-xs">{when(j.created_at)}</td>
                    <td className="px-4 py-3 text-xs">{when(j.started_at)}</td>
                    <td className="px-4 py-3 text-xs">{when(j.finished_at)}</td>
                    <td className="px-4 py-3 font-mono text-xs">{jobBackend(j)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {jobs.data.total === 0 && <EmptyState title="No jobs" className="py-8" />}
          <Pager offset={offset} total={jobs.data.total} onChange={setOffset} />
        </>
      )}
    </section>
  );
}

const JOB_TONES: Record<JobStatus, Tone> = {
  queued: "warning",
  running: "accent",
  succeeded: "success",
  failed: "danger",
};

function JobBadge({ status }: { status: JobStatus }) {
  return <Badge tone={JOB_TONES[status]}>{JOB_LABELS[status]}</Badge>;
}

function MoviesTab() {
  const queryClient = useQueryClient();
  const [status, setStatus] = useState<MovieStatus | "">("");
  const [offset, setOffset] = useState(0);
  const [deleting, setDeleting] = useState<AdminMovie | null>(null);
  const [retranscoding, setRetranscoding] = useState<AdminMovie | null>(null);
  const [editing, setEditing] = useState<AdminMovie | null>(null);
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
      void queryClient.invalidateQueries({ queryKey: ["featured"] });
      void queryClient.invalidateQueries({ queryKey: ["popular"] });
    },
  };
  const retry = useMutation({ mutationFn: (m: AdminMovie) => adminRetryMovie(m.id), ...done });
  const remove = useMutation({ mutationFn: (m: AdminMovie) => adminDeleteMovie(m.id), ...done });
  const retranscode = useMutation({ mutationFn: (m: AdminMovie) => adminRetranscodeMovie(m.id), ...done });
  const feature = useMutation({
    mutationFn: ({ m, on }: { m: AdminMovie; on: boolean }) => adminSetFeatured(m.id, on),
    ...done,
  });
  const publish = useMutation({
    mutationFn: ({ m, on }: { m: AdminMovie; on: boolean }) => adminSetPublished(m.id, on),
    ...done,
  });

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
        <div aria-label="Loading movies"><Skeleton className="h-40 rounded-xl" /></div>
      ) : movies.isError ? (
        <LoadError error={movies.error} />
      ) : (
        <>
          <div className="overflow-x-auto rounded-xl border border-line bg-surface">
            <table className="w-full text-left text-sm">
              <thead className="bg-raised text-xs tracking-wide text-fg-subtle uppercase">
                <tr>
                  <th className="px-4 py-3 font-medium">Title</th>
                  <th className="px-4 py-3 font-medium">Viewers see it</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-4 py-3 font-medium">Job</th>
                  <th className="hidden px-4 py-3 font-medium md:table-cell">Added</th>
                  <th className="px-4 py-3 font-medium">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {movies.data.items.map((m) => (
                  <tr key={m.id}>
                    <td className="px-4 py-3">
                      <span className="flex items-center gap-3">
                      <span className="w-8 shrink-0 overflow-hidden rounded ring-1 ring-line">
                        <PosterImage key={m.poster?.hash} poster={m.poster} title={m.title} sizes={POSTER_SIZES.ranked} />
                      </span>
                      <span>
                      {m.status === "deleting" ? (
                        <span className="text-fg-subtle">{m.title}</span>
                      ) : (
                        <Link to={`/movies/${m.id}`} className="font-medium hover:underline">
                          {m.title}
                        </Link>
                      )}
                      {m.year !== null && <span className="ml-1 text-fg-subtle">({m.year})</span>}
                      {m.featured_at !== null && (
                        <Badge tone="accent" className="ml-2">
                          <SparkIcon size={12} />
                          Featured
                        </Badge>
                      )}
                      </span>
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      <PublishBadges movie={m} />
                    </td>
                    <td className="px-4 py-3">
                      <StatusBadge status={m.status} />
                      {retranscodeNote(m) !== null && (
                        <span className="ml-2 text-xs text-accent-soft">{retranscodeNote(m)}</span>
                      )}
                      {m.library_cleanup_after !== null && m.status === "ready" && (
                        <span className="mt-0.5 block text-xs text-fg-subtle">
                          old version cleanup at {when(m.library_cleanup_after)}
                        </span>
                      )}
                    </td>
                    <td className="px-4 py-3">{m.active_job === null ? "—" : <JobBadge status={m.active_job} />}</td>
                    <td className="hidden px-4 py-3 text-xs md:table-cell">{when(m.created_at)}</td>
                    <td className="px-4 py-3">
                      <span className="flex justify-end gap-2">
                        {m.status !== "deleting" && (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => {
                              setError(null);
                              setEditing(m);
                            }}
                          >
                            <PencilIcon size={14} />
                            Edit
                          </Button>
                        )}
                        {m.status !== "deleting" && (
                          <PublishButton
                            movie={m}
                            pending={publish.isPending}
                            onClick={(on) => {
                              publish.mutate({ m, on });
                            }}
                          />
                        )}
                        {(canFeature(m) || m.featured_at !== null) && (
                          <Button
                            size="sm"
                            variant={m.featured_at === null ? "secondary" : "ghost"}
                            aria-pressed={m.featured_at !== null}
                            disabled={feature.isPending}
                            onClick={() => {
                              feature.mutate({ m, on: m.featured_at === null });
                            }}
                          >
                            <SparkIcon size={14} />
                            {m.featured_at === null ? "Feature" : "Unfeature"}
                          </Button>
                        )}
                        {canRetry(m) && (
                          <Button
                            size="sm"
                            disabled={retry.isPending}
                            onClick={() => {
                              retry.mutate(m);
                            }}
                          >
                            <RefreshIcon size={14} />
                            Retry
                          </Button>
                        )}
                        {canRetranscode(m) && (
                          <Button
                            size="sm"
                            onClick={() => {
                              setError(null);
                              setRetranscoding(m);
                            }}
                          >
                            <RefreshIcon size={14} />
                            Retranscode
                          </Button>
                        )}
                        {canDelete(m) && (
                          <Button
                            size="sm"
                            variant="ghost"
                            className="text-danger"
                            onClick={() => {
                              setError(null);
                              setDeleting(m);
                            }}
                          >
                            <TrashIcon size={14} />
                            Delete
                          </Button>
                        )}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {movies.data.total === 0 && <EmptyState title="No movies" className="py-8" />}
          <Pager offset={offset} total={movies.data.total} onChange={setOffset} />
        </>
      )}
      {retranscoding && (
        <ConfirmDialog
          title="Retranscode this movie?"
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
      {editing && (
        <EditMovieDialog
          movie={editing}
          onClose={() => {
            setEditing(null);
          }}
          onSaved={() => {
            setEditing(null);
            done.onSettled();
          }}
          onPosterChanged={() => {
            void queryClient.invalidateQueries({ queryKey: ["admin", "movies"] });
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete this movie?"
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

/**
 * Whether viewers see the movie, and what it lacks to be published: the
 * reasons are shown, not only implied by a disabled button.
 */
function PublishBadges({ movie }: { movie: AdminMovie }) {
  if (movie.status === "deleting") return <span className="text-fg-subtle">—</span>;
  const blockers = publishBlockers(movie);
  return (
    <span className="flex flex-wrap gap-1.5">
      {movie.published_at !== null ? (
        <Badge tone="success">Published</Badge>
      ) : (
        <Badge tone="neutral">Unpublished</Badge>
      )}
      {blockers.includes("missing_poster") && <Badge tone="warning">Needs poster</Badge>}
      {blockers.includes("missing_overview") && <Badge tone="warning">Needs description</Badge>}
    </span>
  );
}

/** Publish (disabled, with the reason, until the movie has what it needs) or Unpublish. */
function PublishButton({
  movie,
  pending,
  onClick,
}: {
  movie: AdminMovie;
  pending: boolean;
  onClick: (publish: boolean) => void;
}) {
  const hintId = `publish-hint-${movie.id}`;
  if (movie.published_at !== null) {
    return (
      <Button
        size="sm"
        variant="ghost"
        disabled={pending}
        onClick={() => {
          onClick(false);
        }}
      >
        <EyeOffIcon size={14} />
        Unpublish
      </Button>
    );
  }
  const hint = publishHint(movie);
  return (
    <span className="flex flex-col items-end">
      <Button
        size="sm"
        variant="secondary"
        disabled={pending || hint !== null}
        aria-describedby={hint ? hintId : undefined}
        title={hint ?? undefined}
        onClick={() => {
          onClick(true);
        }}
      >
        <EyeIcon size={14} />
        Publish
      </Button>
      {hint && (
        <span id={hintId} className="sr-only">
          {hint}
        </span>
      )}
    </span>
  );
}

/**
 * Title, year, description and poster of one movie; only changed fields
 * are sent. A poster uploads as soon as it is chosen; "Save and publish"
 * saves the text, then publishes, when the movie has what it needs.
 */
function EditMovieDialog({
  movie,
  onClose,
  onSaved,
  onPosterChanged,
}: {
  movie: AdminMovie;
  onClose: () => void;
  onSaved: () => void;
  onPosterChanged: () => void;
}) {
  const [current, setCurrent] = useState(movie);
  const [draft, setDraft] = useState<MovieEditDraft>(() => draftOf(movie));
  const [problem, setProblem] = useState<{ field: keyof MovieEditDraft | null; error: string } | null>(null);
  const save = useMutation({
    mutationFn: async ({ body, andPublish }: { body: Parameters<typeof adminEditMovie>[1] | null; andPublish: boolean }) => {
      if (body !== null) await adminEditMovie(current.id, body);
      if (andPublish) await adminSetPublished(current.id, true);
    },
    onSuccess: onSaved,
    onError: (err) => {
      setProblem({ field: null, error: describeAdminError(err) });
    },
  });
  const edit = (next: Partial<MovieEditDraft>) => {
    setDraft((d) => ({ ...d, ...next }));
    setProblem(null);
  };
  const errorFor = (f: keyof MovieEditDraft) => (problem?.field === f ? problem.error : null);
  const published = current.published_at !== null;
  // What publishing would lack once this draft is saved.
  const lacking = publishHint({ ...current, overview: draft.overview });
  const overviewLength = Array.from(draft.overview.trim()).length;

  const submit = (andPublish: boolean) => {
    const r = checkEdit(draft, current);
    if (!r.ok) {
      setProblem({ field: r.field, error: r.error });
      return;
    }
    if (published && draft.overview.trim() === "") {
      setProblem({ field: "overview", error: "A published movie needs a description: unpublish it first." });
      return;
    }
    if (r.body === null && !andPublish) {
      onClose();
      return;
    }
    save.mutate({ body: r.body, andPublish });
  };

  return (
    <Dialog title="Edit movie" onClose={onClose}>
      <form
        noValidate
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          submit(false);
        }}
      >
        <PosterUpload
          movie={current}
          onChange={(m) => {
            setCurrent(m);
            onPosterChanged();
          }}
        />
        <EditField label="Title" error={errorFor("title")}>
          <input
            value={draft.title}
            maxLength={MAX_TITLE}
            required
            onChange={(e) => {
              edit({ title: e.target.value });
            }}
            className={`${fieldClass} w-full`}
          />
        </EditField>
        <EditField label="Year" hint="Empty for unknown" error={errorFor("year")}>
          <input
            value={draft.year}
            inputMode="numeric"
            maxLength={4}
            onChange={(e) => {
              edit({ year: e.target.value });
            }}
            className={`${fieldClass} w-28`}
          />
        </EditField>
        <EditField
          label="Description"
          hint={`${String(overviewLength)} / ${String(MAX_OVERVIEW)}`}
          hintTone={overviewLength > MAX_OVERVIEW ? "danger" : undefined}
          error={errorFor("overview")}
        >
          <textarea
            value={draft.overview}
            rows={9}
            placeholder="What viewers read on the detail page. Needed to publish."
            onChange={(e) => {
              edit({ overview: e.target.value });
            }}
            className="field min-h-48 w-full resize-y leading-relaxed"
          />
        </EditField>
        {problem?.field === null && <Notice tone="danger">{problem.error}</Notice>}
        <p className="text-xs text-fg-muted">
          {published ? (
            <>Published: viewers see it.</>
          ) : lacking ? (
            <>
              Not published. <span className="text-warning">{lacking}</span>
            </>
          ) : (
            <>Not published yet: it has everything it needs.</>
          )}
        </p>
        <div className="mt-1 flex flex-wrap justify-end gap-3">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant={published ? "primary" : "secondary"} disabled={save.isPending}>
            {save.isPending ? "Saving…" : "Save"}
          </Button>
          {!published && (
            <Button
              variant="primary"
              disabled={save.isPending || lacking !== null}
              title={lacking ?? undefined}
              onClick={() => {
                submit(true);
              }}
            >
              <EyeIcon size={16} />
              Save and publish
            </Button>
          )}
        </div>
      </form>
    </Dialog>
  );
}

function EditField({
  label,
  hint,
  hintTone,
  error,
  children,
}: {
  label: string;
  hint?: string;
  hintTone?: "danger";
  error: string | null;
  children: React.ReactNode;
}) {
  return (
    <label className="flex flex-col gap-1.5 text-sm">
      <span className="flex items-baseline justify-between gap-3">
        <span className="font-medium text-fg">{label}</span>
        {hint && <span className={`text-xs tabular-nums ${hintTone === "danger" ? "text-danger" : "text-fg-subtle"}`}>{hint}</span>}
      </span>
      {children}
      {error && <span role="alert" className="text-xs text-danger">{error}</span>}
    </label>
  );
}
