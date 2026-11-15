import { useEffect, useRef } from "react";

interface Props {
  message: string;
  confirmLabel: string;
  danger?: boolean;
  pending?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

/** A modal question: the native <dialog>, so focus, Escape and the backdrop come for free. */
export function ConfirmDialog({ message, confirmLabel, danger = false, pending = false, onConfirm, onCancel }: Props) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal();
    return () => d?.close();
  }, []);
  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault();
        onCancel();
      }}
      className="m-auto max-w-md rounded-lg border border-white/10 bg-neutral-900 p-6 text-neutral-100 backdrop:bg-black/60"
    >
      <p className="text-sm leading-relaxed">{message}</p>
      <div className="mt-6 flex justify-end gap-3">
        <button
          type="button"
          onClick={onCancel}
          className="rounded-md px-3 py-1.5 text-sm hover:bg-white/10"
          autoFocus
        >
          Cancel
        </button>
        <button
          type="button"
          disabled={pending}
          onClick={onConfirm}
          className={`rounded-md px-3 py-1.5 text-sm font-medium disabled:opacity-50 ${
            danger ? "bg-red-600 hover:bg-red-500" : "bg-white/15 hover:bg-white/25"
          }`}
        >
          {confirmLabel}
        </button>
      </div>
    </dialog>
  );
}
