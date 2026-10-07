// The paste sheet of the Credentials tab: the
// token a person creates at the server, for themselves or for an agent they
// sponsor. The value is read from its box once, on save, and is never held
// in state, so nothing on the page and nothing in a React tree keeps it
// after the call; the row redraws from the server's answer, which carries a
// fingerprint and never the value.
import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { RefusedError } from "@/components/error-state";
import { CAPS, Field } from "@/components/wizard/parts";
import { type ApiError, type ConnectAnswer, connectToken } from "@/lib/api";
import { refused } from "@/lib/say";
import { KIND_NAME } from "@/lib/server-words";
import { type CredentialRow, keyOf } from "./credential-rows";
import * as W from "./credential-words";

type Props = {
  // row is the server the token is for, or null while the sheet is closed.
  row: CredentialRow | null;
  onClose: () => void;
  // onSaved hands the tab the server's answer, which redraws the row.
  onSaved: (row: CredentialRow, answer: ConnectAnswer) => void;
};

// PasteSheet is the sheet a Paste a token, Replace or Paste a new token
// button opens.
export function PasteSheet({ row, onClose, onSaved }: Props) {
  const [busy, setBusy] = React.useState(false);
  return (
    <Sheet open={!!row} onOpenChange={(open) => { if (!open && !busy) onClose(); }}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-[520px]" data-paste-sheet={row ? keyOf(row) : undefined}>
        {row && <Body key={keyOf(row)} row={row} busy={busy} setBusy={setBusy} onClose={onClose} onSaved={onSaved} />}
      </SheetContent>
    </Sheet>
  );
}

type BodyProps = Props & { row: CredentialRow; busy: boolean; setBusy: (busy: boolean) => void };

function Body({ row, busy, setBusy, onClose, onSaved }: BodyProps) {
  const box = React.useRef<HTMLInputElement>(null);
  const day = React.useRef<HTMLInputElement>(null);
  const [miss, setMiss] = React.useState("");
  const [refusal, setRefusal] = React.useState("");

  // save reads the value once and empties the box in the same moment, so a
  // refusal leaves the sheet open with nothing sensitive still on screen.
  // The expiry travels as an instant at the start of that day in UTC, which
  // is how the server records it.
  const save = async () => {
    if (busy) return;
    const value = box.current ? box.current.value : "";
    if (box.current) box.current.value = "";
    if (!value.trim()) {
      setMiss(W.tokenRequired(row.app));
      setRefusal("");
      box.current?.focus();
      return;
    }
    setMiss("");
    setRefusal("");
    setBusy(true);
    const at = day.current && day.current.value ? day.current.value + "T00:00:00Z" : "";
    try {
      const answer = await connectToken(row.app, value, at, row.who);
      onSaved(row, answer);
    } catch (e) {
      // A 401 is the session module's: it ends the login and the page
      // redraws as signed out, so the sheet says nothing of its own.
      const err = e as ApiError;
      if (err.status !== 401) setRefusal(refused(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <SheetHeader className="border-b border-border pr-12">
        <SheetTitle className="text-lg leading-snug">{W.pasteTitle(row.app, !!row.connected, row.who)}</SheetTitle>
        <SheetDescription>{W.sealedLine(row.app)}</SheetDescription>
      </SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4">
        <div className="flex flex-col gap-1 rounded-md border border-border bg-card px-4 py-3">
          <span className={CAPS}>{KIND_NAME.token}</span>
          <span className="text-[13px] leading-snug text-text-2">{W.whoseToken(row.who, row.app)}</span>
        </div>
        <Field id="cred-token" label={W.TOKEN_LABEL} error={miss || undefined} hint={W.tokenHint(row.app)}>
          <Input
            id="cred-token"
            ref={box}
            type="password"
            autoComplete="off"
            spellCheck={false}
            aria-describedby="cred-token-say"
            aria-invalid={miss ? true : undefined}
          />
        </Field>
        <Field id="cred-expiry" label={W.EXPIRY_LABEL} hint={W.expiryHint(row.app)}>
          <Input id="cred-expiry" ref={day} type="date" aria-describedby="cred-expiry-say" />
        </Field>
      </div>
      <SheetFooter className="mt-0 border-t border-border">
        {refusal && <RefusedError subject={W.SUBJECT_SAVE} message={refusal} />}
        <div className="flex items-center justify-end gap-2">
          <Button variant="outline" onClick={onClose} disabled={busy}>{W.CANCEL}</Button>
          <Button onClick={() => void save()} disabled={busy} data-save-token>
            {busy && <Loader2Icon className="animate-spin" />}
            {W.SAVE_TOKEN}
          </Button>
        </div>
      </SheetFooter>
    </>
  );
}
