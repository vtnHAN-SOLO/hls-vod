import { useEffect } from "react";
import { useLocation } from "react-router";

import { MarkdownDoc } from "../components/MarkdownDoc";
import about from "../content/about.md";

/**
 * /about, readable signed in or not. The text is src/content/about.md,
 * parsed at build time. React Router does not scroll to a fragment, so a
 * link to /about#privacy (the footer's Privacy) lands on its heading here.
 */
export function AboutPage() {
  const { hash, key } = useLocation();
  useEffect(() => {
    if (hash.length > 1) {
      document.getElementById(decodeURIComponent(hash.slice(1)))?.scrollIntoView();
    } else {
      window.scrollTo(0, 0);
    }
  }, [hash, key]);
  return (
    <main className="mx-auto max-w-3xl px-4 py-10 sm:px-6 sm:py-14">
      <MarkdownDoc doc={about} />
    </main>
  );
}
