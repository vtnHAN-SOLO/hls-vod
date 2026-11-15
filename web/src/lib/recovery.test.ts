import { describe, expect, it } from "vitest";

import { checkNewPassword } from "./recovery";

describe("checkNewPassword", () => {
  it.each([
    ["correct horse battery", "correct horse battery", null],
    ["short", "short", "The password must be at least 12 characters."],
    ["😀".repeat(12), "😀".repeat(12), null],
    ["x".repeat(1025), "x".repeat(1025), "The password must be at most 1024 characters."],
    ["correct horse battery", "correct horse", "The two passwords do not match."],
  ])("%s / %s → %s", (password, confirm, want) => {
    expect(checkNewPassword(password, confirm)).toBe(want);
  });
});
