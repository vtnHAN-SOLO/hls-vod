// Every icon of the SPA: small inline SVGs drawn for this project, stroked
// with currentColor (so they take the text colour) and hidden from screen
// readers (the control around them carries the label). No icon library.
import type { SVGProps } from "react";

type IconProps = Omit<SVGProps<SVGSVGElement>, "children"> & { size?: number };

function Icon({ size = 20, children, ...rest }: IconProps & { children: React.ReactNode }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...rest}
    >
      {children}
    </svg>
  );
}

export function PlayIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M7 4.5v15l13-7.5z" fill="currentColor" stroke="none" />
    </Icon>
  );
}

export function InfoIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 11v5M12 7.5v.5" />
    </Icon>
  );
}

export function ChevronLeftIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M15 5l-7 7 7 7" />
    </Icon>
  );
}

export function ChevronRightIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M9 5l7 7-7 7" />
    </Icon>
  );
}

export function ChevronDownIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M6 9l6 6 6-6" />
    </Icon>
  );
}

export function SearchIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <circle cx="11" cy="11" r="6.5" />
      <path d="M16 16l4 4" />
    </Icon>
  );
}

export function UserIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <circle cx="12" cy="8.5" r="3.5" />
      <path d="M5 19.5c1.2-3.3 3.9-5 7-5s5.8 1.7 7 5" />
    </Icon>
  );
}

export function ShieldIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M12 3l7 3v5.5c0 4.3-3 7.9-7 9.5-4-1.6-7-5.2-7-9.5V6z" />
    </Icon>
  );
}

export function LogOutIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M10 4H6a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h4M15 8l4 4-4 4M19 12H9" />
    </Icon>
  );
}

export function SparkIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M12 3l1.9 5.6L19.5 10.5l-5.6 1.9L12 18l-1.9-5.6L4.5 10.5l5.6-1.9z" />
    </Icon>
  );
}

export function PencilIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M4 20h4L19 9l-4-4L4 16zM13.5 6.5l4 4" />
    </Icon>
  );
}

export function TrashIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3" />
    </Icon>
  );
}

export function RefreshIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M20 11a8 8 0 0 0-14.6-4.5M4 4v3h3M4 13a8 8 0 0 0 14.6 4.5M20 20v-3h-3" />
    </Icon>
  );
}

export function FilmIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      <path d="M7.5 4.5v15M16.5 4.5v15M3.5 9h4M3.5 15h4M16.5 9h4M16.5 15h4" />
    </Icon>
  );
}

export function ClockIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 7.5V12l3 2" />
    </Icon>
  );
}

export function TrendIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M3.5 17l6-6 4 4 7-7.5M15 7.5h5.5V13" />
    </Icon>
  );
}

export function CloseIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M6 6l12 12M18 6L6 18" />
    </Icon>
  );
}

export function ArrowLeftIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M19 12H5M11 6l-6 6 6 6" />
    </Icon>
  );
}

// The player's controls.

export function PauseIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <rect x="6.5" y="5" width="3.6" height="14" rx="1" fill="currentColor" stroke="none" />
      <rect x="13.9" y="5" width="3.6" height="14" rx="1" fill="currentColor" stroke="none" />
    </Icon>
  );
}

/** A circular arrow turning back, with the seconds inside. */
export function SkipBackIcon({ seconds = 5, ...p }: IconProps & { seconds?: number }) {
  return (
    <Icon {...p}>
      <path d="M4.5 12a7.5 7.5 0 1 0 2.2-5.3" />
      <path d="M6.2 3.2v3.6h3.6" />
      <text x="12" y="15.6" textAnchor="middle" fontSize="8.5" fontWeight="700" fill="currentColor" stroke="none">
        {seconds}
      </text>
    </Icon>
  );
}

