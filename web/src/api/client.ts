// Typed API client. Every type comes from schema.ts, generated from
// docs/openapi.yaml by openapi-typescript (`pnpm gen:api`); nothing here is
// hand-written, so a spec change that breaks the client fails typecheck.
import createClient from "openapi-fetch";

import { type GrantResult, Session, Unauthenticated, webLock } from "../lib/session";
import type { components, paths } from "./schema";

export type Movie = components["schemas"]["Movie"];
export type MovieList = components["schemas"]["MovieList"];
export type MovieStatus = components["schemas"]["MovieStatus"];
export type MovieDetail = components["schemas"]["MovieDetail"];
export type Playback = components["schemas"]["Playback"];
export type ContinueWatching = components["schemas"]["ContinueWatching"];
export type ContinueWatchingItem = components["schemas"]["ContinueWatchingItem"];
export type User = components["schemas"]["User"];
export type Account = components["schemas"]["Account"];
export type LoginSession = components["schemas"]["LoginSession"];
export type SessionList = components["schemas"]["SessionList"];
export type AuthConfig = components["schemas"]["AuthConfig"];
export type RegisterRequest = components["schemas"]["RegisterRequest"];
export type AdminUser = components["schemas"]["AdminUser"];
export type AdminUserList = components["schemas"]["AdminUserList"];
export type Job = components["schemas"]["Job"];
export type JobList = components["schemas"]["JobList"];
export type JobStatus = components["schemas"]["JobStatus"];
export type JobPurpose = components["schemas"]["JobPurpose"];
export type AdminMovie = components["schemas"]["AdminMovie"];
export type AdminMovieList = components["schemas"]["AdminMovieList"];
export type PopularList = components["schemas"]["PopularList"];
export type PopularItem = components["schemas"]["PopularItem"];
export type FeaturedList = components["schemas"]["FeaturedList"];
export type MovieEditRequest = components["schemas"]["MovieEditRequest"];
type PopularQuery = NonNullable<paths["/api/v1/movies/popular"]["get"]["parameters"]["query"]>;
export type Role = components["schemas"]["RoleRequest"]["role"];
type AdminUsersQuery = NonNullable<paths["/api/v1/admin/users"]["get"]["parameters"]["query"]>;
type AdminJobsQuery = NonNullable<paths["/api/v1/admin/jobs"]["get"]["parameters"]["query"]>;
type AdminMoviesQuery = NonNullable<paths["/api/v1/admin/movies"]["get"]["parameters"]["query"]>;
type ProgressRequest = components["schemas"]["ProgressRequest"];
export type ListMoviesQuery = NonNullable<paths["/api/v1/movies"]["get"]["parameters"]["query"]>;
type ErrorCode = components["schemas"]["Error"]["error"]["code"];
type SessionBody = components["schemas"]["Session"];

// refresh and logout must carry it (the api's CSRF defence); any value works.
const CSRF = { "X-Requested-With": "XMLHttpRequest" } as const;

// Same origin: the Vite proxy in dev, the Ingress in the cluster. The client
// never knows a host name.
const api = createClient<paths>({ baseUrl: "" });

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: ErrorCode | "network",
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

type Result<T> =
  | { data: T; error?: never; response: Response }
  | { data?: never; error: components["schemas"]["Error"]; response: Response };

function unwrap<T>(r: Result<T>): T {
  if (r.error) throw new ApiError(r.response.status, r.error.error.code, r.error.error.message);
  return r.data;
}

async function call<T>(req: () => Promise<Result<T>>): Promise<T> {
  let r: Result<T>;
  try {
    r = await req();
  } catch (e) {
    throw asApiError(e);
  }
  return unwrap(r);
}

function asApiError(e: unknown): ApiError {
  if (e instanceof ApiError) return e;
  if (e instanceof Unauthenticated) return new ApiError(401, "unauthorized", "not logged in");
  return new ApiError(0, "network", e instanceof Error ? e.message : "network error");
}

function grant(s: SessionBody) {
  return { accessToken: s.access_token, user: s.user };
}

