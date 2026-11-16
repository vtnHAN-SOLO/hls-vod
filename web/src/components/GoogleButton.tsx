import { GOOGLE_START_URL } from "../api/client";

/** "Continue with Google": a plain link; the api redirects to Google and back. */
export function GoogleButton() {
  return (
    <a
      href={GOOGLE_START_URL}
      className="flex items-center justify-center gap-2 rounded-md border border-line px-3 py-2 text-sm font-medium hover:bg-overlay"
    >
      <span aria-hidden="true" className="font-bold">
        G
      </span>
      Continue with Google
    </a>
  );
}
