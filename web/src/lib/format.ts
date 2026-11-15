/** 75 -> "1:15", 3725 -> "1:02:05". */
export function formatTime(t: number): string {
  if (!Number.isFinite(t) || t < 0) return "0:00";
  const s = Math.floor(t % 60);
  const m = Math.floor((t / 60) % 60);
  const h = Math.floor(t / 3600);
  const ss = String(s).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

/** 4_000_000_000 -> "3.7 GB". */
export function formatBytes(n: number | null): string {
  if (n === null || !Number.isFinite(n) || n < 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i] ?? "B"}`;
}

/** 1939 -> "1.9 Mbit/s", 837 -> "837 kbit/s". */
export function formatBitrate(kbps: number | null): string {
  if (kbps === null || !Number.isFinite(kbps) || kbps < 0) return "—";
  return kbps >= 1000 ? `${(kbps / 1000).toFixed(1)} Mbit/s` : `${Math.round(kbps)} kbit/s`;
}

/** Share of the movie watched, 0..1; 0 when the runtime is unknown. */
export function watchedFraction(positionSec: number, runtimeSec: number | null): number {
  if (runtimeSec === null || !(runtimeSec > 0) || !Number.isFinite(positionSec)) return 0;
  return Math.min(Math.max(positionSec / runtimeSec, 0), 1);
}

/** 600 of 5400 -> "80 min left"; under a minute -> "1 min left"; unknown runtime -> "at 10:00". */
export function formatRemaining(positionSec: number, runtimeSec: number | null): string {
  if (runtimeSec === null || !(runtimeSec > 0)) return `at ${formatTime(positionSec)}`;
  const min = Math.max(1, Math.ceil((runtimeSec - positionSec) / 60));
  return `${min} min left`;
}
