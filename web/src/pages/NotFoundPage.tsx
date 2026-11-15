import { Link } from "react-router";

export function NotFoundPage() {
  return (
    <main className="mx-auto flex max-w-7xl flex-col items-center gap-3 px-6 py-24 text-center">
      <p className="text-neutral-300">There is no page here.</p>
      <Link to="/" className="rounded-md bg-white/10 px-3 py-1.5 text-sm hover:bg-white/20">
        Browse movies
      </Link>
    </main>
  );
}
