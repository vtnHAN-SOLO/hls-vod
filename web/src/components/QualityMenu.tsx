export interface QualityLevel {
  index: number; // hls.js level index
  height: number;
  bitrate: number; // bits/s
}

interface Props {
  levels: readonly QualityLevel[];
  selected: number; // -1 = automatic
  playingHeight: number | null; // what auto picked, shown as "Auto (720p)"
  onSelect: (index: number) => void;
}

export function QualityMenu({ levels, selected, playingHeight, onSelect }: Props) {
  if (levels.length < 2) return null;
  const sorted = [...levels].sort((a, b) => b.height - a.height);
  return (
    <label className="flex items-center gap-1 text-sm">
      <span className="sr-only">Quality</span>
      <select
        className="cursor-pointer rounded bg-black/60 px-2 py-1 text-sm outline-none focus-visible:ring-2 focus-visible:ring-white/70"
        value={selected}
        onChange={(e) => {
          onSelect(Number(e.target.value));
        }}
      >
        <option value={-1}>Auto{selected === -1 && playingHeight ? ` (${playingHeight}p)` : ""}</option>
        {sorted.map((l) => (
          <option key={l.index} value={l.index}>
            {l.height}p · {(l.bitrate / 1_000_000).toFixed(1)} Mbit/s
          </option>
        ))}
      </select>
    </label>
  );
}
