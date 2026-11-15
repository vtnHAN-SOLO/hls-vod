// The reset-password form's rules: the api's (docs/openapi.yaml,
// ResetPasswordRequest), checked before sending so a typo never costs the
// link a round trip.
import { MAX_PASSWORD_LENGTH, MIN_PASSWORD_LENGTH } from "./password";

/** Why a new password cannot be sent, or null when it can. */
export function checkNewPassword(password: string, confirm: string): string | null {
  const n = Array.from(password).length; // code points, as the api counts
  if (n < MIN_PASSWORD_LENGTH) return `The password must be at least ${String(MIN_PASSWORD_LENGTH)} characters.`;
  if (n > MAX_PASSWORD_LENGTH) return `The password must be at most ${String(MAX_PASSWORD_LENGTH)} characters.`;
  if (confirm !== password) return "The two passwords do not match.";
  return null;
}
