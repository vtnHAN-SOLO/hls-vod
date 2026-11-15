import { useQuery } from "@tanstack/react-query";

import { getAuthConfig } from "../api/client";

/** Whether registration is open and the Turnstile site key; fetched once. */
export function useAuthConfig() {
  return useQuery({ queryKey: ["auth-config"], queryFn: getAuthConfig, staleTime: Infinity });
}
