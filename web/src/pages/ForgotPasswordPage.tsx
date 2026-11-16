import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";

import { forgotPassword } from "../api/client";
import { useAuthConfig } from "../auth/useAuthConfig";
import { Turnstile } from "../components/Turnstile";
import { describeSignupError, plausibleEmail } from "../lib/registration";
import { AuthShell } from "../components/AuthShell";

const inputClass = "field h-10";

/** "Forgot password?": an address and a challenge; the api answers alike for every address. */
export function ForgotPasswordPage() {
  const config = useAuthConfig();
  const siteKey = config.data?.turnstile_site_key ?? null;
  const [email, setEmail] = useState("");
  const [token, setToken] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [error, setError] = useState<string | null>(null);

  const send = useMutation({
    mutationFn: () => forgotPassword(email.trim(), token ?? ""),
    onSettled: () => {
      // A token is good once: a new challenge for the next try.
      setToken(null);
      setAttempt((n) => n + 1);
    },
    onError: (err) => {
      setError(describeSignupError(err));
    },
  });

  return (
    <AuthShell>
      <h1 className="text-2xl font-bold">Forgot password</h1>
      {send.isSuccess ? (
        <>
          <p role="status" className="text-sm text-success">
            If the address has an account, a mail is on its way. Its link works once, for 30 minutes. The mail also
            names your username.
          </p>
          <Link to="/login" className="text-sm underline hover:text-fg">
            Back to log in
          </Link>
        </>
      ) : config.isPending ? (
        <div className="h-24 animate-pulse rounded-md bg-surface" aria-label="Loading" />
      ) : siteKey === null ? (
        <p className="text-sm text-fg-muted">Password reset is unavailable right now. Try again later.</p>
      ) : (
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            setError(null);
            if (!plausibleEmail(email)) setError("Enter the address of your account, like name@example.com.");
            else if (token === null) setError("Complete the check first.");
            else send.mutate();
          }}
        >
          <p className="text-sm text-fg-muted">Enter the email address of your account. We mail you a link to choose a new password.</p>
          <label className="flex flex-col gap-1 text-sm">
            Email address
            <input
              type="email"
              autoComplete="email"
              value={email}
              onChange={(e) => {
                setEmail(e.target.value);
              }}
              className={inputClass}
            />
          </label>
          <Turnstile key={attempt} siteKey={siteKey} onToken={setToken} />
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
          <button
            type="submit"
            disabled={send.isPending}
            className="inline-flex h-10 items-center justify-center rounded-lg bg-accent px-4 text-sm font-semibold text-on-accent transition-colors duration-150 hover:bg-accent-hover disabled:opacity-50"
          >
            {send.isPending ? "Sending…" : "Send reset link"}
          </button>
        </form>
      )}
    </AuthShell>
  );
}
