import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useSearchParams } from "react-router";

import {
  ApiError,
  changePassword,
  getMe,
  linkGoogle,
  listSessions,
  type LoginSession,
  revokeOtherSessions,
  requestPasswordSetLink,
  revokeSession,
  unlinkGoogle,
} from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { useAuthConfig } from "../auth/useAuthConfig";
import { buttonClass } from "../components/ui/Button";
import { UserIcon } from "../components/ui/Icons";
import { Badge, Skeleton } from "../components/ui/Primitives";
import { describeError } from "../lib/errors";
import { describeGoogleError } from "../lib/google";
import { checkPasswordChange, describePasswordChangeError } from "../lib/password";

const SESSIONS = ["sessions"] as const;

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });
const date = new Intl.DateTimeFormat(undefined, { dateStyle: "long" });

export function AccountPage() {
  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-6 px-4 py-8 sm:px-6">
      <h1 className="flex items-center gap-3 text-3xl font-bold">
        <span className="text-accent-soft">
          <UserIcon size={28} />
        </span>
        Account
      </h1>
      <Profile />
      <EmailSection />
      <GoogleSection />
      <PasswordSection />
      <Sessions />
    </main>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  const id = `account-${title.toLowerCase().replaceAll(" ", "-")}`;
  return (
    <section aria-labelledby={id} className="flex flex-col gap-4 rounded-2xl border border-line bg-surface p-5 sm:p-6">
      <h2 id={id} className="text-lg font-semibold">
        {title}
      </h2>
      {children}
    </section>
  );
}

function Profile() {
  const me = useQuery({ queryKey: ["me"], queryFn: getMe });
  return (
    <Section title="Profile">
      {me.isPending ? (
        <div aria-label="Loading your account"><Skeleton className="h-16" /></div>
      ) : me.isError ? (
        <p className="text-sm text-danger">{describeError(me.error)}</p>
      ) : (
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
          <dt className="text-fg-muted">Username</dt>
          <dd>{me.data.username}</dd>
          <dt className="text-fg-muted">Role</dt>
          <dd>{me.data.role === "admin" ? <Badge tone="accent">Admin</Badge> : "User"}</dd>
          <dt className="text-fg-muted">Member since</dt>
          <dd>
            <time dateTime={me.data.created_at}>{date.format(new Date(me.data.created_at))}</time>
          </dd>
        </dl>
      )}
    </Section>
  );
}

const inputClass = "field h-10";

/** The address and whether it is confirmed, read-only: an account's email never changes. */
function EmailSection() {
  const me = useQuery({ queryKey: ["me"], queryFn: getMe });
  return (
    <Section title="Email">
      {me.isPending ? (
        <div aria-label="Loading your email"><Skeleton className="h-16" /></div>
      ) : me.isError ? (
        <p className="text-sm text-danger">{describeError(me.error)}</p>
      ) : (
        <>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-fg-muted">Address</dt>
            <dd className="break-all">{me.data.email ?? "None"}</dd>
            {me.data.email !== null && (
              <>
                <dt className="text-fg-muted">Status</dt>
                <dd>
                  {me.data.email_verified_at === null ? (
                    "Not confirmed"
                  ) : (
                    <>
                      Confirmed{" "}
                      <time dateTime={me.data.email_verified_at}>{date.format(new Date(me.data.email_verified_at))}</time>
                    </>
                  )}
                </dd>
              </>
            )}
          </dl>
          <p className="text-sm text-fg-muted">
            {me.data.email === null
              ? "This account was made by an admin and has no email, so a forgotten password cannot be reset by mail: ask the admin."
              : "Your email is fixed when you register: it is where password-reset links go."}
          </p>
        </>
      )}
    </Section>
  );
}