/** The one session of this tab. See src/lib/session.ts for the rules. */
export const session = new Session(
  {
    refresh: async (): Promise<GrantResult> => {
      try {
        const r = await api.POST("/api/v1/auth/refresh", { params: { header: CSRF } });
        if (r.data) return { kind: "granted", grant: grant(r.data) };
        if (r.response.status === 401) return { kind: "refused" };
        return { kind: "failed", error: new ApiError(r.response.status, r.error.error.code, r.error.error.message) };
      } catch (e) {
        return { kind: "failed", error: asApiError(e) };
      }
    },
    logout: async () => {
      // Best effort: the token is already forgotten here; a failure leaves
      // only the cookie, which the next login replaces.
      await api.POST("/api/v1/auth/logout", { params: { header: CSRF } }).catch(() => undefined);
    },
  },
  webLock(),
);

/** An authenticated call: the access token attached, one refresh-and-retry on a 401. */
function authed<T>(req: (headers: { Authorization: string }) => Promise<Result<T>>): Promise<T> {
  return call(() => session.withAuth((token) => req({ Authorization: `Bearer ${token}` })));
}

/** turnstileToken: a solved challenge, needed after 3 failures (captcha_required). */
export async function login(username: string, password: string, turnstileToken?: string): Promise<User> {
  const s = await call(() =>
    api.POST("/api/v1/auth/login", {
      body: turnstileToken ? { username, password, turnstile_token: turnstileToken } : { username, password },
    }),
  );
  session.granted(grant(s));
  return s.user;
}

/** Whether registration is open, and the Turnstile site key. Public. */
export function getAuthConfig(): Promise<AuthConfig> {
  return call(() => api.GET("/api/v1/auth/config"));
}

/** 202 whether or not the address has an account; the mail tells the rest. */
export async function register(body: RegisterRequest): Promise<void> {
  await call(() => api.POST("/api/v1/auth/register", { body }));
}

/** Mails a new verify link, if the address has an account waiting. Always 202. */
export async function resendVerification(email: string, turnstileToken: string): Promise<void> {
  await call(() => api.POST("/api/v1/auth/resend-verification", { body: { email, turnstile_token: turnstileToken } }));
}

/** Confirms the address with the link's token; answered like a login. */
export async function verifyEmail(token: string): Promise<User> {
  const s = await call(() => api.POST("/api/v1/auth/verify-email", { body: { token } }));
  session.granted(grant(s));
  return s.user;
}

/** Mails a reset link if the address belongs to a verified account. Always 202. */
export async function forgotPassword(email: string, turnstileToken: string): Promise<void> {
  await call(() => api.POST("/api/v1/auth/forgot-password", { body: { email, turnstile_token: turnstileToken } }));
}

/** Sets a new password with the link's token. Every session ends; no new one starts. */
export async function resetPassword(token: string, newPassword: string): Promise<void> {
  await call(() => api.POST("/api/v1/auth/reset-password", { body: { token, new_password: newPassword } }));
}

export function listMovies(query: ListMoviesQuery): Promise<MovieList> {
  return authed((headers) => api.GET("/api/v1/movies", { params: { query }, headers }));
}

export function getMovie(id: string): Promise<MovieDetail> {
  return authed((headers) => api.GET("/api/v1/movies/{id}", { params: { path: { id } }, headers }));
}

export function getPlayback(id: string): Promise<Playback> {
  return authed((headers) => api.GET("/api/v1/movies/{id}/playback", { params: { path: { id } }, headers }));
}

/** Ready movies ranked by distinct viewers in the window; only movies someone watched. */
export function popularMovies(query: PopularQuery): Promise<PopularList> {
  return authed((headers) => api.GET("/api/v1/movies/popular", { params: { query }, headers }));
}

/** The spotlight: featured movies, or the newest ready ones when none is featured. */
export function featuredMovies(): Promise<FeaturedList> {
  return authed((headers) => api.GET("/api/v1/movies/featured", { headers }));
}

export function continueWatching(): Promise<ContinueWatching> {
  return authed((headers) => api.GET("/api/v1/continue-watching", { headers }));
}

export function saveProgress(movieId: string, positionSec: number): Promise<undefined> {
  return authed((headers) =>
    api.POST("/api/v1/progress", { body: { movie_id: movieId, position_sec: positionSec }, headers }),
  );
}

