import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";

import { listMovies } from "../api/client";
import { ContinueWatching } from "../components/ContinueWatching";
import { Popular } from "../components/Popular";
import { Spotlight } from "../components/Spotlight";
import { ButtonLink } from "../components/ui/Button";
import { MovieCard, MovieCardSkeleton } from "../components/ui/Card";
import { FilmIcon, SparkIcon } from "../components/ui/Icons";
import { Row, RowItem } from "../components/ui/Row";
import { once } from "../lib/mediaExpiry";
import { POSTER_SIZES } from "../lib/poster";

/** How many movies the home grid shows before "Browse all". */
const GRID_SIZE = 18;

export function HomePage() {
  return (
    <main>
      <h1 className="sr-only">Home</h1>
      <Spotlight />
      <div className="relative mx-auto grid max-w-[1600px] grid-cols-1 gap-10 px-4 pt-6 sm:px-6 lg:grid-cols-[minmax(0,1fr)_20rem] lg:gap-x-10 xl:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="flex min-w-0 flex-col gap-10 lg:col-start-1">
          <ContinueWatching />
        </div>
        <Popular className="lg:sticky lg:top-24 lg:col-start-2 lg:row-span-3 lg:row-start-1 lg:self-start" />
        <div className="flex min-w-0 flex-col gap-12 lg:col-start-1">
          <RecentlyAdded />
          <AllMovies />
        </div>
      </div>
    </main>
  );
}

function RecentlyAdded() {
  const movies = useQuery({
    queryKey: ["movies", { sort: "added_desc", limit: 12 }],
    queryFn: () => listMovies({ sort: "added_desc", limit: 12 }),
  });
  const [posterFailed] = useState(() => once(() => void movies.refetch()));
  if (movies.isError || movies.data?.items.length === 0) return null;
  return (
    <Row
      title="Recently added"
      icon={<SparkIcon size={20} />}
      action={
        <Link to="/browse" className="text-accent-soft hover:underline">
          See all
        </Link>
      }
    >
      {movies.isPending
        ? Array.from({ length: 7 }, (_, i) => (
            <RowItem key={i}>
              <MovieCardSkeleton />
            </RowItem>
          ))
        : movies.data.items.map((m) => (
            <RowItem key={m.id}>
              <MovieCard movie={m} onPosterFailed={posterFailed} />
            </RowItem>
          ))}
    </Row>
  );
}

function AllMovies() {
  const movies = useQuery({
    queryKey: ["movies", { sort: "title_asc", limit: GRID_SIZE }],
    queryFn: () => listMovies({ sort: "title_asc", limit: GRID_SIZE }),
  });
  const [posterFailed] = useState(() => once(() => void movies.refetch()));
  if (movies.isError || movies.data?.items.length === 0) return null;
  return (
    <section aria-labelledby="all-movies" className="flex flex-col gap-4">
      <div className="flex items-center gap-3">
        <h2 id="all-movies" className="flex items-center gap-2 text-lg font-semibold sm:text-xl">
          <span className="text-accent-soft">
            <FilmIcon size={20} />
          </span>
          All movies
        </h2>
        {movies.data && (
          <span className="text-sm text-fg-subtle">{movies.data.total}</span>
        )}
      </div>
      <ul className="grid grid-cols-3 gap-x-3 gap-y-6 sm:grid-cols-4 sm:gap-x-4 xl:grid-cols-5 2xl:grid-cols-6">
        {movies.isPending
          ? Array.from({ length: 8 }, (_, i) => (
              <li key={i}>
                <MovieCardSkeleton />
              </li>
            ))
          : movies.data.items.map((m) => (
              <li key={m.id}>
                <MovieCard movie={m} onPosterFailed={posterFailed} sizes={POSTER_SIZES.grid} />
              </li>
            ))}
      </ul>
      {movies.data && movies.data.total > movies.data.items.length && (
        <ButtonLink to="/browse?sort=title_asc" variant="secondary" className="self-center">
          Browse all {movies.data.total} movies
        </ButtonLink>
      )}
    </section>
  );
}
