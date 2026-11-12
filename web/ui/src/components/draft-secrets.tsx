import * as React from "react";
import { Loader2Icon, RefreshCwIcon } from "lucide-react";
import { FetchError, RefusedError } from "@/components/error-state";
import { Button } from "@/components/ui/button";
import { DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { HINT, HealthBox, Landed, ProblemBlock, type Row, type RowState } from "@/components/wizard/parts";
import { CHECK_UNREACHABLE, findServer, problemOf, storeAndCheck } from "@/components/wizard/use-install";
import { type ApiError, type AppRow, setListingCredential } from "@/lib/api";
import type { SecretAsk } from "@/lib/drafts-model";
import {
  ADD_LATER,
  CANCEL,
  CLOSE,
  GO_LISTING,
  STORE_LISTING,
  againHint,
  enterListingOr,
  enterOr,
  idChip,
  laterLine,
  laterListing,
  leadBoth,
  leadListing,
  listingHint,
  listingLabel,
  listingMaybeStored,
  listingMaybeText,
  listingNotStored,
  listingNotStoredText,
  listingRetryHint,
  listsSoon,
  maybeStored,
  maybeText,
  notStored,
  notStoredText,
  openLive,
  publishGo,
  publishedToast,
  publishingTitle,
  retryHint,
  secretHint,
  secretLabel,
  secretsHead,
  secretsLead,
  storeListingRow,
  storeRow,
  storedNotPassed,
} from "@/lib/drafts-words";
import { navigate } from "@/lib/router";
import { RECHECK, SET_AGAIN, STORE_AND_CHECK, checkRow, finishLater, notListed, publishRow } from "@/lib/save-words";
import { cn } from "@/lib/utils";
import type { HealthRead } from "@/lib/words";

// The values a draft's new server needs and nothing stores yet, as the
// review page's publish dialog asks for them: a server's one shared
// secret, and the listing credential of a server that pins an object type.
// The Secrets section sits under the confirm blocks, and the run after the
// publish stores each value and checks its server with the Add MCP server
// wizard's sequence and words. A value never goes into a draft, so it
// lives in the dialog's state until the publish lands and is then sent
// once.

const FIELD = "h-8 w-80 max-w-full font-mono text-sm";
const LABEL = "text-sm font-semibold text-foreground";

// Field is one value the section asks: a server's secret or its listing
// credential, keyed apart in the dialog's state.
export type Field = { key: string; ask: SecretAsk; kind: "secret" | "listing" };

// fieldsOf lists the fields of the asks, a server's secret before its
// listing credential.
export const fieldsOf = (asks: SecretAsk[]): Field[] =>
  asks.flatMap((a) => [
    ...(a.secret ? [{ key: a.name, ask: a, kind: "secret" as const }] : []),
    ...(a.listing ? [{ key: a.name + "/listing", ask: a, kind: "listing" as const }] : []),
  ]);

// askState is what the section asks of the button: its label, which counts
// every value to store as a secret, and the line that names the first
// field nobody answered, or "".
export function askState(fields: Field[], values: Record<string, string>, later: Record<string, boolean>): { label: string; need: string } {
  const asked = fields.filter((f) => !later[f.key]);
  const empty = asked.find((f) => !values[f.key]);
  const label = asked.length === 1 && asked[0].kind === "listing" ? GO_LISTING : publishGo(asked.length);
  return { label, need: !empty ? "" : empty.kind === "listing" ? enterListingOr(empty.ask.name) : enterOr(empty.ask.name) };
}

// leadOf says what is missing and that the dialog stores it: one sentence
// for servers that miss only their secret, else one per server.
function leadOf(asks: SecretAsk[]): string {
  if (asks.every((a) => !a.listing)) return secretsLead(asks.map((a) => a.name));
  return asks.map((a) => (!a.listing ? secretsLead([a.name]) : a.secret ? leadBoth(a.name, a.listing) : leadListing(a.name, a.listing))).join(" ");
}

type FieldsProps = {
  asks: SecretAsk[];
  // agent is the agent that wrote the draft, which the hints say never
  // sees a value, or null when only people wrote it.
  agent: string | null;
  values: Record<string, string>;
  later: Record<string, boolean>;
  onValue: (key: string, value: string) => void;
  onLater: (key: string, on: boolean) => void;
};

// SecretFields is the Secrets section: one password field per value,
// labelled with its server's name, with Add it later under each.
export function SecretFields({ asks, agent, values, later, onValue, onLater }: FieldsProps) {
  const id = React.useId();
  const fields = fieldsOf(asks);
  const head = secretsHead(fields.length);
  return (
    <section aria-label={head} className="flex flex-col gap-3 rounded-md border border-border bg-background px-3.5 py-3" data-secrets>
      <p className="m-0 text-[13px] font-semibold text-muted-foreground">{head}</p>
      <p className="m-0 text-sm leading-relaxed text-text-2">{leadOf(asks)}</p>
      {fields.map((f, i) => {
        const field = id + "-" + i;
        const off = !!later[f.key];
        const pin = f.kind === "listing" ? f.ask.listing : null;
        return (
          <div key={f.key} className="flex flex-col gap-1.5" data-secret={f.key}>
            <label htmlFor={field} className={LABEL}>{pin ? listingLabel(f.ask.name) : secretLabel(f.ask.name)}</label>
            <Input id={field} type="password" value={values[f.key] || ""} onChange={(e) => onValue(f.key, e.target.value)} disabled={off} autoComplete="off" spellCheck={false} aria-describedby={field + "-say"} className={FIELD} />
            <p id={field + "-say"} className={cn(HINT, "m-0 max-w-[75ch]")}>{pin ? listingHint(f.ask.name, pin, agent) : secretHint(agent)}</p>
            <label className="flex items-center gap-2 text-sm text-text-2">
              <input type="checkbox" checked={off} onChange={(e) => onLater(f.key, e.target.checked)} className="size-4 shrink-0 accent-primary" />
              {ADD_LATER}
            </label>
            {off && (
              <p className="m-0 max-w-[75ch] rounded-md border border-warn/40 bg-warn-bg px-3 py-2 text-[13px] leading-snug text-text-2" data-later>
                {pin ? laterListing(f.ask.name, pin) : laterLine(f.ask.name)}
              </p>
            )}
          </div>
        );
      })}
    </section>
  );
}

// Send is one asked server at the click: the values to send, null for a
// value it does not ask or one ticked for later.
type Send = { ask: SecretAsk; secret: string | null; listing: string | null };

// OnChecked takes a server after its check: the server as the check read
// it, and the Next line its stored secret answers, null while no secret of
// it is stored.
export type OnChecked = (name: string, app: AppRow, answered: string | null) => void;

// Run is one asked server after the click: which values the dialog sends,
// its rows, the server and the read its last check left, and what did not
// land. stored and listed say a secret and a listing credential of it
// landed in this dialog, and again that Set the secret again opened its
// field.
type Run = {
  ask: SecretAsk;
  secret: boolean;
  listing: boolean;
  rows: Row[];
  app: AppRow | null;
  read: HealthRead | null;
  unlisted: boolean;
  store: ApiError | null;
  list: ApiError | null;
  check: ApiError | null;
  stored: boolean;
  listed: boolean;
  again: boolean;
};

const rowOf = (r: Run, key: Row["key"]) => r.rows.find((row) => row.key === key);
const stateIs = (r: Run, key: Row["key"], state: RowState) => rowOf(r, key)?.state === state;
// waits says the whole server waits for later, so it has no rows.
const waits = (r: Run) => !r.secret && !r.listing;
// settled says the server's run ended: nothing runs and the check ran.
const settled = (r: Run) => !waits(r) && !r.rows.some((row) => row.state === "running") && !stateIs(r, "check", "pending");
// secretLost says a secret the dialog sends did not land, or never left
// because the list did not name its server.
const secretLost = (r: Run) => r.unlisted || (r.secret && !stateIs(r, "secret", "done"));
const passed = (r: Run) => settled(r) && !secretLost(r) && !!r.read && r.read.kind === "running";
// checkFailed is a stored secret whose check did not pass, the one case
// the wizard's doors after the check serve.
const checkFailed = (r: Run) => settled(r) && !r.unlisted && r.stored && !passed(r) && !!r.app;
const openServer = (app: AppRow | null) => app && navigate("servers", [app.id]);

// useSecretRun is the run after the click. begin shows its rows while the
// publish is in flight, reset drops them when the publish is refused, and
// start runs each server in turn once it landed. busy names the act that
// runs, for the footer's button, published says the publish landed, and
// first says the first pass runs, which the title says.
export function useSecretRun(onChecked: OnChecked) {
  const [runs, setRuns] = React.useState<Run[] | null>(null);
  const [busy, setBusy] = React.useState<string | null>(null);
  const [published, setPublished] = React.useState(false);
  const [first, setFirst] = React.useState(false);

  const patch = (name: string, p: (r: Run) => Partial<Run>) => setRuns((rs) => rs && rs.map((r) => (r.ask.name === name ? { ...r, ...p(r) } : r)));
  const put = (name: string, key: Row["key"], state: RowState) => patch(name, (x) => ({ rows: x.rows.map((row) => (row.key === key ? { ...row, state } : row)) }));

  // one stores the values given, null for one it leaves as it is, then
  // probes the server.
  const one = async (r: Run, secret: string | null, listing: string | null) => {
    const name = r.ask.name;
    let stored = r.stored;
    let listed = r.listed;
    const fresh = (key: Row["key"]) => key === "check" || (key === "secret" && secret !== null) || (key === "listing" && listing !== null);
    patch(name, (x) => ({
      store: secret === null ? x.store : null, list: listing === null ? x.list : null, check: null, unlisted: false, again: false,
      rows: x.rows.map((row) => (fresh(row.key) ? { ...row, state: "pending" } : row)),
    }));
    const out = await storeAndCheck({
      name, url: r.ask.url, secret, listing, app: r.app,
      put: (key, state) => {
        if (key === "secret" && state === "done") stored = true;
        if (key === "listing" && state === "done") listed = true;
        put(name, key, state);
      },
      failed: (step, err) => patch(name, () => (step === "list" ? { unlisted: true } : step === "secret" ? { store: err || null } : step === "listing" ? { list: err || null } : { check: err || null })),
    });
    patch(name, () => ({ app: out.app, read: out.read, stored, listed }));
    if (out.app) onChecked(name, out.app, stored && r.ask.fix ? r.ask.fix : null);
  };

  // listOnly stores a listing credential again, which needs no check: the
  // listing it makes due runs on its own.
  const listOnly = async (r: Run, listing: string) => {
    const name = r.ask.name;
    put(name, "listing", "running");
    const app = r.app || (await findServer(name));
    if (!app) {
      put(name, "listing", "pending");
      patch(name, () => ({ unlisted: true }));
      return;
    }
    try {
      await setListingCredential(app.id, listing);
      put(name, "listing", "done");
      patch(name, () => ({ app, unlisted: false, list: null, listed: true }));
    } catch (e) {
      put(name, "listing", "failed");
      patch(name, () => ({ app, unlisted: false, list: e as ApiError }));
    }
  };

  const begin = (asks: { ask: SecretAsk; secret: boolean; listing: boolean }[], label: string) => {
    setRuns(asks.map(({ ask, secret, listing }) => {
      const rows: Row[] = [];
      if (secret) rows.push({ key: "secret", label: storeRow(ask.name), state: "pending" });
      if (listing) rows.push({ key: "listing", label: storeListingRow(ask.name), state: "pending" });
      if (secret || listing) rows.push({ key: "check", label: checkRow(ask.name), state: "pending" });
      return { ask, secret, listing, rows, app: null, read: null, unlisted: false, store: null, list: null, check: null, stored: false, listed: false, again: false };
    }));
    setPublished(false);
    setFirst(true);
    setBusy(label);
  };
  const reset = () => {
    setRuns(null);
    setBusy(null);
    setFirst(false);
  };

  // start takes each server with its values, in the order begin named
  // them. A value lives here until its turn, and goes once to its server.
  const start = async (sends: Send[]) => {
    setPublished(true);
    for (const s of sends) {
      if (s.secret === null && s.listing === null) continue;
      const fresh: Run = { ask: s.ask, secret: s.secret !== null, listing: s.listing !== null, rows: [], app: null, read: null, unlisted: false, store: null, list: null, check: null, stored: false, listed: false, again: false };
      await one(fresh, s.secret, s.listing);
    }
    setFirst(false);
    setBusy(null);
  };

  const after = async (name: string, label: string, go: (r: Run) => Promise<void>) => {
    const r = runs && runs.find((x) => x.ask.name === name);
    if (!r || busy) return;
    setBusy(label);
    try {
      await go(r);
    } finally {
      setBusy(null);
    }
  };

  return {
    runs, busy, published, first, begin, reset, start,
    store: (name: string, secret: string) => after(name, STORE_AND_CHECK, (r) => one(r, secret, null)),
    storeListing: (name: string, listing: string) => after(name, STORE_LISTING, (r) => listOnly(r, listing)),
    recheck: (name: string) => after(name, RECHECK, (r) => one(r, null, null)),
    again: (name: string) => patch(name, () => ({ again: true })),
  };
}

type SecretRun = ReturnType<typeof useSecretRun>;

// Retry is the field that comes back for a value that did not land, or
// after Set the secret again: it stores that value, and nothing is
// published again.
function Retry({ name, kind, hint, run }: { name: string; kind: "secret" | "listing"; hint: string; run: SecretRun }) {
  const id = React.useId();
  const [value, setValue] = React.useState("");
  const send = () => {
    if (!value || run.busy) return;
    const v = value;
    setValue("");
    void (kind === "secret" ? run.store(name, v) : run.storeListing(name, v));
  };
  return (
    <div className="flex flex-col gap-1.5" data-retry={kind}>
      <label htmlFor={id} className={LABEL}>{kind === "secret" ? secretLabel(name) : listingLabel(name)}</label>
      <Input id={id} type="password" value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off" spellCheck={false} aria-describedby={id + "-say"} className={FIELD} />
      <p id={id + "-say"} className={cn(HINT, "m-0 max-w-[75ch]")}>{hint}</p>
      <span><Button onClick={send} disabled={!value || !!run.busy}>{kind === "secret" ? STORE_AND_CHECK : STORE_LISTING}</Button></span>
    </div>
  );
}

// Outcome is what one server's run ended in, in the order the rows ran:
// the secret that did not land with its field again, the check's sentence,
// then the listing credential's line or its field again.
function Outcome({ id, r, run }: { id: string; r: Run; run: SecretRun }) {
  if (!settled(r)) return null;
  const name = r.ask.name;
  const pin = r.ask.listing;
  const lost = secretLost(r);
  let secret: React.ReactNode = null;
  if (r.unlisted) secret = <RefusedError subject={name} heading={r.secret ? notStored(name) : listingNotStored(name)} message={notListed(name)} />;
  else if (r.store) secret = r.store.unreachable ? <FetchError subject={name} heading={maybeStored(name)} detail={maybeText(id, name)} /> : <RefusedError subject={name} heading={notStored(name)} message={notStoredText(id, name, r.store.message)} />;
  let check: React.ReactNode = null;
  if (!lost && r.read && r.read.kind === "running") check = <HealthBox read={r.read} />;
  else if (!lost && r.check) check = <ProblemBlock problem={problemOf("Check", r.check, CHECK_UNREACHABLE)} />;
  else if (!lost && r.read) check = <>{r.stored && <p className="m-0 text-sm text-text-2" data-stored-not-passed>{storedNotPassed(name)}</p>}<HealthBox read={r.read} /></>;
  let listing: React.ReactNode = null;
  if (pin && !r.listing) listing = <p className={cn(HINT, "m-0")} data-later>{laterListing(name, pin)}</p>;
  else if (pin && r.listed && stateIs(r, "listing", "done")) listing = <p className="m-0 text-sm text-text-2" data-lists-soon>{listsSoon(name, pin)}</p>;
  else if (pin && r.list) {
    listing = r.list.unreachable
      ? <FetchError subject={name} heading={listingMaybeStored(name)} detail={listingMaybeText(id, name)} />
      : <RefusedError subject={name} heading={listingNotStored(name)} message={listingNotStoredText(id, name, pin, r.stored, r.list.message)} />;
  }
  const listingLost = !!pin && r.listing && !stateIs(r, "listing", "done");
  return (
    <>
      {secret}
      {r.secret && lost && <Retry name={name} kind="secret" hint={retryHint(name)} run={run} />}
      {check}
      {r.again && <Retry name={name} kind="secret" hint={againHint(name)} run={run} />}
      {listing}
      {listingLost && <Retry name={name} kind="listing" hint={listingRetryHint(name)} run={run} />}
    </>
  );
}

// Doors are a server's doors after a check that did not pass: Set the
// secret again when the server turned the secret away, and Recheck.
function Doors({ r, run, size }: { r: Run; run: SecretRun; size?: "sm" }) {
  return (
    <>
      {r.read && r.read.kind === "refused" && !r.again && <Button variant="outline" size={size} onClick={() => run.again(r.ask.name)} disabled={!!run.busy}>{SET_AGAIN}</Button>}
      <Button variant="outline" size={size} onClick={() => void run.recheck(r.ask.name)} disabled={!!run.busy}><RefreshCwIcon />{RECHECK}</Button>
    </>
  );
}

// SecretRunView is the dialog after the click: the rows, one server's
// result under them or each server's in a box of its own, and the doors.
// Cancel stays off while anything runs.
export function SecretRunView({ id, run, onClose }: { id: string; run: SecretRun; onClose: () => void }) {
  const runs = run.runs || [];
  const one = runs.length === 1 ? runs[0] : null;
  const top: Row = { key: "install", label: publishRow(idChip(id)), state: run.published ? "done" : "running" };
  const close = <Button variant="outline" onClick={onClose}>{CLOSE}</Button>;
  let foot: React.ReactNode = close;
  if (run.busy) {
    foot = <><Button variant="outline" disabled>{CANCEL}</Button><Button disabled><Loader2Icon className="animate-spin" />{run.busy}</Button></>;
  } else if (one && passed(one)) {
    foot = <>{close}<Button onClick={() => openServer(one.app)}>{openLive(one.ask.name)}</Button></>;
  } else if (one && checkFailed(one)) {
    foot = <><Doors r={one} run={run} /><span className="flex-1" /><Button variant="ghost" onClick={() => openServer(one.app)}>{finishLater(one.ask.name)}</Button></>;
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle>{run.first ? publishingTitle(id) : publishedToast(id)}</DialogTitle>
      </DialogHeader>
      {one ? (
        <div className="flex flex-col gap-3" data-run={one.ask.name}>
          <Landed rows={[top, ...one.rows]} />
          <Outcome id={id} r={one} run={run} />
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          <Landed rows={[top]} />
          {runs.map((r) => (
            <section key={r.ask.name} aria-label={r.ask.name} className="flex flex-col gap-2 rounded-md border border-border px-3 py-2.5" data-run={r.ask.name}>
              <div className="flex flex-wrap items-center gap-2.5">
                <span className="font-mono text-sm font-semibold text-foreground">{r.ask.name}</span>
                {passed(r) && <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => openServer(r.app)}>{openLive(r.ask.name)}</Button>}
              </div>
              {waits(r) ? (
                <p className={cn(HINT, "m-0")} data-later>{[r.ask.secret ? laterLine(r.ask.name) : "", r.ask.listing ? laterListing(r.ask.name, r.ask.listing) : ""].filter(Boolean).join(" ")}</p>
              ) : (
                <><Landed rows={r.rows} /><Outcome id={id} r={r} run={run} /></>
              )}
              {!run.busy && checkFailed(r) && <div className="flex flex-wrap gap-2"><Doors r={r} run={run} size="sm" /></div>}
            </section>
          ))}
        </div>
      )}
      <DialogFooter>{foot}</DialogFooter>
    </>
  );
}