export function getMe(): Promise<Account> {
  return authed((headers) => api.GET("/api/v1/me", { headers }));
}

/**
 * Changes the password. The api revokes every session and hands this tab a
 * new one (token + refresh cookie), which the session takes over; it runs
 * under the refresh lock (session.reissue), so no tab refreshes with the
 * cookie that is being revoked.
 */
export async function changePassword(current: string, next: string): Promise<void> {
  await call(() =>
    session.reissue(
      (token) =>
        api.POST("/api/v1/me/password", {
          body: { current_password: current, new_password: next },
          headers: { Authorization: `Bearer ${token}` },
        }),
      (r) => (r.data ? grant(r.data) : null),
    ),
  );
}

/** "Continue with Google": a plain link (a navigation, not a fetch). */
export const GOOGLE_START_URL = "/api/v1/auth/google/start";

/**
 * Starts linking Google (needs the current password); resolves to the URL to
 * navigate to. The api set the state cookie the callback needs.
 */
export async function linkGoogle(currentPassword: string): Promise<string> {
  const r = await authed((headers) =>
    api.POST("/api/v1/me/google/link", { body: { current_password: currentPassword }, headers }),
  );
  return r.url;
}

/** Removes the linked Google account (refused without a password). */
export function unlinkGoogle(): Promise<undefined> {
  return authed((headers) => api.DELETE("/api/v1/me/google", { headers }));
}

/** "Set password" for an account without one: a link to its own email. Always 202. */
export async function requestPasswordSetLink(): Promise<void> {
  await authed((headers) => api.POST("/api/v1/me/password/set-link", { headers }));
}

export function listSessions(): Promise<SessionList> {
  return authed((headers) => api.GET("/api/v1/me/sessions", { headers }));
}

export function revokeSession(id: string): Promise<undefined> {
  return authed((headers) => api.DELETE("/api/v1/me/sessions/{id}", { params: { path: { id } }, headers }));
}

export function revokeOtherSessions(): Promise<undefined> {
  return authed((headers) => api.POST("/api/v1/me/sessions/revoke-others", { headers }));
}

// The admin area. The api checks the role in the database on every call, so
// a demoted admin gets 403 here even while their token still says admin.

export function adminListUsers(query: AdminUsersQuery): Promise<AdminUserList> {
  return authed((headers) => api.GET("/api/v1/admin/users", { params: { query }, headers }));
}

/** Disabling revokes every session of the user at once. */
export function adminSetDisabled(id: string, disabled: boolean): Promise<AdminUser> {
  const params = { path: { id } };
  return authed((headers) =>
    disabled
      ? api.POST("/api/v1/admin/users/{id}/disable", { params, headers })
      : api.POST("/api/v1/admin/users/{id}/enable", { params, headers }),
  );
}

export function adminSetRole(id: string, role: Role): Promise<AdminUser> {
  return authed((headers) => api.PUT("/api/v1/admin/users/{id}/role", { params: { path: { id } }, body: { role }, headers }));
}

export function adminListJobs(query: AdminJobsQuery): Promise<JobList> {
  return authed((headers) => api.GET("/api/v1/admin/jobs", { params: { query }, headers }));
}

export function adminListMovies(query: AdminMoviesQuery): Promise<AdminMovieList> {
  return authed((headers) => api.GET("/api/v1/admin/movies", { params: { query }, headers }));
}

/** A failed movie: pending again, with a new queued job. */
export async function adminRetryMovie(id: string): Promise<void> {
  await authed((headers) => api.POST("/api/v1/admin/movies/{id}/retry", { params: { path: { id } }, headers }));
}

/** A ready movie into a new version; it keeps playing the current one meanwhile. */
export async function adminRetranscodeMovie(id: string): Promise<void> {
  await authed((headers) => api.POST("/api/v1/admin/movies/{id}/retranscode", { params: { path: { id } }, headers }));
}

/** Hidden from users at once; the watcher removes its files within 5 minutes. */
export async function adminDeleteMovie(id: string): Promise<void> {
  await authed((headers) => api.DELETE("/api/v1/admin/movies/{id}", { params: { path: { id } }, headers }));
}

