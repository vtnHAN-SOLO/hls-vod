import { Link, Outlet, useLocation, useNavigate, useSearchParams } from "react-router";

import { useAuth } from "../auth/AuthProvider";
import { useAuthConfig } from "../auth/useAuthConfig";
import { EMPTY_STATE, MAX_QUERY_LENGTH, writeCatalogParams } from "../lib/catalogParams";
import { accountMenu, activeNav, FOOTER_NAV, MAIN_NAV } from "../lib/nav";
import { ButtonLink } from "./ui/Button";
import { useScrolled } from "./ui/hooks";
import { LogOutIcon, SearchIcon, ShieldIcon, UserIcon } from "./ui/Icons";
import { Menu, type MenuItem } from "./ui/Menu";
import { Wordmark } from "./ui/Wordmark";

/** Pages whose first section runs under the transparent header (the spotlight). */
const UNDER_HEADER = new Set(["/"]);

export function Layout() {
  const { state } = useAuth();
  const location = useLocation();
  const scrolled = useScrolled();
  const solid = scrolled || !UNDER_HEADER.has(location.pathname) || state.status !== "authenticated";

  return (
    <div className="flex min-h-full flex-col">
      <header
        className={`sticky top-0 z-40 transition-colors duration-200 ${
          solid ? "border-b border-line bg-bg/80 backdrop-blur-xl" : "border-b border-transparent bg-gradient-to-b from-scrim/60 to-scrim/0"
        }`}
      >
        <nav aria-label="Main" className="mx-auto flex h-16 max-w-[1600px] items-center gap-2 px-4 sm:gap-4 sm:px-6">
          <Link to="/" className="mr-1 shrink-0 rounded-lg text-fg sm:mr-3">
            {/* Wrapped: display is set here, not on the Wordmark (its own inline-flex would win). */}
            <span className="hidden sm:block">
              <Wordmark className="text-lg" />
            </span>
            <span className="block sm:hidden">
              <Wordmark compact />
            </span>
          </Link>
          {state.status === "authenticated" && <Signed admin={state.user.role === "admin"} username={state.user.username} />}
          {state.status === "anonymous" && <Anonymous />}
        </nav>
      </header>
      <div className="flex-1">
        <Outlet />
      </div>
      <Footer />
    </div>
  );
}

function navClass(active: boolean) {
  return `rounded-lg px-2.5 py-1.5 text-sm font-medium transition-colors duration-150 sm:px-3 ${
    active ? "bg-overlay/70 text-fg" : "text-fg-muted hover:text-fg"
  }`;
}

const MENU_ICONS = {
  account: <UserIcon size={18} />,
  admin: <ShieldIcon size={18} />,
  signout: <LogOutIcon size={18} />,
};

function Signed({ admin, username }: { admin: boolean; username: string }) {
  const { logout } = useAuth();
  const location = useLocation();
  const onBrowse = location.pathname === "/browse";
  const active = activeNav(location.pathname);
  // Admin lives in this menu only (src/lib/nav.ts); the api checks the role anyway.
  const items: MenuItem[] = accountMenu(admin ? "admin" : "user").map((item) =>
    item.key === "signout"
      ? { kind: "action", label: item.label, onSelect: () => void logout(), icon: MENU_ICONS.signout }
      : { kind: "link", label: item.label, to: item.to, icon: MENU_ICONS[item.key] },
  );
  return (
    <>
      {MAIN_NAV.map((item) => (
        <Link key={item.to} to={item.to} aria-current={active === item.to ? "page" : undefined} className={navClass(active === item.to)}>
          {item.label}
        </Link>
      ))}
      <div className="ml-auto flex items-center gap-2 sm:gap-3">
        {/* The Browse page has its own search toolbar. */}
        {!onBrowse && (
          <>
            <div className="hidden sm:block">
              <HeaderSearch />
            </div>
            {/* A phone has no room for the box: the Browse toolbar searches. */}
            <Link
              to="/browse"
              aria-label="Search"
              className="grid size-9 place-items-center rounded-full text-fg-muted transition-colors duration-150 hover:bg-overlay hover:text-fg sm:hidden"
            >
              <SearchIcon size={20} />
            </Link>
          </>
        )}
        <Menu
          label="Account menu"
          header={
            <span className="flex flex-col">
              <span className="text-xs text-fg-subtle">Signed in as</span>
              <span className="truncate text-sm font-semibold text-fg">{username}</span>
            </span>
          }
          trigger={
            <span
              aria-hidden="true"
              className="grid size-9 place-items-center rounded-full bg-gradient-to-br from-accent-soft to-accent font-display text-sm font-bold text-on-accent ring-2 ring-line transition-shadow duration-150 hover:ring-accent-soft"
            >
              {username.charAt(0).toUpperCase()}
            </span>
          }
          items={items}
        />
      </div>
    </>
  );
}

/**
 * Search from any page: submitting goes to Browse with ?q= (search on
 * submit, as the Browse toolbar). Keyed by the URL, so it shows what the
 * address says after back and forward.
 */
function HeaderSearch() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const location = useLocation();
  return (
    <form
      role="search"
      key={location.key}
      onSubmit={(e) => {
        e.preventDefault();
        const v = new FormData(e.currentTarget).get("q");
        const q = typeof v === "string" ? v.trim() : "";
        void navigate(`/browse?${writeCatalogParams({ ...EMPTY_STATE, q }).toString()}`);
      }}
      className="relative"
    >
      <SearchIcon size={16} className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-subtle" />
      <input
        type="search"
        name="q"
        aria-label="Search titles"
        placeholder="Search titles"
        maxLength={MAX_QUERY_LENGTH}
        defaultValue={params.get("q") ?? ""}
        className="field h-9 w-56 rounded-full bg-surface/70 pl-9 transition-[width] duration-200 focus:w-72"
      />
    </form>
  );
}

function Anonymous() {
  const config = useAuthConfig();
  return (
    <span className="ml-auto flex items-center gap-2">
      <ButtonLink to="/login" variant="ghost" size="sm">
        Log in
      </ButtonLink>
      {config.data?.registration_open && (
        <ButtonLink to="/register" variant="primary" size="sm">
          Register
        </ButtonLink>
      )}
    </span>
  );
}

function Footer() {
  return (
    <footer className="mt-16 border-t border-line">
      <div className="mx-auto flex max-w-[1600px] flex-col gap-2 px-4 py-8 text-xs text-fg-subtle sm:flex-row sm:items-center sm:justify-between sm:px-6">
        <Wordmark size={24} className="text-sm" />
        <span>Self-hosted video on demand. Every movie is transcoded once and served as static HLS.</span>
        <nav aria-label="Footer" className="flex items-center gap-1">
          {FOOTER_NAV.map((item, i) => (
            <span key={item.to} className="flex items-center gap-1">
              {i > 0 && <span aria-hidden="true">·</span>}
              <Link to={item.to} className="rounded px-1 py-0.5 text-fg-muted transition-colors duration-150 hover:text-fg">
                {item.label}
              </Link>
            </span>
          ))}
        </nav>
      </div>
    </footer>
  );
}
