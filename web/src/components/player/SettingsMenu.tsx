import { type ReactNode, useEffect, useRef } from "react";

import { SPEEDS, SUBTITLE_SIZES, type SubtitleSize } from "../../lib/playerPrefs";
import { type MenuEvent, type MenuState, qualityLabel, SIZE_LABELS, speedLabel } from "../../lib/settingsMenu";
import { currentSubtitleName, type LanguageGroup } from "../../lib/subtitleMenu";
import { ArrowLeftIcon, CheckIcon, ChevronRightIcon } from "../ui/Icons";

export interface QualityLevel {
  index: number; // hls.js level index
  height: number;
  bitrate: number; // bits/s
}

interface Props {
  state: Extract<MenuState, { open: true }>;
  dispatch: (e: MenuEvent) => void;
  levels: readonly QualityLevel[];
  selectedLevel: number; // -1 = automatic
  playingHeight: number | null;
  onLevel: (index: number) => void;
  groups: readonly LanguageGroup[];
  subtitle: number | null;
  onSubtitle: (index: number | null) => void;
  size: SubtitleSize;
  onSize: (s: SubtitleSize) => void;
  speed: number;
  onSpeed: (s: number) => void;
}

/**
 * The gear's panel: a main page with one row per setting, each opening a
 * sub-page with a back arrow. Scrolls when long (a movie may have dozens of
 * subtitle tracks). Focus moves to the first item of each page; Escape steps
 * back (lib/settingsMenu.ts, handled by the player).
 */
export function SettingsMenu(p: Props) {
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    panel.current?.querySelector<HTMLElement>("button")?.focus();
  }, [p.state.page]);

  const levels = [...p.levels].sort((a, b) => b.height - a.height);
  const selectedHeight = p.levels.find((l) => l.index === p.selectedLevel)?.height ?? null;
  const subtitleCount = p.groups.reduce((n, g) => n + g.tracks.length, 0);

  let body: ReactNode;
  switch (p.state.page) {
    case "main":
      body = (
        <ul>
          {p.levels.length > 1 && (
            <Row label="Quality" value={qualityLabel(selectedHeight, p.playingHeight)} onClick={() => { p.dispatch({ kind: "go", page: "quality" }); }} />
          )}
          {subtitleCount > 0 && (
            <Row label="Subtitles" value={currentSubtitleName(p.groups, p.subtitle)} onClick={() => { p.dispatch({ kind: "go", page: "subtitles" }); }} />
          )}
          {subtitleCount > 0 && (
            <Row label="Subtitle size" value={SIZE_LABELS[p.size]} onClick={() => { p.dispatch({ kind: "go", page: "size" }); }} />
          )}
          <Row label="Playback speed" value={speedLabel(p.speed)} onClick={() => { p.dispatch({ kind: "go", page: "speed" }); }} />
        </ul>
      );
      break;
    case "quality":
      body = (
        <Page title="Quality" onBack={() => { p.dispatch({ kind: "back" }); }}>
          <Choice
            selected={p.selectedLevel === -1}
            label={p.selectedLevel === -1 && p.playingHeight ? `Auto (${String(p.playingHeight)}p)` : "Auto"}
            onClick={() => {
              p.onLevel(-1);
              p.dispatch({ kind: "chosen" });
            }}
          />
          {levels.map((l) => (
            <Choice
              key={l.index}
              selected={p.selectedLevel === l.index}
              label={`${String(l.height)}p`}
              detail={`${(l.bitrate / 1_000_000).toFixed(1)} Mbit/s`}
              onClick={() => {
                p.onLevel(l.index);
                p.dispatch({ kind: "chosen" });
              }}
            />
          ))}
        </Page>
      );
      break;
    case "subtitles":
      body = (
        <Page title="Subtitles" onBack={() => { p.dispatch({ kind: "back" }); }}>
          <Choice
            selected={p.subtitle === null}
            label="Off"
            onClick={() => {
              p.onSubtitle(null);
              p.dispatch({ kind: "chosen" });
            }}
          />
          {p.groups.map((g) => (
            <li key={g.language} role="presentation">
              {g.tracks.length > 1 && (
                <p className="px-4 pt-3 pb-1 text-xs font-semibold tracking-wide text-fg-subtle uppercase">{g.name}</p>
              )}
              <ul role="group" aria-label={g.name}>
                {g.tracks.map((c) => (
                  <Choice
                    key={c.index}
                    selected={p.subtitle === c.index}
                    label={c.name}
                    lang={c.language === "und" ? undefined : c.language}
                    onClick={() => {
                      p.onSubtitle(c.index);
                      p.dispatch({ kind: "chosen" });
                    }}
                  />
                ))}
              </ul>
            </li>
          ))}
        </Page>
      );
      break;
    case "size":
      body = (
        <Page title="Subtitle size" onBack={() => { p.dispatch({ kind: "back" }); }}>
          {SUBTITLE_SIZES.map((s) => (
            <Choice
              key={s}
              selected={p.size === s}
              label={SIZE_LABELS[s]}
              onClick={() => {
                p.onSize(s);
                p.dispatch({ kind: "chosen" });
              }}
            />
          ))}
        </Page>
      );
      break;
    case "speed":
      body = (
        <Page title="Playback speed" onBack={() => { p.dispatch({ kind: "back" }); }}>
          {SPEEDS.map((s) => (
            <Choice
              key={s}
              selected={p.speed === s}
              label={speedLabel(s)}
              onClick={() => {
                p.onSpeed(s);
                p.dispatch({ kind: "chosen" });
              }}
            />
          ))}
        </Page>
      );
      break;
  }

  return (
    <div
      ref={panel}
      role="dialog"
      aria-label="Settings"
      onPointerDown={(e) => {
        e.stopPropagation();
      }}
      className="absolute right-3 bottom-16 z-30 max-h-[min(24rem,calc(100%-6rem))] w-72 overflow-y-auto overscroll-contain rounded-xl border border-line bg-surface/95 py-1.5 text-sm shadow-2xl shadow-scrim/60 backdrop-blur-md"
    >
      {body}
    </div>
  );
}

