import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { Link, Navigate } from "react-router";

import { register } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { useAuthConfig } from "../auth/useAuthConfig";
import { GoogleButton } from "../components/GoogleButton";
import { ResendVerification } from "../components/ResendVerification";
import { Turnstile } from "../components/Turnstile";
import { checkRegistration, describeSignupError } from "../lib/registration";

const inputClass = "rounded-md border border-white/10 bg-neutral-900 px-3 py-2 outline-none focus:border-white/40";

export function RegisterPage() {
  const { state } = useAuth();
  const config = useAuthConfig();
  const [email, setEmail] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [token, setToken] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [sentTo, setSentTo] = useState<string | null>(null);

  const signup = useMutation({
    mutationFn: (siteToken: string) =>
      register({ email: email.trim(), username: username.trim(), password, turnstile_token: siteToken }),
    onSuccess: () => {
      setSentTo(email.trim());
      setPassword("");
      setConfirm("");
    },
    onError: (err) => {
      setError(describeSignupError(err));
      // A token is good once: a new challenge for the next try.
      setToken(null);
      setAttempt((n) => n + 1);
    },
  });

  if (state.status === "authenticated") return <Navigate to="/" replace />;

  const shell = (children: React.ReactNode) => (
    <main className="mx-auto flex max-w-sm flex-col gap-6 px-4 py-16">{children}</main>
  );

  if (config.isPending) return shell(<div className="h-64 animate-pulse rounded-md bg-neutral-900" aria-label="Loading" />);
  if (config.isError) return shell(<p className="text-sm text-red-300">{describeSignupError(config.error)}</p>);
  const siteKey = config.data.turnstile_site_key;
  if (!config.data.registration_open || siteKey === null) {
    return shell(
      <>
        <h1 className="text-2xl font-semibold">Register</h1>
        <p className="text-sm text-neutral-300">
          {config.data.registration_open ? "Registration is unavailable right now." : "Registration is closed."}
        </p>
        <Link to="/login" className="text-sm underline hover:text-white/80">
          Log in
        </Link>
      </>,
    );
  }

  if (sentTo !== null) {
    return shell(
      <>
        <h1 className="text-2xl font-semibold">Check your email</h1>
        <p className="text-sm text-neutral-300">
          If <span className="font-medium text-neutral-100">{sentTo}</span> can be used, we sent it a link to confirm
          the address. Open it within 24 hours to finish creating your account. Nothing there? Check the spam folder,
          or send it again:
        </p>
        <ResendVerification siteKey={siteKey} initialEmail={sentTo} />
        <Link to="/login" className="text-sm underline hover:text-white/80">
          Back to log in
        </Link>
      </>,
    );
  }

  const field = (
    label: string,
    value: string,
    set: (v: string) => void,
    props: React.InputHTMLAttributes<HTMLInputElement>,
  ) => (
    <label className="flex flex-col gap-1 text-sm">
      {label}
      <input
        {...props}
        value={value}
        onChange={(e) => {
          set(e.target.value);
          setError(null);
        }}
        className={inputClass}
      />
    </label>
  );

  return shell(
    <>
      <h1 className="text-2xl font-semibold">Register</h1>
      {config.data.google_enabled && (
        <>
          <GoogleButton />
          <p className="text-center text-xs text-neutral-500">or register with an email address</p>
        </>
      )}
      <form
        noValidate // checkRegistration applies the api's rules, with its messages
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          const problem = checkRegistration({ email, username, password, confirm });
          if (problem) setError(problem);
          else if (token === null) setError("Complete the check first.");
          else signup.mutate(token);
        }}
      >
        {field("Email address", email, setEmail, { type: "email", name: "email", autoComplete: "email" })}
        {field("Username", username, setUsername, {
          name: "username",
          autoComplete: "username",
          autoCapitalize: "none",
          spellCheck: false,
        })}
        <p className="-mt-2 text-xs text-neutral-500">Other people can see your username.</p>
        {field("Password", password, setPassword, { type: "password", name: "password", autoComplete: "new-password" })}
        {field("Confirm password", confirm, setConfirm, {
          type: "password",
          name: "confirm",
          autoComplete: "new-password",
        })}
        <Turnstile key={attempt} siteKey={siteKey} onToken={setToken} />
        {error && (
          <p role="alert" className="text-sm text-red-300">
            {error}
          </p>
        )}
        <button
          type="submit"
          disabled={signup.isPending}
          className="rounded-md bg-white/90 px-3 py-2 font-medium text-neutral-900 hover:bg-white disabled:opacity-50"
        >
          {signup.isPending ? "Creating account…" : "Create account"}
        </button>
      </form>
      <p className="text-sm text-neutral-400">
        Already have an account?{" "}
        <Link to="/login" className="underline hover:text-white/80">
          Log in
        </Link>
      </p>
    </>,
  );
}
