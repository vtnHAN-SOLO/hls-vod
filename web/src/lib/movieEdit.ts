// The admin's Edit dialog: the draft as typed, checked with the api's rules
// (PATCH /api/v1/admin/movies/{id}), and turned into a body that carries only
// the fields that changed. The api checks again; this is for quick feedback.
import type { AdminMovie, MovieEditRequest } from "../api/client";
import { MAX_YEAR, MIN_YEAR } from "./catalogParams";

// Must match MovieEditRequest in docs/openapi.yaml.
export const MAX_TITLE = 200;
export const MAX_OVERVIEW = 2000;

export interface MovieEditDraft {
  title: string;
  year: string; // as typed; "" = no year
  overview: string; // "" = none
}

export function draftOf(m: Pick<AdminMovie, "title" | "year" | "overview">): MovieEditDraft {
  return { title: m.title, year: m.year === null ? "" : String(m.year), overview: m.overview ?? "" };
}

export type EditResult =
  | { ok: true; body: MovieEditRequest | null } // null: nothing changed
  | { ok: false; field: keyof MovieEditDraft; error: string };

// Characters, as the api counts them (code points), not UTF-16 units.
const length = (s: string) => Array.from(s).length;
// eslint-disable-next-line no-control-regex -- control characters are exactly what is refused
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;
// eslint-disable-next-line no-control-regex -- the overview may hold line breaks and tabs, nothing else
const CONTROL_MULTILINE = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/;

export function checkEdit(draft: MovieEditDraft, original: Pick<AdminMovie, "title" | "year" | "overview">): EditResult {
  const title = draft.title.trim();
  if (title === "") return { ok: false, field: "title", error: "A title is required." };
  if (length(title) > MAX_TITLE) return { ok: false, field: "title", error: `At most ${String(MAX_TITLE)} characters.` };
  if (CONTROL.test(title)) return { ok: false, field: "title", error: "No control characters." };

  const yearText = draft.year.trim();
  let year: number | null = null;
  if (yearText !== "") {
    if (!/^\d{4}$/.test(yearText)) return { ok: false, field: "year", error: "A four-digit year, or empty." };
    year = Number(yearText);
    if (year < MIN_YEAR || year > MAX_YEAR) {
      return { ok: false, field: "year", error: `Between ${String(MIN_YEAR)} and ${String(MAX_YEAR)}, or empty.` };
    }
  }

  const overview = draft.overview.trim();
  if (length(overview) > MAX_OVERVIEW) {
    return { ok: false, field: "overview", error: `At most ${String(MAX_OVERVIEW)} characters.` };
  }
  if (CONTROL_MULTILINE.test(overview)) return { ok: false, field: "overview", error: "No control characters." };

  const body: MovieEditRequest = {};
  if (title !== original.title) body.title = title;
  if (year !== original.year) body.year = year;
  const ov = overview === "" ? null : overview;
  if (ov !== (original.overview ?? null)) body.overview = ov;
  return { ok: true, body: Object.keys(body).length === 0 ? null : body };
}
