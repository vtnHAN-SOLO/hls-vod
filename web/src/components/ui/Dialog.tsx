import { type ReactNode, useEffect, useId, useRef } from "react";

/**
 * A modal: the native <dialog>, so focus trapping, Escape and the inert
 * page behind come from the browser. Mounted = open; Escape and the close
 * button call onClose.
 */
export function Dialog({
  title,
  children,
  onClose,
  className = "",
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  className?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal();
    return () => d?.close();
  }, []);
  return (
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      className={`m-auto w-[min(32rem,calc(100vw-2rem))] rounded-2xl border border-line bg-surface p-6 text-fg shadow-2xl shadow-scrim backdrop:bg-scrim/70 backdrop:backdrop-blur-sm ${className}`}
    >
      <h2 id={titleId} className="mb-4 text-lg font-semibold">
        {title}
      </h2>
      {children}
    </dialog>
  );
}
