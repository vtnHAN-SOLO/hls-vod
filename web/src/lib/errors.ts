import { ApiError } from "../api/client";

/** A sentence for the user; details stay in the server log. */
export function describeError(err: unknown): string {
  if (!(err instanceof ApiError)) return "Something went wrong.";
  switch (err.code) {
    case "not_found":
      return "There is no movie with this id.";
    case "not_ready":
      return `This movie is not ready to play yet: ${err.message}.`;
    case "invalid_request":
      return err.message.startsWith("id:") ? "That is not a valid movie id." : `Invalid request: ${err.message}.`;
    case "unauthorized":
      return "Your session has ended. Log in again.";
    case "invalid_credentials":
      return "Wrong username or password.";
    case "rate_limited":
      return "Too many failed attempts. Wait 15 minutes and try again.";
    case "forbidden":
      return "You are not allowed to do that.";
    case "account_disabled":
      return "This account has been disabled by an admin.";
    case "network":
      return "Could not reach the server. Is `make run` running?";
    default:
      return "The server had a problem. Try again in a moment.";
  }
}

/** Only network and server failures can succeed on a retry; a 4xx will not. */
export function isRetryable(err: unknown): boolean {
  return !(err instanceof ApiError) || err.status === 0 || err.status >= 500;
}
