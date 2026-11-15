import { describe, expect, it } from "vitest";

import { ApiError } from "../api/client";
import { describeError, isRetryable } from "./errors";

describe("errors", () => {
  it.each([
    [new ApiError(404, "not_found", "movie not found"), "There is no movie with this id.", false],
    [new ApiError(400, "invalid_request", "id: must be a UUID"), "That is not a valid movie id.", false],
    [new ApiError(400, "invalid_request", "q: must be at most 100 characters"), "Invalid request: q: must be at most 100 characters.", false],
    [new ApiError(409, "not_ready", "movie is pending, not ready"), "This movie is not ready to play yet: movie is pending, not ready.", false],
    [new ApiError(401, "unauthorized", "not logged in"), "Your session has ended. Log in again.", false],
    [new ApiError(401, "invalid_credentials", "invalid username or password"), "Wrong username or password.", false],
    [new ApiError(429, "rate_limited", "too many"), "Too many failed attempts. Wait 15 minutes and try again.", false],
    [new ApiError(0, "network", "Failed to fetch"), "Could not reach the server. Is `make run` running?", true],
    [new ApiError(500, "internal", "internal error"), "The server had a problem. Try again in a moment.", true],
    [new Error("boom"), "Something went wrong.", true],
  ])("%s", (err, text, retry) => {
    expect(describeError(err)).toBe(text);
    expect(isRetryable(err)).toBe(retry);
  });
});
