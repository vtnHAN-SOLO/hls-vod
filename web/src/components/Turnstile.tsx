import { useEffect, useRef, useState } from "react";

import { loadTurnstile } from "../lib/turnstile";

/**
 * One Turnstile challenge. onToken gets the token once solved, and null
 * when it expires or fails. A token is good for one request: after a
 * submit, remount the widget (change its `key`) to get a new one.
 */
export function Turnstile({ siteKey, onToken }: { siteKey: string; onToken: (token: string | null) => void }) {
  const box = useRef<HTMLDivElement>(null);
  const report = useRef(onToken);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    report.current = onToken;
  });

  useEffect(() => {
    let widget: string | null = null;
    let cancelled = false;
    loadTurnstile()
      .then((ts) => {
        if (cancelled || !box.current) return;
        widget = ts.render(box.current, {
          sitekey: siteKey,
          theme: "dark",
          callback: (token) => {
            report.current(token);
          },
          "expired-callback": () => {
            report.current(null);
          },
          "error-callback": () => {
            report.current(null);
          },
        });
      })
      .catch(() => {
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
      if (widget !== null) window.turnstile?.remove(widget);
    };
  }, [siteKey]);

  return (
    <div className="flex min-h-[65px] flex-col gap-1">
      <div ref={box} />
      {failed && (
        <p role="alert" className="text-sm text-danger">
          The check could not load. Disable blockers for challenges.cloudflare.com, or reload the page.
        </p>
      )}
    </div>
  );
}