/** Edits title, year or overview; a field left out stays as it is. */
export function adminEditMovie(id: string, body: MovieEditRequest): Promise<AdminMovie> {
  return authed((headers) => api.PATCH("/api/v1/admin/movies/{id}", { params: { path: { id } }, body, headers }));
}

/** Features a ready movie in the home page's spotlight, or takes it out. */
export async function adminSetFeatured(id: string, featured: boolean): Promise<void> {
  const params = { path: { id } };
  await authed((headers) =>
    featured
      ? api.POST("/api/v1/admin/movies/{id}/feature", { params, headers })
      : api.DELETE("/api/v1/admin/movies/{id}/feature", { params, headers }),
  );
}

/** Publishes a movie (409 with the reason when it lacks something), or unpublishes it. */
export async function adminSetPublished(id: string, published: boolean): Promise<void> {
  const params = { path: { id } };
  await authed((headers) =>
    published
      ? api.POST("/api/v1/admin/movies/{id}/publish", { params, headers })
      : api.DELETE("/api/v1/admin/movies/{id}/publish", { params, headers }),
  );
}

/** Removes a movie's poster (refused while it is published). */
export async function adminDeletePoster(id: string): Promise<void> {
  await authed((headers) => api.DELETE("/api/v1/admin/movies/{id}/poster", { params: { path: { id } }, headers }));
}

type PosterUploadBody =
  paths["/api/v1/admin/movies/{id}/poster"]["put"]["responses"]["200"]["content"]["application/json"];

/**
 * Uploads a poster image as the request body, reporting progress (0..1).
 * XMLHttpRequest, not fetch: only it reports upload progress. It still goes
 * through session.withAuth, so a 401 refreshes once and retries.
 */
export async function adminUploadPoster(id: string, file: Blob, onProgress?: (loaded: number, total: number) => void): Promise<AdminMovie> {
  const path: keyof paths = "/api/v1/admin/movies/{id}/poster";
  const url = path.replace("{id}", encodeURIComponent(id));
  let r: { response: Response; text: string };
  try {
    r = await session.withAuth((token) => putWithProgress(url, token, file, onProgress));
  } catch (e) {
    throw asApiError(e);
  }
  let body: unknown;
  try {
    body = JSON.parse(r.text);
  } catch {
    throw new ApiError(r.response.status, "internal", "unexpected response");
  }
  if (!r.response.ok) {
    const e = (body as components["schemas"]["Error"]).error;
    throw new ApiError(r.response.status, e.code, e.message);
  }
  return body as PosterUploadBody;
}

function putWithProgress(
  url: string,
  token: string,
  file: Blob,
  onProgress?: (loaded: number, total: number) => void,
): Promise<{ response: Response; text: string }> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", url);
    xhr.setRequestHeader("Authorization", `Bearer ${token}`);
    // The api reads the type from the bytes; this only describes them.
    xhr.setRequestHeader("Content-Type", file.type || "application/octet-stream");
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress?.(e.loaded, e.total);
    };
    xhr.onload = () => {
      resolve({ response: new Response(null, { status: xhr.status }), text: xhr.responseText });
    };
    xhr.onerror = () => {
      reject(new Error("the upload failed: check the connection"));
    };
    xhr.send(file);
  });
}

/**
 * The pagehide save: fetch with keepalive, so the request outlives the page
 * and still carries the Authorization header (sendBeacon cannot). One
 * attempt with the current token, never a refresh: a refresh the page dies
 * during could rotate the cookie on the server while the browser never
 * stores the new one, and the next refresh would then be reuse.
 *
 * Plain fetch(url, init), not openapi-fetch: that one hands fetch a Request
 * object, and Firefox drops a Request's keepalive when the tab closes (the
 * save then never arrives). The body is still typed by the spec.
 */
export function saveProgressOnExit(movieId: string, positionSec: number): void {
  const token = session.accessToken();
  if (token === null) return;
  const body: ProgressRequest = { movie_id: movieId, position_sec: positionSec };
  const path: keyof paths = "/api/v1/progress";
  void fetch(path, {
    method: "POST",
    keepalive: true,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }).catch(() => undefined);
}
