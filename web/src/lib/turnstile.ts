// Cloudflare Turnstile, loaded only by the pages that show a challenge
// (register, resend, and login after 3 failures). The script comes from
// Cloudflare; the token it produces means nothing until the api checks it
// with siteverify.

const SCRIPT_URL = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";

export interface TurnstileOptions {
  sitekey: string;
  callback: (token: string) => void;
  "expired-callback"?: () => void;
  "error-callback"?: () => void;
  theme?: "light" | "dark" | "auto";
}

export interface TurnstileApi {
  render(container: HTMLElement, options: TurnstileOptions): string;
  remove(widgetId: string): void;
}

declare global {
  interface Window {
    turnstile?: TurnstileApi;
  }
}

let loading: Promise<TurnstileApi> | null = null;

/** Adds the script once; later calls share it. A failed load can be retried. */
export function loadTurnstile(): Promise<TurnstileApi> {
  if (window.turnstile) return Promise.resolve(window.turnstile);
  loading ??= new Promise<TurnstileApi>((resolve, reject) => {
    const script = document.createElement("script");
    script.src = SCRIPT_URL;
    script.async = true;
    script.onload = () => {
      if (window.turnstile) resolve(window.turnstile);
      else reject(new Error("Turnstile did not load"));
    };
    script.onerror = () => {
      script.remove();
      reject(new Error("Turnstile did not load"));
    };
    document.head.append(script);
  }).catch((e: unknown) => {
    loading = null;
    throw e;
  });
  return loading;
}
