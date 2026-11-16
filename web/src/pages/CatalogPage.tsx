import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useSearchParams } from "react-router";

import { listMovies } from "../api/client";
import { useUser } from "../auth/AuthProvider";
import { StatusBadge } from "../components/StatusBadge";
import { Button } from "../components/ui/Button";
import { MovieCard, MovieCardSkeleton } from "../components/ui/Card";
import { POSTER_SIZES } from "../lib/poster";
import { FilmIcon, SearchIcon } from "../components/ui/Icons";
import { EmptyState, Notice } from "../components/ui/Primitives";
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
import { once } from "../lib/mediaExpiry";

/** Browse: the whole catalog, searched, sorted and filtered (all in the URL). */
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
    <main className="mx-auto flex max-w-[1600px] flex-col gap-8 px-4 py-8 sm:px-6">
      <div className="flex flex-col gap-5">
        <div className="flex items-baseline gap-3">
          <h1 className="text-3xl font-bold">Browse</h1>
          {movies.data && <span className="text-sm text-fg-subtle">{describeCount(movies.data.total, state)}</span>}
        </div>
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

      {movies.isPending ? (
        <GridSkeleton />
      ) : movies.isError ? (
        <EmptyState
          title="The movies could not be loaded"
          action={
            isRetryable(movies.error) ? (
              <Button onClick={() => void movies.refetch()}>Try again</Button>
            ) : undefined
          }
        >
          {describeError(movies.error)}
        </EmptyState>
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
            className={`grid grid-cols-3 gap-x-3 gap-y-6 transition-opacity duration-200 sm:grid-cols-4 sm:gap-x-4 lg:grid-cols-5 xl:grid-cols-6 2xl:grid-cols-7 ${
              movies.isPlaceholderData ? "opacity-60" : ""
            }`}
          >
            {movies.data.items.map((m) => (
              <li key={m.id}>
                <MovieCard
                  movie={m}
                  // The detail page's back link returns to this exact list.
                  linkState={{ catalogSearch: params.toString() }}
                  corner={m.status !== "ready" ? <StatusBadge status={m.status} /> : undefined}
                  onPosterFailed={posterFailed}
                  sizes={POSTER_SIZES.browse}
                />
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

const fieldClass = "field h-10";

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
      <div className="flex flex-col gap-3 rounded-2xl border border-line bg-surface p-3 sm:flex-row sm:flex-wrap sm:items-center">
        <div className="relative w-full sm:w-72">
          <SearchIcon size={16} className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-subtle" />
          <input
          type="search"
          aria-label="Search titles"
          placeholder="Search titles"
          maxLength={MAX_QUERY_LENGTH}
          value={draft.q}
          onChange={(e) => {
            edit({ q: e.target.value });
          }}
          className={`${fieldClass} w-full pl-9`}
          />
        </div>
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
        <span className="flex items-center gap-2 text-sm text-fg-subtle">
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
        <div className="flex gap-2 sm:ml-auto">
          <Button type="submit" variant="primary" className="flex-1 sm:flex-none">
            Search
          </Button>
          <Button variant="ghost" onClick={onClear}>
            Clear
          </Button>
        </div>
      </div>
      {error && <Notice tone="danger">{error}</Notice>}
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
      className={`${fieldClass} w-full sm:w-28`}
    />
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
    <nav aria-label="Pages" className="flex items-center justify-between text-sm text-fg-subtle">
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
    <Button size="sm" disabled={disabled} onClick={onClick}>
      {children}
    </Button>
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
      <EmptyState title={`There are no movies on page ${String(state.page)}`} action={<Button onClick={onFirstPage}>Go to the first page</Button>} />
    );
  }
  if (hasFilters(state)) {
    return (
      <EmptyState
        icon={<SearchIcon size={28} />}
        title={`No movies ${describeFilters(state)}`}
        action={<Button onClick={onClear}>Clear search</Button>}
      />
    );
  }
  return (
    <EmptyState icon={<FilmIcon size={28} />} title="The catalog is empty">
      Add one with <code className="rounded bg-overlay px-1 text-fg">make dev-ingest INPUT=… TITLE=…</code>, or eight
      demo movies with <code className="rounded bg-overlay px-1 text-fg">make demo-seed</code>.
    </EmptyState>
  );
}

/** "8 movies", "3 matches". */
function describeCount(total: number, state: CatalogState): string {
  const noun = hasFilters(state) ? (total === 1 ? "match" : "matches") : total === 1 ? "movie" : "movies";
  return `${String(total)} ${noun}`;
}

function GridSkeleton() {
  return (
    <ul
      aria-label="Loading movies"
      className="grid grid-cols-3 gap-x-3 gap-y-6 sm:grid-cols-4 sm:gap-x-4 lg:grid-cols-5 xl:grid-cols-6 2xl:grid-cols-7"
    >
      {Array.from({ length: 12 }, (_, i) => (
        <li key={i}>
          <MovieCardSkeleton />
        </li>
      ))}
    </ul>
  );
}
