import { useEffect, useState } from "react";

import { tokenFromHash } from "../lib/registration";

/**
 * The token of a mailed link (#token=…), read once. The fragment never
 * reaches a server; it is removed from the address bar at once, so it is
 * not left in history or bookmarks. Call it before any effect that posts
 * the token: effects run in order, so the fragment is gone first.
 */
export function useLinkToken(): string | null {
  const [token] = useState(() => tokenFromHash(window.location.hash));
  useEffect(() => {
    if (window.location.hash) {
      window.history.replaceState(window.history.state, "", window.location.pathname + window.location.search);
    }
  }, []);
  return token;
}
