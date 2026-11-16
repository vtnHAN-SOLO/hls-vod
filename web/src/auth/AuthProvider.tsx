import { useQueryClient } from "@tanstack/react-query";
import { createContext, use, useEffect, useSyncExternalStore } from "react";
import { Navigate, Outlet, useLocation } from "react-router";

import { login as apiLogin, session, verifyEmail as apiVerifyEmail } from "../api/client";
import type { SessionState, SessionUser } from "../lib/session";

interface Auth {
  state: SessionState;
  login: (username: string, password: string, turnstileToken?: string) => Promise<void>;
  /** Confirms an address with a mailed link's token, and logs in as that account. */
  verifyEmail: (token: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<Auth | null>(null);

/**
 * Holds the session for the React tree. The access token itself stays inside
 * `session` (memory only); components see who is logged in, never the token.
 * On start it asks the api once, through the HttpOnly refresh cookie.
 */
export function AuthProvider({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient();
  const state = useSyncExternalStore(session.subscribe, session.getState);

  useEffect(() => {
    void session.start();
  }, []);

  // Logged out (button, revoked session, a second 401): drop every cached
  // answer, so the next user never sees the previous user's data.
  useEffect(() => {
    if (state.status === "anonymous") queryClient.clear();
  }, [state.status, queryClient]);

  const value: Auth = {
    state,
    login: async (username, password, turnstileToken) => {
      queryClient.clear();
      await apiLogin(username, password, turnstileToken);
    },
    verifyEmail: async (token) => {
      queryClient.clear();
      await apiVerifyEmail(token);
    },
    logout: () => session.logout(),
  };
  return <AuthContext value={value}>{children}</AuthContext>;
}

export function useAuth(): Auth {
  const a = use(AuthContext);
  if (!a) throw new Error("useAuth outside <AuthProvider>");
  return a;
}

/** The logged-in user; only valid under <RequireAuth>. */
export function useUser(): SessionUser {
  const { state } = useAuth();
  if (state.status !== "authenticated") throw new Error("useUser outside <RequireAuth>");
  return state.user;
}

/** Routes under it need a session; without one they go to /login and come back after. */
export function RequireAuth() {
  const { state } = useAuth();
  const location = useLocation();
  if (state.status === "unknown") {
    return <div className="mx-auto h-1 max-w-7xl animate-pulse bg-overlay" aria-label="Checking your session" />;
  }
  if (state.status === "anonymous") {
    return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />;
  }
  return <Outlet />;
}
