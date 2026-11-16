import { describe, expect, it } from "vitest";

import { type AdminMovie, type AdminUser, ApiError, type Job } from "../api/client";
import {
  actionLabel,
  canDelete,
  canFeature,
  canRetranscode,
  canRetry,
  confirmDelete,
  confirmRetranscode,
  confirmUserAction,
  describeAdminError,
  jobBackend,
  retranscodeNote,
  tabFrom,
  userActions,
} from "./admin";

const at = "2026-01-02T03:04:05Z";

function movie(over: Partial<AdminMovie>): AdminMovie {
  return {
    id: "m",
    title: "Night Train",
    year: 2020,
    overview: null,
    status: "ready",
    featured_at: null,
    published_at: null,
    poster: null,
    backdrop_url: null,
    active_job: null,
    active_job_purpose: null,
    library_cleanup_after: null,
    created_at: at,
    updated_at: at,
    ...over,
  };
}

function user(over: Partial<AdminUser>): AdminUser {
  return {
    id: "u",
    username: "tester1",
    email: null,
    email_verified_at: null,
    role: "user",
    google_linked: false,
    has_password: true,
    disabled_at: null,
    created_at: at,
    ...over,
  };
}

describe("tabFrom", () => {
  it.each([
    [null, "users"],
    ["jobs", "jobs"],
    ["movies", "movies"],
    ["settings", "users"],
  ])("%s → %s", (param, want) => {
    expect(tabFrom(param)).toBe(want);
  });
});

describe("movie buttons", () => {
  it.each([
    ["failed, nothing active", movie({ status: "failed" }), true, true],
    ["failed, retry queued", movie({ status: "failed", active_job: "queued" }), false, false],
    ["ready", movie({}), false, true],
    ["processing", movie({ status: "processing", active_job: "running" }), false, false],
    ["pending without a job", movie({ status: "pending" }), false, true],
    ["deleting", movie({ status: "deleting" }), false, false],
  ])("%s: retry %s, delete %s", (_, m, retry, del) => {
    expect(canRetry(m)).toBe(retry);
    expect(canDelete(m)).toBe(del);
  });

  it.each([
    ["ready", movie({}), true, null],
    ["ready, retranscode queued", movie({ active_job: "queued", active_job_purpose: "retranscode" }), false, "retranscode queued"],
    ["ready, retranscoding", movie({ active_job: "running", active_job_purpose: "retranscode" }), false, "retranscoding"],
    ["failed", movie({ status: "failed" }), false, null],
    ["processing an ingest", movie({ status: "processing", active_job: "running", active_job_purpose: "ingest" }), false, null],
  ] as const)("%s: retranscode %s, note %s", (_, m, can, note) => {
    expect(canRetranscode(m)).toBe(can);
    expect(retranscodeNote(m)).toBe(note);
  });

  it("the retranscode dialog names the title", () => {
    expect(confirmRetranscode(movie({}))).toContain("“Night Train”");
  });

  it("the delete dialog names the title", () => {
    expect(confirmDelete(movie({}))).toContain("“Night Train”");
  });
});

describe("user actions", () => {
  it("an enabled user: Disable and Make admin", () => {
    const u = user({});
    expect(userActions(u).map(actionLabel)).toEqual(["Disable", "Make admin"]);
  });

  it("a disabled admin: Enable and Make user", () => {
    const u = user({ role: "admin", disabled_at: at });
    expect(userActions(u).map(actionLabel)).toEqual(["Enable", "Make user"]);
  });

  it("every dialog names the user", () => {
    for (const u of [user({}), user({ role: "admin", disabled_at: at })]) {
      for (const a of userActions(u)) expect(confirmUserAction(a, u)).toContain("tester1");
    }
  });
});

describe("jobBackend", () => {
  const base: Job = {
    id: "j",
    movie_id: "m",
    movie_title: "t",
    status: "running",
    purpose: "ingest",
    started_at: at,
    finished_at: null,
    error_msg: null,
    k8s_job_name: null,
    local_pid: null,
    created_at: at,
  };
  it.each([
    [{ k8s_job_name: "transcode-abc" }, "Job transcode-abc"],
    [{ local_pid: 4242 }, "pid 4242"],
    [{}, "—"],
  ])("%o → %s", (over, want) => {
    expect(jobBackend({ ...base, ...over })).toBe(want);
  });
});

describe("describeAdminError", () => {
  it.each([
    ["last_admin", "That is the last active admin. Make someone else an admin first."],
    ["self_change", "You cannot disable your own account or change your own role."],
    ["job_active", "This movie has a queued or running transcode job. Wait for it to finish."],
    ["forbidden", "Only admins can do this. Your admin role may have been removed."],
    ["internal", "The server had a problem. Try again in a moment."],
    ["not_published", "Publish the movie before featuring it."],
    ["movie_published", "The movie is published. Unpublish it first."],
    ["missing_poster", "Upload a poster before publishing."],
    ["missing_overview", "Write a description before publishing."],
    ["too_large", "The image is larger than 10 MB."],
  ] as const)("%s", (code, want) => {
    expect(describeAdminError(new ApiError(409, code, "x"))).toBe(want);
  });
});

describe("describeAdminError: invalid_image", () => {
  it("passes on the api's own sentence: which rule the image broke", () => {
    expect(describeAdminError(new ApiError(400, "invalid_image", "the image must be at least 300×450 pixels"))).toBe(
      "The image must be at least 300×450 pixels.",
    );
  });
});

describe("canFeature", () => {
  it("features only a published movie that is not featured yet", () => {
    expect(canFeature(movie({ published_at: at }))).toBe(true);
    expect(canFeature(movie({}))).toBe(false); // ready, not published
    expect(canFeature(movie({ published_at: at, featured_at: at }))).toBe(false);
    for (const status of ["pending", "processing", "failed", "deleting"] as const) {
      expect(canFeature(movie({ status }))).toBe(false);
    }
  });
});
