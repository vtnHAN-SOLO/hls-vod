// The site's navigation as data: the header's items, the avatar menu per
// role, the footer's links and which header item is active. Pure, so who
// sees what is unit tested; Layout.tsx renders it.

export interface NavItem {
  label: string;
  to: string;
}

/** The header, left of the search: the logo links home too, but many people do not know it. */
export const MAIN_NAV: readonly NavItem[] = [
  { label: "Home", to: "/" },
  { label: "Browse", to: "/browse" },
];

export type AccountItem = (NavItem & { key: "account" | "admin" }) | { key: "signout"; label: string };

/** The avatar menu. Admin is here only, and only for an admin; the api checks the role anyway. */
export function accountMenu(role: "user" | "admin"): AccountItem[] {
  return [
    { key: "account", label: "Account", to: "/account" },
    ...(role === "admin" ? [{ key: "admin", label: "Admin", to: "/admin" } as const] : []),
    { key: "signout", label: "Sign out" },
  ];
}

/** The footer, for everyone, signed in or not. */
export const FOOTER_NAV: readonly NavItem[] = [
  { label: "About", to: "/about" },
  { label: "Privacy", to: "/about#privacy" },
];

/**
 * The header item that shows where the visitor is: Home on "/" only,
 * Browse on /browse. Other pages (a movie, the account) highlight nothing.
 */
export function activeNav(pathname: string): string | null {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
  const hit = MAIN_NAV.find((item) => item.to === path);
  return hit ? hit.to : null;
}

/** Routes readable without a session; everything else goes through RequireAuth. */
export const PUBLIC_PATHS = [
  "/login",
  "/register",
  "/verify-email",
  "/forgot-password",
  "/reset-password",
  "/auth/google/done",
  "/about",
] as const;

export type PublicPath = (typeof PUBLIC_PATHS)[number];
