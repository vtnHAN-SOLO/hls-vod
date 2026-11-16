import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router";

import { useAuth } from "../auth/AuthProvider";
import { useAuthConfig } from "../auth/useAuthConfig";
import { useLinkToken } from "../auth/useLinkToken";
import { ResendVerification } from "../components/ResendVerification";
import { describeSignupError } from "../lib/registration";
import { AuthShell } from "../components/AuthShell";

// A token is good once. React StrictMode runs effects twice, so the post
// for a token is made once and shared.
const verifying = new Map<string, Promise<void>>();

/**
 * Opened from the mail: /verify-email#token=…. The token is in the
 * fragment, which browsers never send to a server; it is read, removed from
 * the address bar (so it is not left in history or bookmarks), and posted.
 */
export function VerifyEmailPage() {
  const { verifyEmail } = useAuth();
  const navigate = useNavigate();
  const config = useAuthConfig();
  const token = useLinkToken(); // first: its effect clears the fragment before ours posts
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (token === null) return;
    let active = true;
    let p = verifying.get(token);
    if (!p) {
      p = verifyEmail(token);
      verifying.set(token, p);
    }
    p.then(
      () => {
        if (active) void navigate("/", { replace: true });
      },
      (err: unknown) => {
        if (active) setError(describeSignupError(err));
      },
    );
    return () => {
      active = false;
    };
  }, [token, verifyEmail, navigate]);

  const failed = token === null ? "This link is incomplete. Open the link from the mail again." : error;
  return (
    <AuthShell>
      <h1 className="text-2xl font-bold">Confirm your email</h1>
      {failed === null ? (
        <p role="status" className="text-sm text-fg-muted">
          Confirming your address…
        </p>
      ) : (
        <>
          <p role="alert" className="text-sm text-danger">
            {failed}
          </p>
          <p className="text-sm text-fg-muted">
            Already confirmed?{" "}
            <Link to="/login" className="underline hover:text-fg">
              Log in
            </Link>
            . Otherwise ask for a new mail:
          </p>
          {config.data && <ResendVerification siteKey={config.data.turnstile_site_key} />}
        </>
      )}
    </AuthShell>
  );
}