function Row({ label, value, onClick }: { label: string; value: string; onClick: () => void }) {
  return (
    <li>
      <button
        type="button"
        onClick={onClick}
        className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors duration-150 hover:bg-overlay focus-visible:bg-overlay focus-visible:outline-none"
      >
        <span className="flex-1 text-fg">{label}</span>
        <span className="max-w-32 truncate text-fg-muted">{value}</span>
        <ChevronRightIcon size={16} className="text-fg-subtle" />
      </button>
    </li>
  );
}

function Page({ title, onBack, children }: { title: string; onBack: () => void; children: ReactNode }) {
  return (
    <>
      <button
        type="button"
        onClick={onBack}
        aria-label={`Back from ${title}`}
        className="sticky top-0 z-10 flex w-full items-center gap-2 border-b border-line bg-surface px-3 py-2.5 font-semibold text-fg hover:bg-overlay focus-visible:bg-overlay focus-visible:outline-none"
      >
        <ArrowLeftIcon size={16} />
        {title}
      </button>
      <ul className="py-1" role="radiogroup" aria-label={title}>
        {children}
      </ul>
    </>
  );
}

function Choice({
  selected,
  label,
  detail,
  lang,
  onClick,
}: {
  selected: boolean;
  label: string;
  detail?: string;
  lang?: string;
  onClick: () => void;
}) {
  return (
    <li role="presentation">
      <button
        type="button"
        role="radio"
        aria-checked={selected}
        onClick={onClick}
        className="flex w-full items-center gap-3 px-4 py-2 text-left transition-colors duration-150 hover:bg-overlay focus-visible:bg-overlay focus-visible:outline-none"
      >
        <span className="w-4 text-accent-soft">{selected && <CheckIcon size={16} />}</span>
        <span lang={lang} className={`flex-1 ${selected ? "text-fg" : "text-fg-muted"}`}>
          {label}
        </span>
        {detail && <span className="text-xs text-fg-subtle">{detail}</span>}
      </button>
    </li>
  );
}
