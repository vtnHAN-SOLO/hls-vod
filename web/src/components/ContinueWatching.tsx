import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { continueWatching } from "../api/client";
import { formatRemaining, watchedFraction } from "../lib/format";
import { once } from "../lib/mediaExpiry";
import { MovieCard } from "./ui/Card";
import { ClockIcon } from "./ui/Icons";
import { Row, RowItem } from "./ui/Row";

/**
 * The caller's unfinished movies, most recent first, with how far they got.
 * Optional: while it loads, when it fails, or when there is nothing to
 * continue, it shows nothing and the page is unaffected. (No "remove from
 * row": the api has no way to forget a position.)
 */
export function ContinueWatching() {
  const { data, refetch } = useQuery({ queryKey: ["continue-watching"], queryFn: continueWatching });
  // An image that fails to load may carry an expired URL: refetch once.
  const [posterFailed] = useState(() => once(() => void refetch()));
  if (!data || data.items.length === 0) return null;
  return (
    <Row title="Continue watching" icon={<ClockIcon size={20} />}>
      {data.items.map(({ movie, position_sec: pos, runtime_sec: runtime }) => (
        <RowItem key={movie.id} shape="landscape">
          <MovieCard
            movie={movie}
            // The video frame, not the poster: where you stopped is a moment
            // of the film.
            shape="landscape"
            // No ?t=: the watch page resumes from the saved position itself.
            to={`/watch/${movie.id}`}
            progress={watchedFraction(pos, runtime)}
            subtitle={formatRemaining(pos, runtime)}
            onPosterFailed={posterFailed}
          />
        </RowItem>
      ))}
    </Row>
  );
}
