import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { HelpTip } from "@/components/help-tip";
import type { Fact } from "@/components/server-overview";
import { type ApiError, type AppRow, type ObjectsAnswer, type ObjectsBlock, appObjects, removeListingCredential, setListingCredential } from "@/lib/api";
import { notify } from "@/lib/notify";
import { type Nouns, nounsOf } from "@/lib/object-words";
import { refused } from "@/lib/say";
import {
  CANCEL, COPIED, COPY, COPY_REFUSED, GENERATE, LISTING_LABEL, LISTING_NOT_SET, OR_PASTE, PASTE_MISSING, REMOVE, REMOVE_CONFIRM, SET_AGAIN, SET_ONE, STORE, STORED, VALUE_LABEL,
  beforePin, fieldFoot, generatedLine, listingRowHelp, removeBody, removeTitle, removedToast, storedToast,
} from "@/lib/server-setup-words";

// The objects answer the server page reads once, and the listing
// credential row under the server's own secret that it feeds. The row sits
// inside the Overview's Server authentication card, so the page hands the
// answer down through ObjectsReadContext rather than through the cards
// between.

const CODE = "rounded bg-muted px-1 font-mono text-[13px] text-foreground";

// ObjectsRead is the page's objects answer, null while it is read or when
// the read failed, with the door to read it again and whether the listing
// credential field is open, which the setup card's Set it opens too.
export type ObjectsRead = { answer: ObjectsAnswer | null; reload: () => void; open: boolean; setOpen: (open: boolean) => void };

export const ObjectsReadContext = React.createContext<ObjectsRead | null>(null);

// useObjectsRead reads the server's objects answer with the smallest page
// the route takes, once per page and again when the pinned type changes or
// reload is called. A row with no stored manifest is not read, since the
// route has none to answer from. A failed read keeps the answer before it.
export function useObjectsRead(id: string, enabled: boolean, pinnedType: string): ObjectsRead {
  const [read, setRead] = React.useState<{ id: string; answer: ObjectsAnswer } | null>(null);
  const [nonce, setNonce] = React.useState(0);
  const [open, setOpen] = React.useState(false);
  React.useEffect(() => {
    if (!enabled) return undefined;
    let alive = true;
    appObjects(id, "limit=1").then((answer) => { if (alive) setRead({ id, answer }); }, () => undefined);
    return () => { alive = false; };
  }, [id, enabled, pinnedType, nonce]);
  const reload = React.useCallback(() => setNonce((n) => n + 1), []);
  return { answer: enabled && read && read.id === id ? read.answer : null, reload, open, setOpen };
}

// takesListing tells whether the server lists its objects with a listing
// credential of its own. A pinned server's listing says so. Before a pin it
// is a remote server that takes a credential, the rule strazad applies.
export function takesListing(app: AppRow, answer: ObjectsAnswer): boolean {
  if (answer.type) return !!answer.listing && answer.listing.credential !== "not-needed";
  const s = app.manifest?.straza;
  return (s?.runtime?.kind || app.runtime) === "remote" && (s?.credential?.kind || "none") !== "none";
}

// declaredOf is the block the server's marks declare, when it names a type.
export const declaredOf = (answer: ObjectsAnswer): ObjectsBlock | null => (answer.declared && answer.declared.type ? answer.declared : null);

// listingOf answers the nouns and the listing tool the row and the setup
// card speak of: the pinned block's once a type is pinned, else the marks'.
export function listingOf(answer: ObjectsAnswer): { w: Nouns; lister: string } | null {
  if (answer.type) return { w: nounsOf({ type: answer.type, plural: answer.plural }), lister: answer.listing?.tool || answer.pinned?.list.tool || "" };
  const d = declaredOf(answer);
  return d ? { w: nounsOf(d), lister: d.list.tool } : null;
}

