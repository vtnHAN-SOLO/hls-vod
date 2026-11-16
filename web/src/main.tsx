import "./index.css";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ReactElement, StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router";

import { ApiError } from "./api/client";
import { AuthProvider, RequireAuth } from "./auth/AuthProvider";
import { Layout } from "./components/Layout";
import { PUBLIC_PATHS, type PublicPath } from "./lib/nav";
import { AboutPage } from "./pages/AboutPage";
import { AccountPage } from "./pages/AccountPage";
import { AdminPage } from "./pages/AdminPage";
import { CatalogPage } from "./pages/CatalogPage";
import { ForgotPasswordPage } from "./pages/ForgotPasswordPage";
import { GoogleDonePage } from "./pages/GoogleDonePage";
import { HomePage } from "./pages/HomePage";
import { LoginPage } from "./pages/LoginPage";
import { MovieDetailPage } from "./pages/MovieDetailPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { RegisterPage } from "./pages/RegisterPage";
import { ResetPasswordPage } from "./pages/ResetPasswordPage";
import { VerifyEmailPage } from "./pages/VerifyEmailPage";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 4xx answers (unknown id, not ready, bad filter) will not change on retry.
      retry: (count, err) => count < 2 && !(err instanceof ApiError && err.status >= 400),
      refetchOnWindowFocus: false,
    },
  },
});

// Readable without a session (src/lib/nav.ts lists them, so the list is
// tested): the type makes every public path have exactly one page here.
const PUBLIC_PAGES: Record<PublicPath, ReactElement> = {
  "/login": <LoginPage />,
  "/register": <RegisterPage />,
  "/verify-email": <VerifyEmailPage />,
  "/forgot-password": <ForgotPasswordPage />,
  "/reset-password": <ResetPasswordPage />,
  // The api's Google callback lands here; the start-up refresh logs in.
  "/auth/google/done": <GoogleDonePage />,
  "/about": <AboutPage />,
};

const router = createBrowserRouter([
  {
    element: <Layout />,
    children: [
      // Public: everything else needs a session.
      ...PUBLIC_PATHS.map((path) => ({ path, element: PUBLIC_PAGES[path] })),
      {
        element: <RequireAuth />,
        children: [
          { index: true, element: <HomePage /> },
          { path: "/browse", element: <CatalogPage /> },
          { path: "/movies/:id", element: <MovieDetailPage /> },
          { path: "/account", element: <AccountPage /> },
          // Non-admins get the 403 page; the api checks the role on every call.
          { path: "/admin", element: <AdminPage /> },
          // Lazy: the player chunk carries hls.js, which only /watch needs.
          {
            path: "/watch/:id",
            lazy: async () => {
              const { WatchPage } = await import("./pages/WatchPage");
              return { Component: WatchPage };
            },
          },
          { path: "*", element: <NotFoundPage /> },
        ],
      },
    ],
  },
]);

const root = document.getElementById("root");
if (!root) throw new Error("#root missing from index.html");

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <RouterProvider router={router} />
      </AuthProvider>
    </QueryClientProvider>
  </StrictMode>,
);
