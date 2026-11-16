import { Link, Navigate } from "react-router";

import { useAuth } from "../auth/AuthProvider";
import { AuthShell } from "../components/AuthShell";

/**
 * Where the api's Google callback lands after a sign-in. It set only the
 * HttpOnly refresh cookie (no token in any URL); the session's start-up
 * refresh turns it into an access token. Then on to the catalog.
 */
export function GoogleDonePage() {
  const { state } = useAuth();
  if (state.status === "authenticated") return <Navigate to="/" replace />;
  return (
    <AuthShell>
      <h1 className="text-2xl font-bold">Signing in with Google</h1>
      {state.status === "unknown" ? (
        <p role="status" className="text-sm text-fg-muted">
          Signing you in…
        </p>
      ) : (
        <>
          <p role="alert" className="text-sm text-danger">
            The sign-in did not complete.
          </p>
          <Link to="/login" className="text-sm underline hover:text-fg">
            Back to log in
          </Link>
        </>
      )}
    </AuthShell>
  );
}
