// The change-password form's rules: the api's (docs/openapi.yaml,
// PasswordChangeRequest), checked before sending so a typo in the
// confirmation never costs one of the five attempts the api allows.
import { ApiError } from "../api/client";

// Must match new_password's minLength / maxLength in docs/openapi.yaml.
export const MIN_PASSWORD_LENGTH = 12;
export const MAX_PASSWORD_LENGTH = 1024;

export interface PasswordChange {
  current: string;
  next: string;
  confirm: string;
}

/**
 * Characters as the api counts them: code points (Go runes), not UTF-16
 * units and not grapheme clusters, so 😀 is 1 and 👍🏽 is 2, as on the api.
 */
function length(s: string): number {
  return Array.from(s).length;
}

/** Why the form cannot be sent, or null when it can. */
export function checkPasswordChange({ current, next, confirm }: PasswordChange): string | null {
  if (current === "") return "Enter your current password.";
  if (length(next) < MIN_PASSWORD_LENGTH) {
    return `The new password must be at least ${String(MIN_PASSWORD_LENGTH)} characters.`;
  }
  if (length(next) > MAX_PASSWORD_LENGTH) {
    return `The new password must be at most ${String(MAX_PASSWORD_LENGTH)} characters.`;
  }
  if (next === current) return "The new password must differ from the current one.";
  if (confirm !== next) return "The two new passwords do not match.";
  return null;
}

/** A sentence for a refused password change. */
export function describePasswordChangeError(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "invalid_credentials":
        return "The current password is not correct.";
      case "rate_limited":
        return "Too many wrong passwords. Wait 15 minutes and try again.";
      case "invalid_request":
        return `The api refused it: ${err.message}.`;
      case "unauthorized":
        return "Your session has ended. Log in again.";
      case "network":
        return "Could not reach the server. Your password was not changed.";
      default:
        break;
    }
  }
  return "The server had a problem; your password was not changed. Try again in a moment.";
}
