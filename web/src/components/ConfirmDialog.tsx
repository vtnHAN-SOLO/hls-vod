import { Button } from "./ui/Button";
import { Dialog } from "./ui/Dialog";

interface Props {
  message: string;
  confirmLabel: string;
  title?: string;
  danger?: boolean;
  pending?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

/** A modal question, on the shared Dialog. Cancel has focus first: the safe choice. */
export function ConfirmDialog({
  message,
  confirmLabel,
  title = "Are you sure?",
  danger = false,
  pending = false,
  onConfirm,
  onCancel,
}: Props) {
  return (
    <Dialog title={title} onClose={onCancel}>
      <p className="text-sm leading-relaxed text-fg-muted">{message}</p>
      <div className="mt-6 flex justify-end gap-3">
        <Button variant="ghost" onClick={onCancel} autoFocus>
          Cancel
        </Button>
        <Button variant={danger ? "danger" : "primary"} disabled={pending} onClick={onConfirm}>
          {confirmLabel}
        </Button>
      </div>
    </Dialog>
  );
}
