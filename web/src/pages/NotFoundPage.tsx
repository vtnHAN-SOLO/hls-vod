import { ButtonLink } from "../components/ui/Button";
import { FilmIcon } from "../components/ui/Icons";
import { EmptyState } from "../components/ui/Primitives";

export function NotFoundPage() {
  return (
    <main className="mx-auto max-w-3xl px-4 py-24 sm:px-6">
      <EmptyState icon={<FilmIcon size={28} />} title="There is no page here" action={<ButtonLink to="/browse">Browse movies</ButtonLink>} />
    </main>
  );
}
