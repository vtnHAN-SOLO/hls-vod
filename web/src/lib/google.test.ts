import { describe, expect, it } from "vitest";

import { describeGoogleError } from "./google";

describe("describeGoogleError", () => {
  it.each([
    [null, null],
    ["", null],
    ["state", "The sign-in took too long or was started in another tab. Try again."],
    ["registration_closed", "There is no account for this Google account, and registration is closed."],
    ["linked_elsewhere", "That Google account is already connected to another account here."],
    ["account_disabled", "This account has been disabled by an admin."],
    ["something-new", "The server had a problem. Try again in a moment."],
  ])("%s → %s", (code, want) => {
    expect(describeGoogleError(code)).toBe(want);
  });
});
