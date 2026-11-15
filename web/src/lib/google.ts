// Sign in with Google: what the SPA tells the user after the api's callback
// redirected it with ?google_error=<code> (docs/openapi.yaml, googleCallback).

const SERVER = "The server had a problem. Try again in a moment.";

const messages: Record<string, string | undefined> = {
  state: "The sign-in took too long or was started in another tab. Try again.",
  denied: "Google sign-in was cancelled.",
  unavailable: "Google cannot be reached right now. Try again in a few minutes.",
  exchange: "Google did not accept the sign-in. Try again.",
  token: "Google's answer could not be verified. Try again.",
  email_unverified: "Your Google account's email address is not verified by Google.",
  registration_closed: "There is no account for this Google account, and registration is closed.",
  linked_elsewhere: "That Google account is already connected to another account here.",
  already_linked: "This account is already connected to another Google account.",
  account_disabled: "This account has been disabled by an admin.",
  disabled: "Sign in with Google is not available.",
  server: SERVER,
};

/** A sentence for a google_error code, or null when there is none. */
export function describeGoogleError(code: string | null): string | null {
  if (code === null || code === "") return null;
  return messages[code] ?? SERVER;
}
