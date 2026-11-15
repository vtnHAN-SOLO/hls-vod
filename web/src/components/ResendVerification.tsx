import { useMutation } from "@tanstack/react-query";
import { useState } from "react";

import { resendVerification } from "../api/client";
import { describeSignupError, plausibleEmail } from "../lib/registration";
import { Turnstile } from "./Turnstile";

const inputClass = "rounded-md border border-white/10 bg-neutral-900 px-3 py-2 outline-none focus:border-white/40";

/** "Resend email": the address, a challenge, and the api's one answer. */
export function ResendVerification({ siteKey, initialEmail = "" }: { siteKey: string | null; initialEmail?: string }) {
  const [email, setEmail] = useState(initialEmail);
  const [token, setToken] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [error, setError] = useState<string | null>(null);

  const resend = useMutation({
    mutationFn: () => resendVerification(email.trim(), token ?? ""),
    onSettled: () => {
      // A token is good once: a new challenge for the next try.
      setToken(null);
      setAttempt((n) => n + 1);
    },
    onError: (err) => {
      setError(describeSignupError(err));
    },
  });

  if (siteKey === null) {
    return <p className="text-sm text-neutral-400">Resending is unavailable right now. Try again later.</p>;
  }
  return (
    <form
      noValidate
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        setError(null);
        if (!plausibleEmail(email)) setError("Enter the address you signed up with.");
        else if (token === null) setError("Complete the check first.");
        else resend.mutate();
      }}
    >
      <label className="flex flex-col gap-1 text-sm">
        Email address
        <input
          type="email"
          autoComplete="email"
          value={email}
          onChange={(e) => {
            setEmail(e.target.value);
            resend.reset();
          }}
          className={inputClass}
        />
      </label>
      <Turnstile key={attempt} siteKey={siteKey} onToken={setToken} />
      {error && (
        <p role="alert" className="text-sm text-red-300">
          {error}
        </p>
      )}
      {resend.isSuccess && (
        <p role="status" className="text-sm text-emerald-300">
          If that address has an account waiting, a new mail is on its way. The link still expires 24 hours after you
          signed up.
        </p>
      )}
      <button
        type="submit"
        disabled={resend.isPending}
        className="self-start rounded-md bg-white/10 px-3 py-1.5 text-sm hover:bg-white/20 disabled:opacity-50"
      >
        {resend.isPending ? "Sending…" : "Resend email"}
      </button>
    </form>
  );
}