/** The password: one button, collapsed until used. */
function PasswordSection() {
  const me = useQuery({ queryKey: ["me"], queryFn: getMe });
  const [open, setOpen] = useState(false);
  const setLink = useMutation({ mutationFn: requestPasswordSetLink });
  if (me.isPending) return null;
  if (me.isError) return null; // the Profile section shows the error
  const hasPassword = me.data.has_password;
  return (
    <Section title="Password">
      {hasPassword && open ? (
        <PasswordForm />
      ) : setLink.isSuccess ? (
        <p role="status" className="text-sm text-success">
          Check your inbox: we mailed a link to set your password (it works for 30 minutes). Setting the password signs
          you out everywhere; then sign in again.
        </p>
      ) : (
        <>
          {!hasPassword && (
            <p className="text-sm text-fg-muted">
              You sign in with Google. Set a password to also sign in with your username.
            </p>
          )}
          <button
            type="button"
            disabled={setLink.isPending}
            onClick={() => {
              if (hasPassword) setOpen(true);
              else setLink.mutate();
            }}
            className={buttonClass("secondary", "sm", "self-start")}
          >
            {hasPassword ? "Change password" : setLink.isPending ? "Sending…" : "Set password"}
          </button>
          {setLink.isError && (
            <p role="alert" className="text-sm text-danger">
              {describeError(setLink.error)}
            </p>
          )}
        </>
      )}
    </Section>
  );
}

/**
 * Google: connected (with Remove), or Connect, which asks for the current
 * password and then goes to Google. The api's callback comes back here with
 * ?google=linked or ?google_error=<code>.
 */
function GoogleSection() {
  const queryClient = useQueryClient();
  const me = useQuery({ queryKey: ["me"], queryFn: getMe });
  const config = useAuthConfig();
  const [params] = useSearchParams();
  const [connecting, setConnecting] = useState(false);
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);

  const connect = useMutation({
    mutationFn: () => linkGoogle(password),
    onSuccess: (url) => {
      window.location.assign(url);
    },
    onError: (err) => {
      setPassword("");
      setError(describePasswordChangeError(err));
    },
  });
  const remove = useMutation({
    mutationFn: unlinkGoogle,
    onSuccess: () => {
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["me"] });
    },
    onError: (err) => {
      setError(
        err instanceof ApiError && err.code === "last_login_method"
          ? "Set a password first: Google is your only way to sign in."
          : describeError(err),
      );
    },
  });

  if (me.isPending || me.isError) return null;
  const google = me.data.google;
  if (google === null && !config.data?.google_enabled) return null;
  const googleError = describeGoogleError(params.get("google_error"));
  return (
    <Section title="Google">
      {params.get("google") === "linked" && google !== null && (
        <p role="status" className="text-sm text-success">
          Google is connected: you can now sign in with it.
        </p>
      )}
      {googleError !== null && (
        <p role="alert" className="text-sm text-danger">
          {googleError}
        </p>
      )}
      {google !== null ? (
        <>
          <p className="text-sm">
            Google: connected{google.email_at_link !== null && <span className="text-fg-muted"> ({google.email_at_link})</span>}
          </p>
          {me.data.has_password ? (
            <button
              type="button"
              disabled={remove.isPending}
              onClick={() => {
                remove.mutate();
              }}
              className={buttonClass("secondary", "sm", "self-start")}
            >
              Remove Google
            </button>
          ) : (
            <p className="text-sm text-fg-muted">Google is your only way to sign in: set a password before removing it.</p>
          )}
        </>
      ) : !me.data.has_password ? null : connecting ? (
        <form
          className="flex max-w-sm flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            setError(null);
            if (password === "") setError("Enter your current password.");
            else connect.mutate();
          }}
        >
          <label className="flex flex-col gap-1 text-sm">
            Current password
            <input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => {
                setPassword(e.target.value);
                setError(null);
              }}
              className={inputClass}
            />
          </label>
          <button
            type="submit"
            disabled={connect.isPending}
            className={buttonClass("primary", "md", "self-start")}
          >
            {connect.isPending ? "Going to Google…" : "Continue to Google"}
          </button>
        </form>
      ) : (
        <button
          type="button"
          onClick={() => {
            setConnecting(true);
          }}
          className={buttonClass("secondary", "sm", "self-start")}
        >
          Connect Google
        </button>
      )}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
    </Section>
  );
}