// generated is 32 random bytes from the browser, base64url without padding.
function generated(): string {
  let bin = "";
  for (const b of crypto.getRandomValues(new Uint8Array(32))) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

type RowProps = { app: AppRow; w: Nouns; lister: string; pinned: boolean; set: boolean; read: ObjectsRead };

// ListingRow is the row's body in the shape of the server's own secret:
// why it cannot be set before a pin, Set one, the open field, and the
// stored state with Set it again and Remove. Generate fills the field with
// a value shown in clear with Copy, and Store sends it as it sends a pasted
// one, because storing starts a listing that fails until the server holds
// the value. A value lives in state only until the store answers, and is
// never logged.
function ListingRow({ app, w, lister, pinned, set, read }: RowProps) {
  const [value, setValue] = React.useState("");
  const [made, setMade] = React.useState(false);
  const [fingerprint, setFingerprint] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [ask, setAsk] = React.useState(false);
  const [copyRefused, setCopyRefused] = React.useState(false);
  const input = React.useRef<HTMLInputElement>(null);
  const help = <HelpTip label={LISTING_LABEL} text={listingRowHelp(app.name, lister, w)} className="ml-1" />;

  const close = () => { setValue(""); setMade(false); setCopyRefused(false); setProblem(null); read.setOpen(false); };

  const store = async () => {
    if (!value) { setProblem(PASTE_MISSING); input.current?.focus(); return; }
    setBusy(true);
    setProblem(null);
    try {
      const answer = await setListingCredential(app.id, value);
      setFingerprint(answer?.fingerprint || "");
      close();
      notify.ok(storedToast(app.name, w));
      read.reload();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setProblem(refused(err));
    } finally {
      setBusy(false);
    }
  };

  const generate = () => { setValue(generated()); setMade(true); setCopyRefused(false); setProblem(null); };

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopyRefused(false);
      notify.ok(COPIED);
    } catch {
      setCopyRefused(true);
      input.current?.select();
    }
  };

  const remove = async () => {
    setBusy(true);
    setProblem(null);
    try {
      await removeListingCredential(app.id);
      setFingerprint("");
      notify.ok(removedToast(app.name));
      read.reload();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setProblem(refused(err));
    } finally {
      setBusy(false);
      setAsk(false);
    }
  };

  const problemLine = problem && <p className="m-0 text-[13px] text-danger" role="alert">{problem}</p>;

  if (!pinned) return <span className="text-muted-foreground" data-listing-before>{beforePin(app.name, w)}{help}</span>;
  if (read.open) {
    return (
      <div className="flex flex-col gap-1.5" data-listing-field>
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" onClick={generate} disabled={busy}>{GENERATE}</Button>
          <span className="text-muted-foreground">{OR_PASTE}</span>
          <Input
            ref={input}
            type={made ? "text" : "password"}
            autoComplete="off"
            spellCheck={false}
            aria-label={VALUE_LABEL}
            aria-invalid={problem === PASTE_MISSING || undefined}
            className={made ? "h-8 w-[28rem] max-w-full font-mono text-[13px]" : "h-8 w-64 font-mono"}
            value={value}
            onChange={(e) => { setValue(e.target.value); setMade(false); if (problem === PASTE_MISSING) setProblem(null); }}
          />
          {made && <Button size="sm" variant="outline" onClick={() => void copy()}>{COPY}</Button>}
          <Button size="sm" onClick={() => void store()} disabled={busy} aria-busy={busy || undefined}>{busy && <Loader2Icon className="animate-spin" />}{STORE}</Button>
          <Button size="sm" variant="ghost" onClick={close} disabled={busy}>{CANCEL}</Button>
        </div>
        {problemLine}
        {made && <p className="m-0 text-[13px] text-text-2" data-generated>{generatedLine(app.name)}</p>}
        {copyRefused && <p className="m-0 text-[13px] text-warn" role="status">{COPY_REFUSED}</p>}
        <p className="m-0 text-[13px] text-muted-foreground">{fieldFoot(app.name, lister)}</p>
      </div>
    );
  }
  if (!set) {
    return (
      <span data-listing-state="missing">
        {LISTING_NOT_SET + " "}
        <Button size="sm" variant="ghost" className="h-7" onClick={() => read.setOpen(true)}>{SET_ONE}</Button>
        {help}
      </span>
    );
  }
  return (
    <span data-listing-state="set">
      {STORED}
      {fingerprint && <>{" "}<code className={CODE}>{fingerprint + "…"}</code></>}
      {" "}
      <Button size="sm" variant="ghost" className="h-7" onClick={() => read.setOpen(true)}>{SET_AGAIN}</Button>
      <Button size="sm" variant="ghost" className="h-7" onClick={() => setAsk(true)} disabled={busy}>{REMOVE}</Button>
      {help}
      {problemLine}
      <AlertDialog open={ask} onOpenChange={(o) => { if (!o) setAsk(false); }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{removeTitle(app.name)}</AlertDialogTitle>
            <AlertDialogDescription>{removeBody(app.name, w)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction className="bg-danger text-white hover:bg-danger/90" disabled={busy} onClick={() => void remove()}>{REMOVE_CONFIRM}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </span>
  );
}

// useListingFact is the Listing credential row of the Credential card, or
// null when the page holds no objects answer, the server takes no listing
// credential, or its tools declare no type and it pins none.
export function useListingFact(app: AppRow): Fact | null {
  const read = React.useContext(ObjectsReadContext);
  const answer = read?.answer;
  if (!read || !answer || !takesListing(app, answer)) return null;
  const of = listingOf(answer);
  if (!of) return null;
  const pinned = !!answer.type;
  return {
    label: LISTING_LABEL,
    body: <div data-listing-row><ListingRow app={app} w={of.w} lister={of.lister} pinned={pinned} set={pinned && answer.listing?.credential === "set"} read={read} /></div>,
  };
}
