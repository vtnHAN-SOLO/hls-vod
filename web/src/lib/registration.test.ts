import { describe, expect, it } from "vitest";

import { ApiError } from "../api/client";
import { checkRegistration, describeSignupError, plausibleEmail, tokenFromHash } from "./registration";

const ok = { email: "dana@example.com", username: "dana", password: "correct horse battery", confirm: "correct horse battery" };

describe("checkRegistration", () => {
  it.each([
    [{}, null],
    [{ email: "" }, "Enter your email address."],
    [{ email: "Dana <dana@example.com>" }, "Enter a plain email address, like name@example.com."],
    [{ email: "dana.example.com" }, "Enter a plain email address, like name@example.com."],
    [{ email: "a@b.com, c@d.com" }, "Enter a plain email address, like name@example.com."],
    [{ email: `${"a".repeat(250)}@b.co` }, "Enter a plain email address, like name@example.com."],
    [{ username: "Dana" }, null], // lowercased, as the api does
    [{ username: "-dana" }, "Usernames are 1 to 64 characters: a-z, 0-9, '.', '_' or '-', starting with a letter or digit."],
    [{ username: "da na" }, "Usernames are 1 to 64 characters: a-z, 0-9, '.', '_' or '-', starting with a letter or digit."],
    [{ password: "short", confirm: "short" }, "The password must be at least 12 characters."],
    [{ password: "😀".repeat(12), confirm: "😀".repeat(12) }, null],
    [{ confirm: "something else" }, "The two passwords do not match."],
  ])("%o → %s", (patch, want) => {
    expect(checkRegistration({ ...ok, ...patch })).toBe(want);
  });

  it("accepts a padded address", () => {
    expect(plausibleEmail("  dana@example.com ")).toBe(true);
  });
});

describe("describeSignupError", () => {
  it.each([
    [new ApiError(409, "username_taken", "x"), "That username is taken. Choose another one."],
    [new ApiError(403, "registration_closed", "x"), "Registration is closed."],
    [new ApiError(400, "captcha_failed", "x"), "The check did not go through. Complete it again."],
    [new ApiError(503, "captcha_unavailable", "x"), "The check cannot be verified right now. Try again in a few minutes."],
    [new ApiError(400, "invalid_token", "x"), "This link is invalid, expired or already used."],
    [new ApiError(500, "internal", "x"), "The server had a problem. Try again in a moment."],
    [new Error("boom"), "The server had a problem. Try again in a moment."],
  ])("%o", (err, want) => {
    expect(describeSignupError(err)).toBe(want);
  });
});

describe("tokenFromHash", () => {
  const token = "A".repeat(42) + "_";
  it.each([
    [`#token=${token}`, token],
    [`token=${token}`, token],
    [`#x=1&token=${token}`, token],
    ["", null],
    ["#token=", null],
    ["#token=short", null],
    [`#token=${token}%3Cscript%3E`, null],
  ])("%s", (hash, want) => {
    expect(tokenFromHash(hash)).toBe(want);
  });
});
