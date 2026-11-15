// The catalog's APPLIED search lives in the URL
// (?q=&sort=&year_from=&year_to=&status=&page=), so back/forward, reload and
// shared links all show the same list. What the user is typing is a separate
// draft (see CatalogDraft) that only reaches the URL, and so the api, when
// the search is submitted.
import type { ListMoviesQuery, MovieStatus } from "../api/client";

export const PAGE_SIZE = 24;

// Must match `maxLength` of the `q` parameter in docs/openapi.yaml.
export const MAX_QUERY_LENGTH = 100;

// Must match the year_from / year_to bounds in docs/openapi.yaml.
export const MIN_YEAR = 1880;
export const MAX_YEAR = 2100;

// A Record, so typecheck fails if the spec gains a status this UI ignores.
export const STATUS_LABELS: Record<MovieStatus, string> = {
  ready: "Ready",
  processing: "Processing",
  pending: "Pending",
  failed: "Failed",
  deleting: "Deleting",
};

export type MovieSort = NonNullable<ListMoviesQuery["sort"]>;

export const DEFAULT_SORT: MovieSort = "added_desc";

// Same idea: the spec's sort enum, every value labelled.
export const SORT_LABELS: Record<MovieSort, string> = {
  added_desc: "Recently added",
  title_asc: "Title A–Z",
  year_desc: "Newest year first",
  year_asc: "Oldest year first",
};

export function isMovieStatus(s: string): s is MovieStatus {
  return Object.hasOwn(STATUS_LABELS, s);
}

export function isMovieSort(s: string): s is MovieSort {
  return Object.hasOwn(SORT_LABELS, s);
}

export interface CatalogState {
  q: string; // as typed; the API trims it
  sort: MovieSort;
  yearFrom: number | null; // inclusive; null = no bound
  yearTo: number | null;
  status: MovieStatus | null; // null = every status
  page: number; // 1-based
}

export const EMPTY_STATE: CatalogState = {
  q: "",
  sort: DEFAULT_SORT,
  yearFrom: null,
  yearTo: null,
  status: null,
  page: 1,
};

/** A whole year in MIN_YEAR..MAX_YEAR, or null for anything else. */
export function parseYear(s: string | null): number | null {
  if (s === null || !/^\d{4}$/.test(s.trim())) return null;
  const y = Number(s.trim());
  return y >= MIN_YEAR && y <= MAX_YEAR ? y : null;
}

export function readCatalogParams(params: URLSearchParams): CatalogState {
  const status = params.get("status") ?? "";
  const sort = params.get("sort") ?? "";
  const page = Number(params.get("page"));
  return {
    q: (params.get("q") ?? "").slice(0, MAX_QUERY_LENGTH),
    sort: isMovieSort(sort) ? sort : DEFAULT_SORT,
    yearFrom: parseYear(params.get("year_from")),
    yearTo: parseYear(params.get("year_to")),
    status: isMovieStatus(status) ? status : null,
    page: Number.isInteger(page) && page >= 1 ? page : 1,
  };
}

/** The URL for a state: defaults are left out, so "/" means "everything". */
export function writeCatalogParams(state: CatalogState): URLSearchParams {
  const out = new URLSearchParams();
  if (state.q !== "") out.set("q", state.q);
  if (state.sort !== DEFAULT_SORT) out.set("sort", state.sort);
  if (state.yearFrom !== null) out.set("year_from", String(state.yearFrom));
  if (state.yearTo !== null) out.set("year_to", String(state.yearTo));
  if (state.status) out.set("status", state.status);
  if (state.page > 1) out.set("page", String(state.page));
  return out;
}

/** Filters narrow the list; the sort only orders it, so it is not one. */
export function hasFilters(state: CatalogState): boolean {
  return state.q.trim() !== "" || state.status !== null || state.yearFrom !== null || state.yearTo !== null;
}

/** The API request for a state. Blank search is no search; defaults are not sent. */
export function catalogRequest(state: CatalogState): ListMoviesQuery {
  const q = state.q.trim();
  return {
    ...(q !== "" && { q }),
    ...(state.sort !== DEFAULT_SORT && { sort: state.sort }),
    ...(state.yearFrom !== null && { year_from: state.yearFrom }),
    ...(state.yearTo !== null && { year_to: state.yearTo }),
    ...(state.status && { status: state.status }),
    limit: PAGE_SIZE,
    offset: (state.page - 1) * PAGE_SIZE,
  };
}

/** The filters of a state in words, for "No movies …": `matching “x” from 1990 to 1999`. */
export function describeFilters(state: CatalogState): string {
  const q = state.q.trim();
  const years =
    state.yearFrom !== null && state.yearTo !== null
      ? state.yearFrom === state.yearTo
        ? `from ${String(state.yearFrom)}`
        : `from ${String(state.yearFrom)} to ${String(state.yearTo)}`
      : state.yearFrom !== null
        ? `from ${String(state.yearFrom)} or later`
        : state.yearTo !== null
          ? `from ${String(state.yearTo)} or earlier`
          : null;
  return [
    q !== "" ? `matching “${q}”` : null,
    years,
    state.status ? `with status ${STATUS_LABELS[state.status].toLowerCase()}` : null,
  ]
    .filter((p) => p !== null)
    .join(" ");
}

/** What the search form holds while the user edits it: inputs as typed. */
export interface CatalogDraft {
  q: string;
  sort: MovieSort;
  yearFrom: string;
  yearTo: string;
  status: MovieStatus | null;
}

/** The form for an applied state: what the URL says, ready to edit. */
export function draftFrom(state: CatalogState): CatalogDraft {
  return {
    q: state.q,
    sort: state.sort,
    yearFrom: state.yearFrom === null ? "" : String(state.yearFrom),
    yearTo: state.yearTo === null ? "" : String(state.yearTo),
    status: state.status,
  };
}

export type SubmitResult = { ok: true; state: CatalogState } | { ok: false; error: string };

/**
 * Turns a submitted draft into the state to apply, back on page 1, or says
 * why it cannot be searched. The same rules as the api, so a submitted
 * search never comes back as a 400.
 */
export function submitDraft(draft: CatalogDraft): SubmitResult {
  const years: (number | null)[] = [];
  for (const [label, raw] of [
    ["From", draft.yearFrom],
    ["To", draft.yearTo],
  ] as const) {
    if (raw.trim() === "") {
      years.push(null);
      continue;
    }
    const y = parseYear(raw);
    if (y === null) {
      return { ok: false, error: `${label} year must be a year between ${String(MIN_YEAR)} and ${String(MAX_YEAR)}.` };
    }
    years.push(y);
  }
  const [yearFrom = null, yearTo = null] = years;
  if (yearFrom !== null && yearTo !== null && yearFrom > yearTo) {
    return { ok: false, error: "The From year must not be after the To year." };
  }
  return {
    ok: true,
    state: { q: draft.q.slice(0, MAX_QUERY_LENGTH), sort: draft.sort, yearFrom, yearTo, status: draft.status, page: 1 },
  };
}
