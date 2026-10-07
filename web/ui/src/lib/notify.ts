// Toasts for the result of an action the person just took. A good or
// advisory result closes on its own; a failure stays until
// the person closes it, because it says what to do next.
import { toast } from "sonner";

const SHOWN_MS = 5000;

export const notify = {
  ok: (text: string) => toast.success(text, { duration: SHOWN_MS }),
  warn: (text: string) => toast.warning(text, { duration: SHOWN_MS }),
  failed: (text: string) => toast.error(text, { duration: Infinity }),
  // undo is a good result with the one action that takes it back.
  undo: (text: string, label: string, onUndo: () => void) => toast.success(text, { duration: SHOWN_MS, action: { label, onClick: onUndo } }),
};
