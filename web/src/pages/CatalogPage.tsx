import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";

import { listMovies, type Movie } from "../api/client";
import { useUser } from "../auth/AuthProvider";
import { ContinueWatching } from "../components/ContinueWatching";
import { Poster } from "../components/Poster";
import { StatusBadge } from "../components/StatusBadge";
import {
  type CatalogDraft,
  type CatalogState,
  catalogRequest,
  describeFilters,
  draftFrom,
  hasFilters,
  isMovieSort,
  isMovieStatus,
  MAX_QUERY_LENGTH,
  MAX_YEAR,
  MIN_YEAR,
  PAGE_SIZE,
  readCatalogParams,
  SORT_LABELS,
  STATUS_LABELS,
  submitDraft,
  writeCatalogParams,
} from "../lib/catalogParams";
import { describeError, isRetryable } from "../lib/errors";
import { formatTime } from "../lib/format";
import { once } from "../lib/mediaExpiry";

export function CatalogPage() {
  const [params, setParams] = useSearchParams();
  const isAdmin = useUser().role === "admin";
  const parsed = readCatalogParams(params);
  // ?status= is admin-only (the api answers 403); for anyone else a status
  // in the URL is ignored, not an error page.
  const state = isAdmin ? parsed : { ...parsed, status: null };

  // Only the APPLIED search (the URL) is ever fetched; the form below edits
  // a draft and touches the URL only on submit.
  const request = catalogRequest(state);
  const movies = useQuery({
    queryKey: ["movies", request],
    queryFn: () => listMovies(request),
    // Keep the last page on screen while the next one loads: no flash of
    // the loading skeleton on every search or page change.
    placeholderData: keepPreviousData,
  });
  // A poster that fails to load may carry an expired URL: refetch the page
  // once for fresh ones.
  const [posterFailed] = useState(() => once(() => void movies.refetch()));

  // A new history entry each time, so the back button returns to the
  // previous search.
  const apply = (next: CatalogState) => {
    setParams(writeCatalogParams(next));
  };
  const clearAll = () => {
    setParams(new URLSearchParams());
  };

  return (
    <main className="mx-auto flex max-w-7xl flex-col gap-6 px-4 py-6 sm:px-6">
      <div className="flex flex-col gap-3">
        <h1 className="text-2xl font-semibold">Movies</h1>
        {/* Keyed by the URL: back/forward or a link replaces the draft with
            what the URL says, without syncing state in an effect. */}
        <SearchForm
          key={params.toString()}
          applied={state}
          isAdmin={isAdmin}
          onSubmit={apply}
          onClear={clearAll}
        />
      </div>

      {/* Above the grid, on the unfiltered first page only. */}
      {!hasFilters(state) && state.page === 1 && <ContinueWatching />}

      {movies.isPending ? (
        <GridSkeleton />
      ) : movies.isError ? (
        <Panel>
          <p className="text-neutral-300">{describeError(movies.error)}</p>
          {isRetryable(movies.error) && <PanelButton onClick={() => void movies.refetch()}>Try again</PanelButton>}
        </Panel>
      ) : movies.data.items.length === 0 ? (
        <Empty
          total={movies.data.total}
          state={state}
          onClear={clearAll}
          onFirstPage={() => {
            apply({ ...state, page: 1 });
          }}
        />
      ) : (
        <>
          <ul
            aria-busy={movies.isFetching}
            className={`grid grid-cols-2 gap-x-4 gap-y-6 transition-opacity sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6 ${
              movies.isPlaceholderData ? "opacity-60" : ""
            }`}
          >
            {movies.data.items.map((m) => (
              <li key={m.id}>
                <MovieCard movie={m} search={params.toString()} onPosterFailed={posterFailed} />
              </li>
            ))}
          </ul>
          <Pager
            total={movies.data.total}
            offset={movies.data.offset}
            count={movies.data.items.length}
            onPage={(page) => {
              apply({ ...state, page });
            }}
          />
        </>
      )}
    </main>
  );
}

