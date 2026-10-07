// The confirms of the Credentials tab. Removing a
// token, removing a credential for a server nobody reaches any more and
// disconnecting a sign-in all destroy something a call depends on, so each
// asks first in a dialog that names the server and says in one sentence
// what stops working. One component draws all three, since they differ only
// in their words.
import * as React from "react";
import { Loader2Icon } from "lucide-react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { RefusedError } from "@/components/error-state";
import { type ApiError, connectDelete } from "@/lib/api";
import { refused } from "@/lib/say";
import { type CredentialRow, keyOf, thingOf } from "./credential-rows";
import * as W from "./credential-words";

// Ask is the confirm on screen: which row, and which of the three the
// person pressed.
export type Ask = { row: CredentialRow; kind: "remove" | "leftover" | "disconnect" };

type Props = {
  ask: Ask | null;
  onClose: () => void;
  // onDone says the server answered, so the tab redraws the row and says so.
  onDone: (ask: Ask) => void;
};

// wordsOf is the title, the one sentence of consequence, the button and the
// subject an error block names, for each of the three confirms.
function wordsOf(ask: Ask) {
  const { row, kind } = ask;
  if (kind === "disconnect") {
    return { title: W.disconnectTitle(row.app), body: W.disconnectBody(row.app, row.who), action: W.DISCONNECT, subject: W.SUBJECT_DISCONNECT };
  }
  if (kind === "leftover") {
    const token = thingOf(row) === "token";
    return {
      title: W.leftoverTitle(row.app, thingOf(row)),
      body: W.leftoverBody(row.app, row.who),
      action: token ? W.REMOVE_TOKEN : W.REMOVE_SIGN_IN,
      subject: token ? W.SUBJECT_REMOVE : W.SUBJECT_REMOVE_SIGN_IN,
    };
  }
  return { title: W.removeTokenTitle(row.app, row.who), body: W.removeTokenBody(row.app, row.who), action: W.REMOVE_TOKEN, subject: W.SUBJECT_REMOVE };
}

// ConfirmDialog asks before it destroys, then sends the one DELETE.
export function ConfirmDialog({ ask, onClose, onDone }: Props) {
  const [busy, setBusy] = React.useState(false);
  const [refusal, setRefusal] = React.useState("");
  const said = ask ? wordsOf(ask) : null;

  const run = async () => {
    if (!ask || busy) return;
    setBusy(true);
    setRefusal("");
    try {
      await connectDelete(ask.row.app, ask.row.who);
      onDone(ask);
    } catch (e) {
      // A 401 ends the login session in the api module, and the page
      // redraws as signed out, so the dialog adds nothing.
      const err = e as ApiError;
      if (err.status !== 401) setRefusal(refused(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AlertDialog
      open={!!ask}
      onOpenChange={(open) => { if (!open && !busy) { setRefusal(""); onClose(); } }}
    >
      <AlertDialogContent className="sm:max-w-[520px]" data-confirm={ask ? keyOf(ask.row) : undefined}>
        <AlertDialogHeader>
          <AlertDialogTitle>{said ? said.title : ""}</AlertDialogTitle>
          <AlertDialogDescription>{said ? said.body : ""}</AlertDialogDescription>
        </AlertDialogHeader>
        {refusal && said && <RefusedError subject={said.subject} message={refusal} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{W.CANCEL}</AlertDialogCancel>
          <AlertDialogAction
            className="bg-danger text-white hover:bg-danger/90"
            aria-busy={busy || undefined}
            onClick={(e) => { e.preventDefault(); void run(); }}
          >
            {busy && <Loader2Icon className="animate-spin" />} {said ? said.action : ""}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