/** A circular arrow turning forward, with the seconds inside. */
export function SkipForwardIcon({ seconds = 5, ...p }: IconProps & { seconds?: number }) {
  return (
    <Icon {...p}>
      <path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3" />
      <path d="M17.8 3.2v3.6h-3.6" />
      <text x="12" y="15.6" textAnchor="middle" fontSize="8.5" fontWeight="700" fill="currentColor" stroke="none">
        {seconds}
      </text>
    </Icon>
  );
}

function Speaker() {
  return <path d="M4 9.5h3.2L12 5.5v13l-4.8-4H4z" fill="currentColor" />;
}

export function VolumeMutedIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <Speaker />
      <path d="M16 9.5l5 5M21 9.5l-5 5" />
    </Icon>
  );
}

export function VolumeLowIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <Speaker />
      <path d="M15.5 9.2a4 4 0 0 1 0 5.6" />
    </Icon>
  );
}

export function VolumeHighIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <Speaker />
      <path d="M15.5 9.2a4 4 0 0 1 0 5.6M18.2 6.6a7.6 7.6 0 0 1 0 10.8" />
    </Icon>
  );
}

export function SubtitlesIcon({ on = false, ...p }: IconProps & { on?: boolean }) {
  return (
    <Icon {...p}>
      <rect x="3" y="5.5" width="18" height="13" rx="2.5" fill={on ? "currentColor" : "none"} />
      <path
        d="M10.2 10.6a2.2 2.2 0 1 0 0 2.8M16.7 10.6a2.2 2.2 0 1 0 0 2.8"
        stroke={on ? "var(--color-bg)" : "currentColor"}
      />
    </Icon>
  );
}

export function GearIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 13.5a7.6 7.6 0 0 0 0-3l2-1.6-2-3.4-2.4 1a7.6 7.6 0 0 0-2.6-1.5L14 2.5h-4l-.4 2.5A7.6 7.6 0 0 0 7 6.5l-2.4-1-2 3.4 2 1.6a7.6 7.6 0 0 0 0 3l-2 1.6 2 3.4 2.4-1A7.6 7.6 0 0 0 9.6 19l.4 2.5h4l.4-2.5a7.6 7.6 0 0 0 2.6-1.5l2.4 1 2-3.4z" />
    </Icon>
  );
}

export function PipIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <rect x="2.5" y="4.5" width="19" height="15" rx="2" />
      <rect x="12" y="11.5" width="7" height="5.5" rx="1" fill="currentColor" stroke="none" />
    </Icon>
  );
}

/** Four corners pointing out: go fullscreen. */
export function ExpandIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M4 9V4h5M15 4h5v5M20 15v5h-5M9 20H4v-5" />
    </Icon>
  );
}

/** Four corners pointing in: leave fullscreen. */
export function CollapseIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M9 4v5H4M20 9h-5V4M15 20v-5h5M4 15h5v5" />
    </Icon>
  );
}

export function CheckIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M5 12.5l4.5 4.5L19 7.5" />
    </Icon>
  );
}

export function SpinnerIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <circle cx="12" cy="12" r="9" opacity="0.25" />
      <path d="M21 12a9 9 0 0 0-9-9" />
    </Icon>
  );
}

export function ImageIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      <circle cx="9" cy="10" r="1.6" />
      <path d="m4 17.5 5-5 4 4 2.5-2.5 4.5 4.5" />
    </Icon>
  );
}

export function EyeIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z" />
      <circle cx="12" cy="12" r="2.8" />
    </Icon>
  );
}

export function EyeOffIcon(p: IconProps) {
  return (
    <Icon {...p}>
      <path d="M4 4l16 16" />
      <path d="M9.9 6A9.6 9.6 0 0 1 12 5.5c6 0 9.5 6.5 9.5 6.5a17 17 0 0 1-2.6 3.4M6.4 7.6A16.6 16.6 0 0 0 2.5 12S6 18.5 12 18.5a9.2 9.2 0 0 0 4-.9" />
      <path d="M10 10.1a2.8 2.8 0 0 0 3.9 3.9" />
    </Icon>
  );
}
