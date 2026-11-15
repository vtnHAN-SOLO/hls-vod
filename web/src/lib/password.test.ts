import { describe, expect, it } from "vitest";

import { ApiError } from "../api/client";
import { checkPasswordChange, describePasswordChangeError, MAX_PASSWORD_LENGTH } from "./password";

const current = "the current one";
const good = "a new long password";

describe("checkPasswordChange", () => {
  it.each([
    [{ current: "", next: good, confirm: good }, "Enter your current password."],
    [{ current, next: "short", confirm: "short" }, "The new password must be at least 12 characters."],
    // 11 emoji are 22 UTF-16 units but 11 characters, as the api counts them.
    [{ current, next: "😀".repeat(11), confirm: "😀".repeat(11) }, "The new password must be at least 12 characters."],
    [
      { current, next: "a".repeat(MAX_PASSWORD_LENGTH + 1), confirm: "a".repeat(MAX_PASSWORD_LENGTH + 1) },
      "The new password must be at most 1024 characters.",
    ],
    [{ current: good, next: good, confirm: good }, "The new password must differ from the current one."],
    [{ current, next: good, confirm: `${good} ` }, "The two new passwords do not match."],
  ])("%j -> %s", (form, want) => {
    expect(checkPasswordChange(form)).toBe(want);
  });

  it("accepts a valid change", () => {
    expect(checkPasswordChange({ current, next: good, confirm: good })).toBeNull();
    expect(checkPasswordChange({ current, next: "😀".repeat(12), confirm: "😀".repeat(12) })).toBeNull();
  });
});

describe("describePasswordChangeError", () => {
  it.each([
    [new ApiError(400, "invalid_credentials", "x"), "The current password is not correct."],
    [new ApiError(429, "rate_limited", "x"), "Too many wrong passwords. Wait 15 minutes and try again."],
    [new ApiError(400, "invalid_request", "new_password: too short"), "The api refused it: new_password: too short."],
    [new ApiError(0, "network", "x"), "Could not reach the server. Your password was not changed."],
    [new ApiError(500, "internal", "x"), "The server had a problem; your password was not changed. Try again in a moment."],
    [new Error("boom"), "The server had a problem; your password was not changed. Try again in a moment."],
  ])("%s -> %s", (err, want) => {
    expect(describePasswordChangeError(err)).toBe(want);
  });
});