function PasswordForm() {
  const queryClient = useQueryClient();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  const change = useMutation({
    mutationFn: () => changePassword(current, next),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      setConfirm("");
      setDone(true);
      void queryClient.invalidateQueries({ queryKey: SESSIONS });
    },
    onError: (err) => {
      setError(describePasswordChangeError(err));
      setCurrent("");
    },
  });

  const field = (label: string, value: string, set: (v: string) => void, autoComplete: string) => (
    <label className="flex flex-col gap-1 text-sm">
      {label}
      <input
        type="password"
        autoComplete={autoComplete}
        value={value}
        onChange={(e) => {
          set(e.target.value);
          setError(null);
          setDone(false);
        }}
        className={inputClass}
      />
    </label>
  );

  return (
    <form
        noValidate // checkPasswordChange applies the api's rules, with its messages
        className="flex max-w-sm flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          const problem = checkPasswordChange({ current, next, confirm });
          if (problem) setError(problem);
          else change.mutate();
        }}
      >
        {field("Current password", current, setCurrent, "current-password")}
        {field("New password", next, setNext, "new-password")}
        {field("Confirm new password", confirm, setConfirm, "new-password")}
        {error && (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        )}
        {done && (
          <p role="status" className="text-sm text-success">
            Password changed. Other sessions were signed out.
          </p>
        )}
        <button
          type="submit"
          disabled={change.isPending}
          className={buttonClass("primary", "md", "self-start")}
        >
          {change.isPending ? "Changing…" : "Change password"}
        </button>
    </form>
  );
}

function Sessions() {
  const queryClient = useQueryClient();
  const { logout } = useAuth();
  const sessions = useQuery({ queryKey: SESSIONS, queryFn: listSessions });
  const [error, setError] = useState<string | null>(null);
  const refetch = () => queryClient.invalidateQueries({ queryKey: SESSIONS });

  const revoke = useMutation({
    mutationFn: async (s: LoginSession) => {
      await revokeSession(s.id);
      // This device's own session: the refresh cookie is dead, so log out
      // here as well (clears the cookie, then the login page).
      if (s.current) await logout();
    },
    onSuccess: () => {
      setError(null);
      void refetch();
    },
    onError: (err) => {
      // Already gone (revoked elsewhere, or expired): the list was stale.
      setError(err instanceof ApiError && err.code === "not_found" ? "That session had already ended." : describeError(err));
      void refetch();
    },
  });
  const revokeOthers = useMutation({
    mutationFn: revokeOtherSessions,
    onSuccess: () => {
      setError(null);
      void refetch();
    },
    onError: (err) => {
      setError(describeError(err));
    },
  });

  const others = sessions.data?.items.filter((s) => !s.current).length ?? 0;
  return (
    <Section title="Sessions">
      <p className="text-sm text-fg-muted">
        Every browser logged in to your account. A signed-out device loses access at its next refresh, within 15
        minutes.
      </p>
      {sessions.isPending ? (
        <div aria-label="Loading sessions"><Skeleton className="h-24" /></div>
      ) : sessions.isError ? (
        <p className="text-sm text-danger">{describeError(sessions.error)}</p>
      ) : (
        <ul className="flex flex-col divide-y divide-line rounded-xl border border-line bg-raised">
          {sessions.data.items.map((s) => (
            <li key={s.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
              <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="flex items-center gap-2 font-medium">
                  {s.user_agent}
                  {s.current && (
                    <Badge tone="success">This device</Badge>
                  )}
                </span>
                <span className="text-xs text-fg-muted">
                  Signed in <time dateTime={s.created_at}>{dateTime.format(new Date(s.created_at))}</time> · last
                  active <time dateTime={s.last_used_at}>{dateTime.format(new Date(s.last_used_at))}</time>
                </span>
              </div>
              <button
                type="button"
                disabled={revoke.isPending}
                onClick={() => {
                  revoke.mutate(s);
                }}
                className={buttonClass("secondary", "sm")}
              >
                {s.current ? "Sign out" : "Revoke"}
              </button>
            </li>
          ))}
        </ul>
      )}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      <button
        type="button"
        disabled={others === 0 || revokeOthers.isPending}
        onClick={() => {
          revokeOthers.mutate();
        }}
        className={buttonClass("ghost", "sm", "self-start")}
      >
        Sign out everywhere else
      </button>
    </Section>
  );
}