const fieldClass =
  "rounded-md border border-white/10 bg-neutral-900 px-3 py-2 text-sm outline-none placeholder:text-neutral-500 focus:border-white/40";

/**
 * The search box and every filter, as a draft: editing them fetches
 * nothing. Enter or the Search button applies the draft (a new search,
 * page 1); Clear resets both the draft and the applied search.
 */
function SearchForm({
  applied,
  isAdmin,
  onSubmit,
  onClear,
}: {
  applied: CatalogState;
  isAdmin: boolean;
  onSubmit: (state: CatalogState) => void;
  onClear: () => void;
}) {
  const [draft, setDraft] = useState<CatalogDraft>(() => draftFrom(applied));
  const [error, setError] = useState<string | null>(null);
  const edit = (next: Partial<CatalogDraft>) => {
    setDraft((d) => ({ ...d, ...next }));
    setError(null);
  };

  return (
    <form
      role="search"
      noValidate // submitDraft checks the years, with the api's rules and messages
      onSubmit={(e) => {
        e.preventDefault();
        const result = submitDraft(isAdmin ? draft : { ...draft, status: null });
        if (result.ok) onSubmit(result.state);
        else setError(result.error);
      }}
      className="flex flex-col gap-2"
    >
      <div className="flex flex-wrap items-center gap-3">
        <input
          type="search"
          aria-label="Search titles"
          placeholder="Search titles"
          maxLength={MAX_QUERY_LENGTH}
          value={draft.q}
          onChange={(e) => {
            edit({ q: e.target.value });
          }}
          className={`${fieldClass} w-full sm:w-72`}
        />
        <select
          aria-label="Sort"
          value={draft.sort}
          onChange={(e) => {
            if (isMovieSort(e.target.value)) edit({ sort: e.target.value });
          }}
          className={fieldClass}
        >
          {Object.entries(SORT_LABELS).map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>
        <span className="flex items-center gap-2 text-sm text-neutral-400">
          <YearInput label="From year" value={draft.yearFrom} onChange={(v) => { edit({ yearFrom: v }); }} />
          <span aria-hidden="true">–</span>
          <YearInput label="To year" value={draft.yearTo} onChange={(v) => { edit({ yearTo: v }); }} />
        </span>
        {isAdmin && (
          <select
            aria-label="Filter by status"
            value={draft.status ?? ""}
            onChange={(e) => {
              edit({ status: isMovieStatus(e.target.value) ? e.target.value : null });
            }}
            className={fieldClass}
          >
            <option value="">All statuses</option>
            {Object.entries(STATUS_LABELS).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        )}
        <button type="submit" className="rounded-md bg-white px-4 py-2 text-sm font-medium text-neutral-950 hover:bg-white/90">
          Search
        </button>
        <button
          type="button"
          onClick={onClear}
          className="rounded-md bg-white/10 px-3 py-2 text-sm text-neutral-100 hover:bg-white/20"
        >
          Clear
        </button>
      </div>
      {error && (
        <p role="alert" className="text-sm text-red-300">
          {error}
        </p>
      )}
    </form>
  );
}

function YearInput({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <input
      type="text"
      inputMode="numeric"
      aria-label={label}
      placeholder={label}
      title={`${String(MIN_YEAR)}–${String(MAX_YEAR)}`}
      maxLength={4}
      value={value}
      onChange={(e) => {
        onChange(e.target.value);
      }}
      className={`${fieldClass} w-28`}
    />
  );
}

function MovieCard({ movie, search, onPosterFailed }: { movie: Movie; search: string; onPosterFailed: () => void }) {
  return (
    <Link
      to={`/movies/${movie.id}`}
      // The detail page's back link returns to this exact list.
      state={{ catalogSearch: search }}
      className="group flex flex-col gap-2 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-white/70"
    >
      <div className="overflow-hidden rounded-md">
        <Poster
          key={movie.poster_url}
          url={movie.poster_url}
          title={movie.title}
          className="transition-transform group-hover:scale-105"
          onFailed={onPosterFailed}
        />
      </div>
      <div className="flex flex-col gap-1">
        <span className="line-clamp-2 text-sm font-medium group-hover:text-white/80">{movie.title}</span>
        <span className="flex items-center gap-2 text-xs text-neutral-400">
          {[movie.year, movie.runtime_sec ? formatTime(movie.runtime_sec) : null].filter((v) => v !== null).join(" · ")}
          {movie.status !== "ready" && <StatusBadge status={movie.status} />}
        </span>
      </div>
    </Link>
  );
}

function Pager({
  total,
  offset,
  count,
  onPage,
}: {
  total: number;
  offset: number;
  count: number;
  onPage: (page: number) => void;
}) {
  const page = Math.floor(offset / PAGE_SIZE) + 1;
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  return (
    <nav aria-label="Pages" className="flex items-center justify-between text-sm text-neutral-400">
      <span>
        {offset + 1}–{offset + count} of {total}
      </span>
      {pages > 1 && (
        <span className="flex items-center gap-2">
          <PageButton
            disabled={page <= 1}
            onClick={() => {
              onPage(page - 1);
            }}
          >
            Previous
          </PageButton>
          <span>
            Page {page} of {pages}
          </span>
          <PageButton
            disabled={page >= pages}
            onClick={() => {
              onPage(page + 1);
            }}
          >
            Next
          </PageButton>
        </span>
      )}
    </nav>
  );
}

function PageButton({ disabled, onClick, children }: { disabled: boolean; onClick: () => void; children: string }) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className="rounded-md bg-white/10 px-3 py-1.5 text-neutral-100 hover:bg-white/20 disabled:cursor-not-allowed disabled:opacity-40"
    >
      {children}
    </button>
  );
}

