import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { DANGER_FILL } from "@/components/ui/button";
import { RefusedError } from "@/components/error-state";
import { type ApiError, revokeSession } from "@/lib/api";
import { notify } from "@/lib/notify";
import { CANCEL } from "@/lib/role-words";
import { refused } from "@/lib/say";
import { OWN_SESSION_WARNING, REVOKE_SESSION, REVOKE_TITLE, revokePageBody, revokedToast } from "@/lib/session-words";

type Props = {
  id: string;
  // name is the session's user, the one who can start a new session.
  name: string;
  own: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onRevoked runs once the revoke landed, where the page reads itself
  // again.
  onRevoked: () => void;
};

// SessionRevoke is the confirm of Revoke session. The server takes no
// reason on this route, so the dialog asks none. A refusal stays inside it
// with the server's own sentence, and the focus goes back to the page's
// Revoke session button when it closes, or to the page's title once the
// button is gone.
export function SessionRevoke({ id, name, own, open, onOpenChange, onRevoked }: Props) {
  const [busy, setBusy] = React.useState(false);
  const [problem, setProblem] = React.useState<string | null>(null);

  const go = async () => {
    if (busy) return;
    setBusy(true);
    setProblem(null);
    try {
      await revokeSession(id);
      notify.ok(revokedToast);
      onOpenChange(false);
      onRevoked();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setProblem(refused(err));
    } finally {
      setBusy(false);
    }
  };

  const restore = (e: Event) => {
    e.preventDefault();
    const to = document.querySelector<HTMLElement>("[data-revoke-session]") || document.querySelector<HTMLElement>("main h1, h1");
    to?.focus();
  };

  return (
    <AlertDialog open={open} onOpenChange={(o) => { if (!o && !busy) { setProblem(null); onOpenChange(false); } }}>
      <AlertDialogContent className="sm:max-w-[560px]" data-session-revoke onCloseAutoFocus={restore}>
        <AlertDialogHeader>
          <AlertDialogTitle>{REVOKE_TITLE}</AlertDialogTitle>
          <AlertDialogDescription>{revokePageBody(name)}</AlertDialogDescription>
        </AlertDialogHeader>
        {own && <p className="text-dense font-semibold text-foreground" data-own-warning>{OWN_SESSION_WARNING}</p>}
        {problem && <RefusedError subject={REVOKE_SESSION} message={problem} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
          <AlertDialogAction className={DANGER_FILL} disabled={busy} onClick={(e) => { e.preventDefault(); void go(); }}>
            {busy && <Loader2Icon className="animate-spin" />}{REVOKE_SESSION}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
