import { Link, Outlet } from "react-router";

import { useAuth } from "../auth/AuthProvider";
import { useAuthConfig } from "../auth/useAuthConfig";

export function Layout() {
  const { state, logout } = useAuth();
  const config = useAuthConfig();
  return (
    <div className="flex min-h-full flex-col">
      <header className="border-b border-white/10">
        <nav className="mx-auto flex max-w-7xl items-center gap-6 px-4 py-3 sm:px-6">
          <Link to="/" className="text-lg font-semibold tracking-tight hover:text-white/80">
            HLS VOD
          </Link>
          {state.status === "authenticated" && (
            <span className="ml-auto flex items-center gap-3 text-sm">
              <span className="text-neutral-400">
                {state.user.username}
                {state.user.role === "admin" && <span className="ml-1 text-neutral-500">(admin)</span>}
              </span>
              {state.user.role === "admin" && (
                <Link to="/admin" className="rounded-md px-3 py-1.5 hover:bg-white/10">
                  Admin
                </Link>
              )}
              <Link to="/account" className="rounded-md px-3 py-1.5 hover:bg-white/10">
                Account
              </Link>
              <button
                type="button"
                onClick={() => void logout()}
                className="rounded-md bg-white/10 px-3 py-1.5 hover:bg-white/20"
              >
                Log out
              </button>
            </span>
          )}
          {state.status === "anonymous" && (
            <span className="ml-auto flex items-center gap-3 text-sm">
              <Link to="/login" className="rounded-md px-3 py-1.5 hover:bg-white/10">
                Log in
              </Link>
              {config.data?.registration_open && (
                <Link to="/register" className="rounded-md bg-white/10 px-3 py-1.5 hover:bg-white/20">
                  Register
                </Link>
              )}
            </span>
          )}
        </nav>
      </header>
      <div className="flex-1">
        <Outlet />
      </div>
    </div>
  );
}
