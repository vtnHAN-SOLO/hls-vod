// The register form's rules: the api's (docs/openapi.yaml, RegisterRequest),
// checked before sending so a typo never costs a challenge or one of the
// attempts the api allows per hour.
import { ApiError } from "../api/client";
import { MAX_PASSWORD_LENGTH, MIN_PASSWORD_LENGTH } from "./password";

/** Must match auth.CheckUsername. */
export const USERNAME_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
export const MAX_EMAIL_LENGTH = 254;

export interface Registration {
  email: string;
  username: string;
  password: string;
  confirm: string;
}

/** A bare address: one @, no spaces, no display name or angle brackets. */
export function plausibleEmail(email: string): boolean {
  const e = email.trim();
  return e.length <= MAX_EMAIL_LENGTH && /^[^\s@<>,;"]+@[^\s@<>,;"]+$/.test(e);
}

/** Why the form cannot be sent, or null when it can. */
export function checkRegistration({ email, username, password, confirm }: Registration): string | null {
  if (email.trim() === "") return "Enter your email address.";
  if (!plausibleEmail(email)) return "Enter a plain email address, like name@example.com.";
  if (!USERNAME_PATTERN.test(username.trim().toLowerCase())) {
    return "Usernames are 1 to 64 characters: a-z, 0-9, '.', '_' or '-', starting with a letter or digit.";
  }
  const n = Array.from(password).length; // code points, as the api counts
  if (n < MIN_PASSWORD_LENGTH) return `The password must be at least ${String(MIN_PASSWORD_LENGTH)} characters.`;
  if (n > MAX_PASSWORD_LENGTH) return `The password must be at most ${String(MAX_PASSWORD_LENGTH)} characters.`;
  if (confirm !== password) return "The two passwords do not match.";
  return null;
}

/** A sentence for a refused register, resend, verify, forgot or reset. */
export function describeSignupError(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "username_taken":
        return "That username is taken. Choose another one.";
      case "registration_closed":
        return "Registration is closed.";
      case "captcha_failed":
        return "The check did not go through. Complete it again.";
      case "captcha_unavailable":
        return "The check cannot be verified right now. Try again in a few minutes.";
      case "rate_limited":
        return "Too many attempts from your network. Try again in an hour.";
      case "invalid_token":
        return "This link is invalid, expired or already used.";
      case "invalid_request":
        return `The api refused it: ${err.message}.`;
      case "network":
        return "Could not reach the server. Try again in a moment.";
      default:
        break;
    }
  }
  return "The server had a problem. Try again in a moment.";
}

/**
 * The token of a mailed link (/verify-email or /reset-password: #token=…),
 * or null when there is none.
 */
export function tokenFromHash(hash: string): string | null {
  const token = new URLSearchParams(hash.replace(/^#/, "")).get("token");
  return token !== null && /^[A-Za-z0-9_-]{43}$/.test(token) ? token : null;
}