function Empty({
  total,
  state,
  onClear,
  onFirstPage,
}: {
  total: number;
  state: CatalogState;
  onClear: () => void;
  onFirstPage: () => void;
}) {
  // Past the last page (an old link, or movies were removed).
  if (total > 0) {
    return (
      <Panel>
        <p className="text-neutral-300">There are no movies on page {state.page}.</p>
        <PanelButton onClick={onFirstPage}>Go to the first page</PanelButton>
      </Panel>
    );
  }
  if (hasFilters(state)) {
    return (
      <Panel>
        <p className="text-neutral-300">No movies {describeFilters(state)}.</p>
        <PanelButton onClick={onClear}>Clear search</PanelButton>
      </Panel>
    );
  }
  return (
    <Panel>
      <p className="text-neutral-300">The catalog is empty.</p>
      <p className="text-sm text-neutral-500">
        Add one with <code className="rounded bg-white/10 px-1">make dev-ingest INPUT=… TITLE=…</code>
      </p>
    </Panel>
  );
}

function Panel({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-md border border-white/10 px-6 py-16 text-center">
      {children}
    </div>
  );
}

function PanelButton({ onClick, children }: { onClick: () => void; children: string }) {
  return (
    <button type="button" onClick={onClick} className="rounded-md bg-white/10 px-3 py-1.5 text-sm hover:bg-white/20">
      {children}
    </button>
  );
}

function GridSkeleton() {
  return (
    <ul aria-label="Loading movies" className="grid grid-cols-2 gap-x-4 gap-y-6 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6">
      {Array.from({ length: 12 }, (_, i) => (
        <li key={i} className="flex animate-pulse flex-col gap-2">
          <div className="aspect-video rounded-md bg-neutral-800" />
          <div className="h-3 w-3/4 rounded bg-neutral-800" />
          <div className="h-3 w-1/3 rounded bg-neutral-800" />
        </li>
      ))}
    </ul>
  );
}
