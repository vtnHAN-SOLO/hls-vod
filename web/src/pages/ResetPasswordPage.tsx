import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate } from "react-router";

import { ApiError, resetPassword } from "../api/client";
import { useLinkToken } from "../auth/useLinkToken";
import { checkNewPassword } from "../lib/recovery";
import { describeSignupError } from "../lib/registration";
import { AuthShell } from "../components/AuthShell";

const inputClass = "field h-10";

/** Opened from the mail: /reset-password#token=…. A new password, then log in with it. */
export function ResetPasswordPage() {
  const token = useLinkToken();
  const navigate = useNavigate();
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [deadLink, setDeadLink] = useState(token === null);

  const reset = useMutation({
    mutationFn: () => resetPassword(token ?? "", password),
    onSuccess: () => {
      void navigate("/login", { replace: true, state: { notice: "Password changed. Log in with your new password." } });
    },
    onError: (err) => {
      if (err instanceof ApiError && err.code === "invalid_token") setDeadLink(true);
      else setError(describeSignupError(err));
    },
  });

  const field = (label: string, value: string, set: (v: string) => void) => (
    <label className="flex flex-col gap-1 text-sm">
      {label}
      <input
        type="password"
        autoComplete="new-password"
        value={value}
        onChange={(e) => {
          set(e.target.value);
          setError(null);
        }}
        className={inputClass}
      />
    </label>
  );

  return (
    <AuthShell>
      <h1 className="text-2xl font-bold">Choose a new password</h1>
      {deadLink ? (
        <>
          <p role="alert" className="text-sm text-danger">
            This link is invalid, expired or already used. Links work once, for 30 minutes, and only the newest one
            works.
          </p>
          <Link to="/forgot-password" className="text-sm underline hover:text-fg">
            Ask for a new link
          </Link>
        </>
      ) : (
        <form
          noValidate // checkNewPassword applies the api's rules, with its messages
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            const problem = checkNewPassword(password, confirm);
            if (problem) setError(problem);
            else reset.mutate();
          }}
        >
          <p className="text-sm text-fg-muted">This logs out every device signed in to your account.</p>
          {field("New password", password, setPassword)}
          {field("Confirm new password", confirm, setConfirm)}
          {error && (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          )}
          <button
            type="submit"
            disabled={reset.isPending}
            className="inline-flex h-10 items-center justify-center rounded-lg bg-accent px-4 text-sm font-semibold text-on-accent transition-colors duration-150 hover:bg-accent-hover disabled:opacity-50"
          >
            {reset.isPending ? "Saving…" : "Set new password"}
          </button>
        </form>
      )}
    </AuthShell>
  );
}
