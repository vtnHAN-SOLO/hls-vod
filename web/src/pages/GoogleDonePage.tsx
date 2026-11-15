import { Link, Navigate } from "react-router";

import { useAuth } from "../auth/AuthProvider";

/**
 * Where the api's Google callback lands after a sign-in. It set only the
 * HttpOnly refresh cookie (no token in any URL); the session's start-up
 * refresh turns it into an access token. Then on to the catalog.
 */
export function GoogleDonePage() {
  const { state } = useAuth();
  if (state.status === "authenticated") return <Navigate to="/" replace />;
  return (
    <main className="mx-auto flex max-w-sm flex-col gap-6 px-4 py-16">
      <h1 className="text-2xl font-semibold">Signing in with Google</h1>
      {state.status === "unknown" ? (
        <p role="status" className="text-sm text-neutral-300">
          Signing you in…
        </p>
      ) : (
        <>
          <p role="alert" className="text-sm text-red-300">
            The sign-in did not complete.
          </p>
          <Link to="/login" className="text-sm underline hover:text-white/80">
            Back to log in
          </Link>
        </>
      )}
    </main>
  );
}
