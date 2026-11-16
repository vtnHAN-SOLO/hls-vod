import { useSyncExternalStore } from "react";

function mediaQuery(query: string) {
  return {
    subscribe: (cb: () => void) => {
      const mq = window.matchMedia(query);
      mq.addEventListener("change", cb);
      return () => {
        mq.removeEventListener("change", cb);
      };
    },
    get: () => window.matchMedia(query).matches,
  };
}

const reduced = mediaQuery("(prefers-reduced-motion: reduce)");

/** Whether the viewer asked for less motion; follows the setting live. */
export function useReducedMotion(): boolean {
  return useSyncExternalStore(reduced.subscribe, reduced.get, () => false);
}

function subscribeScroll(cb: () => void) {
  window.addEventListener("scroll", cb, { passive: true });
  return () => {
    window.removeEventListener("scroll", cb);
  };
}

/** Whether the page is scrolled past `threshold` pixels (the header turns solid). */
export function useScrolled(threshold = 8): boolean {
  return useSyncExternalStore(
    subscribeScroll,
    () => window.scrollY > threshold,
    () => false,
  );
}
