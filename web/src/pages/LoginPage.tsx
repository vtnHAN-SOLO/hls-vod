import { useState } from "react";
import { Link, Navigate, useLocation, useNavigate, useSearchParams } from "react-router";

import { ApiError } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { useAuthConfig } from "../auth/useAuthConfig";
import { GoogleButton } from "../components/GoogleButton";
import { ResendVerification } from "../components/ResendVerification";
import { Turnstile } from "../components/Turnstile";
import { describeError } from "../lib/errors";
import { describeGoogleError } from "../lib/google";
import { AuthShell } from "../components/AuthShell";

/** A sentence for a refused login. */
function describeLoginError(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "captcha_required":
        return err.message.startsWith("invalid username or password")
          ? "Wrong username or password. Complete the check below, then try again."
          : "Complete the check below, then log in.";
      case "captcha_unavailable":
        return "The check cannot be verified right now. Try again in a few minutes.";
      case "email_not_verified":
        return "Confirm your email address first: open the link we mailed you.";
      default:
        break;
    }
  }
  return describeError(err);
}

/** A message another page left for this one (the password reset), if any. */
function noticeOf(state: unknown): string | null {
  const notice = (state as { notice?: unknown } | null)?.notice;
  return typeof notice === "string" ? notice : null;
}

/** Where to go after login: the page that sent us here, never another origin. */
function returnTo(state: unknown): string {
  const from = (state as { from?: unknown } | null)?.from;
  return typeof from === "string" && from.startsWith("/") && !from.startsWith("//") && !from.startsWith("/\\") ? from : "/";
}

export function LoginPage() {
  const { state, login } = useAuth();
  const location = useLocation();
  const navigate = useNavigate();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const config = useAuthConfig();
  const siteKey = config.data?.turnstile_site_key ?? null;
  // After 3 failures the api wants a solved challenge with every attempt.
  const [captchaNeeded, setCaptchaNeeded] = useState(false);
  const [captcha, setCaptcha] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [unverified, setUnverified] = useState(false);
  // The api's Google callback sends failures back here as ?google_error=<code>.
  const [params] = useSearchParams();
  const googleError = describeGoogleError(params.get("google_error"));

  if (state.status === "authenticated") return <Navigate to={returnTo(location.state)} replace />;

  const submit = async (e: React.SyntheticEvent) => {
    e.preventDefault();
    if (captchaNeeded && captcha === null) {
      setError("Complete the check below, then log in.");
      return;
    }
    setBusy(true);
    setError(null);
    setUnverified(false);
    try {
      await login(username.trim(), password, captchaNeeded ? (captcha ?? undefined) : undefined);
      void navigate(returnTo(location.state), { replace: true });
    } catch (err) {
      setError(describeLoginError(err));
      if (err instanceof ApiError && err.code === "captcha_required") setCaptchaNeeded(true);
      if (err instanceof ApiError && err.code === "email_not_verified") setUnverified(true);
      else setPassword("");
    } finally {
      setBusy(false);
      if (captchaNeeded) {
        // A token is good once: a new challenge for the next try.
        setCaptcha(null);
        setAttempt((n) => n + 1);
      }
    }
  };

  return (
    <AuthShell>
      <h1 className="text-2xl font-bold">Log in</h1>
      {googleError !== null && error === null && (
        <p role="alert" className="text-sm text-danger">
          {googleError}
        </p>
      )}
      {noticeOf(location.state) !== null && error === null && (
        <p role="status" className="text-sm text-success">
          {noticeOf(location.state)}
        </p>
      )}
      <form className="flex flex-col gap-4" onSubmit={(e) => void submit(e)}>
        <label className="flex flex-col gap-1 text-sm">
          Username
          <input
            name="username"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            required
            value={username}
            onChange={(e) => {
              setUsername(e.target.value);
            }}
            className="field h-10"
          />
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Password
          <input
            name="password"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => {
              setPassword(e.target.value);
            }}
            className="field h-10"
          />
        </label>
        {captchaNeeded && siteKey !== null && <Turnstile key={attempt} siteKey={siteKey} onToken={setCaptcha} />}
        {error && (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        )}
        <Link to="/forgot-password" className="self-start text-sm text-fg-muted underline hover:text-fg">
          Forgot password?
        </Link>
        <button
          type="submit"
          disabled={busy || state.status === "unknown"}
          className="inline-flex h-10 items-center justify-center rounded-lg bg-accent px-4 text-sm font-semibold text-on-accent transition-colors duration-150 hover:bg-accent-hover disabled:opacity-50"
        >
          {busy ? "Logging in…" : "Log in"}
        </button>
      </form>
      {config.data?.google_enabled && (
        <>
          <p className="text-center text-xs text-fg-subtle">or</p>
          <GoogleButton />
        </>
      )}
      {unverified && (
        <section aria-label="Resend the confirmation mail" className="flex flex-col gap-3 border-t border-line pt-6">
          <p className="text-sm text-fg-muted">No mail, or the link expired? Send it again:</p>
          <ResendVerification siteKey={siteKey} />
        </section>
      )}
      {config.data?.registration_open && (
        <p className="text-sm text-fg-muted">
          No account?{" "}
          <Link to="/register" className="underline hover:text-fg">
            Register
          </Link>
        </p>
      )}
    </AuthShell>
  );
}
