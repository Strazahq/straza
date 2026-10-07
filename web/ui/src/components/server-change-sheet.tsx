import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { CAPS, ERROR, HINT, ManifestFold } from "@/components/wizard/parts";
import { type Dry, useDryRun } from "@/components/wizard/use-dry-run";
import { SaveNoteLine, useDraftSave, useWorkingDraft } from "@/components/use-draft-save";
import { type ApiError, type AppRow, type ManifestDoc, type ProviderRow, listApps, listProviders } from "@/lib/api";
import { DRY_AS_YOU_TYPE, DRY_PENDING, DRY_UNREACHABLE, NOTHING_TO_SAVE, NOTHING_YET, NO_MANIFEST, READ_UNREACHABLE, SHEET_SUB, SHEET_TITLE, dryAccepted, gone, pausedWords, readRefused, reading, whenWords } from "@/lib/change-words";
import { toYAML } from "@/lib/manifest";
import { type ChangeRow, type Connection, type CredentialEdit, type Settings, changeRows, editConnection, editCredential, editSettings, readConnection, readCredential, readSettings } from "@/lib/manifest-edit";
import { version } from "@/lib/public";
import { SAVE_DRAFT, SAVE_PUBLISH, workingHolds } from "@/lib/save-words";
import { cn } from "@/lib/utils";
import { ConnectionFields, connectionMisses } from "./server-change-connection";
import { CredentialFields, credentialMisses } from "./server-change-credential";
import { SettingsFields, settingsMisses } from "./server-change-settings";

// ChangeWhich names the Overview card a Change sheet edits.
export type ChangeWhich = "connection" | "credential" | "settings";

// ChangeSheetProps is what the server page hands a Change sheet: the
// server as listed, which card is open (null when none), the server's
// current tool names for the exposure picker, the server-wide per-call
// timeout from the config when it answered, the close callback, and the
// callback after a publish landed.
export type ChangeSheetProps = {
  app: AppRow;
  which: ChangeWhich | null;
  tools: string[];
  upstreamTimeout: number | null;
  onClose: () => void;
  onSaved: () => void;
};

// ChangeSheet is the sheet a Change button on a server's Overview opens:
// only that card's fields, prefilled from the installed manifest, what
// changes as you type, the server's own check, what publishing does, and
// the app.yaml it sends. Save draft adds the edited manifest to the
// person's working draft, and Save and publish publishes it under the same
// name. A publish that lands calls onSaved, then
// onClose. The sheet does not close while a save is in flight.
export function ChangeSheet(props: ChangeSheetProps) {
  const { which, onClose } = props;
  const [busy, setBusy] = React.useState(false);
  return (
    <Sheet open={!!which} onOpenChange={(open) => { if (!open && !busy) onClose(); }}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-[560px]" data-change-sheet={which || undefined}>
        {which && <ChangeBody key={which} {...props} which={which} busy={busy} setBusy={setBusy} />}
      </SheetContent>
    </Sheet>
  );
}

type BodyProps = ChangeSheetProps & { which: ChangeWhich; busy: boolean; setBusy: (busy: boolean) => void };

// Read is the installed manifest as this sheet got it from the server:
// still being read, read with the tool list to pick from, read but the row
// carries no manifest, the row gone, or not read at all.
type Read =
  | { state: "loading" }
  | { state: "ready"; base: ManifestDoc; tools: string[]; short: boolean }
  | { state: "bare" }
  | { state: "gone" }
  | { state: "failed"; text: string };

// ChangeBody reads the server's own row as the sheet opens and edits that
// manifest, never the copy the page holds: a page whose reload after an
// earlier save is still in flight or failed would otherwise hand this
// sheet a stale manifest, and saving it would undo that save.
function ChangeBody({ app, which, tools, upstreamTimeout, onClose, onSaved, busy, setBusy }: BodyProps) {
  const [toolsAt] = React.useState(tools);
  const [read, setRead] = React.useState<Read>({ state: "loading" });

  React.useEffect(() => {
    let alive = true;
    listApps().then(
      (rows) => {
        if (!alive) return;
        const fresh = (rows || []).find((r) => r.id === app.id);
        if (!fresh) { setRead({ state: "gone" }); return; }
        if (!fresh.manifest) { setRead({ state: "bare" }); return; }
        // The server lists what it offers only while an instance runs, so
        // without that list the picker falls back to the names the page
        // knows and says why it may be short.
        const offered = Array.isArray(fresh.offered) ? fresh.offered : null;
        setRead({ state: "ready", base: fresh.manifest, tools: offered || (toolsAt.length ? toolsAt : fresh.tools || []), short: !offered });
      },
      (e) => {
        const err = e as ApiError;
        if (alive) setRead({ state: "failed", text: err.unreachable ? READ_UNREACHABLE : readRefused(err.message) });
      },
    );
    return () => { alive = false; };
  }, [app.id, toolsAt]);

  const head = (
    <SheetHeader className="border-b border-border pr-12">
      <SheetTitle className="text-lg leading-snug">{SHEET_TITLE[which]}<code className="font-mono">{app.name}</code></SheetTitle>
      <SheetDescription>{SHEET_SUB[which]}</SheetDescription>
    </SheetHeader>
  );

  if (read.state === "ready") {
    return (
      <>
        {head}
        <ChangeForm app={app} which={which} base={read.base} tools={read.tools} short={read.short}
          upstreamTimeout={upstreamTimeout} onClose={onClose} onSaved={onSaved} busy={busy} setBusy={setBusy} />
      </>
    );
  }
  const say = read.state === "loading" ? reading(app.name) : read.state === "bare" ? NO_MANIFEST : read.state === "gone" ? gone(app.name) : read.text;
  return (
    <>
      {head}
      <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4" data-change-body>
        <p role="status" className={cn(HINT, "max-w-[75ch]")} data-change-say={read.state}>{say}</p>
      </div>
      <SheetFooter className="mt-0 border-t border-border">
        <div className="flex items-center justify-end gap-2">
          <Button variant="outline" onClick={onClose}>{read.state === "loading" ? "Cancel" : "Close"}</Button>
          {read.state === "loading" && (
            <Button aria-disabled="true" title={reading(app.name)} className="aria-disabled:cursor-not-allowed aria-disabled:opacity-50" data-change-save>
              {SAVE_PUBLISH}
            </Button>
          )}
        </div>
      </SheetFooter>
    </>
  );
}

type FormProps = {
  app: AppRow;
  which: ChangeWhich;
  base: ManifestDoc;
  tools: string[];
  short: boolean;
  upstreamTimeout: number | null;
  onClose: () => void;
  onSaved: () => void;
  busy: boolean;
  setBusy: (busy: boolean) => void;
};

function ChangeForm({ app, which, base, tools, short, upstreamTimeout, onClose, onSaved, busy, setBusy }: FormProps) {
  const [doc] = React.useState<ManifestDoc>(() => JSON.parse(JSON.stringify(base)) as ManifestDoc);
  const [conn, setConn] = React.useState(() => readConnection(doc));
  const [cred, setCred] = React.useState(() => readCredential(doc));
  const [sets, setSets] = React.useState(() => readSettings(doc, tools));
  const [runtimes, setRuntimes] = React.useState<string[] | null>(null);
  const [providers, setProviders] = React.useState<ProviderRow[] | null | undefined>(undefined);
  const [asked, setAsked] = React.useState(false);
  const refusal = React.useRef<HTMLParagraphElement>(null);
  const body = React.useRef<HTMLDivElement>(null);

  React.useEffect(() => {
    let alive = true;
    if (which === "connection") version().then((v) => { if (alive) setRuntimes(v && Array.isArray(v.runtimes) ? v.runtimes : null); }, () => undefined);
    if (which === "credential") listProviders().then((p) => { if (alive) setProviders(Array.isArray(p) ? p : null); }, () => { if (alive) setProviders(null); });
    return () => { alive = false; };
  }, [which]);

  // The fields arrive after the manifest is read, so the first text field
  // takes the focus here: a person who opened Connection to fix an address
  // starts typing in it. The sheet reclaims the focus it lost when the
  // waiting footer went away, so this waits for that to happen first.
  React.useEffect(() => {
    const t = setTimeout(() => body.current?.querySelector<HTMLElement>("input:not([type=checkbox]), textarea")?.focus(), 0);
    return () => clearTimeout(t);
  }, []);

  const after = which === "connection" ? editConnection(doc, conn) : which === "credential" ? editCredential(doc, cred) : editSettings(doc, sets, tools);
  const misses = which === "connection" ? connectionMisses(doc, conn) : which === "credential" ? credentialMisses(cred) : settingsMisses(sets, tools, upstreamTimeout);
  const rows = changeRows(doc, after, upstreamTimeout);
  const yaml = toYAML(after);
  const dry = useDryRun(yaml, rows.length > 0 && misses.length === 0 ? "live" : "off");
  const paused = !!app.paused;
  const paras = whenWords({ name: app.name, paused, which, before: doc, after, changed: rows.length > 0 });
  const held = useWorkingDraft("App/" + app.name);
  const save = useDraftSave({ name: app.name, toast: paused ? pausedWords(app.name) : undefined, onPublished: () => { onSaved(); onClose(); } });
  React.useEffect(() => { setBusy(save.busy !== null); }, [save.busy, setBusy]);

  const patchConn = (p: Partial<Connection>) => { setAsked(false); setConn((c) => ({ ...c, ...p })); };
  const patchCred = (p: Partial<CredentialEdit>) => { setAsked(false); setCred((c) => ({ ...c, ...p })); };
  const patchSets = (p: Partial<Settings>) => { setAsked(false); setSets((s) => ({ ...s, ...p })); };

  // send keeps both buttons clickable, and a click
  // that cannot save says why at the spot that needs the fix. Both send the
  // one App put, built from the manifest the sheet read live.
  const send = (publish: boolean) => {
    if (busy) return;
    if (misses.length) { document.getElementById(misses[0].id)?.focus(); return; }
    if (rows.length === 0) { setAsked(true); return; }
    if (dry.state === "refused") { refusal.current?.scrollIntoView({ block: "center" }); return; }
    const items = [{ kind: "App" as const, name: app.name, op: "put" as const, doc: yaml }];
    void (publish ? save.saveAndPublish(items) : save.saveDraft(items));
  };

  let fields: React.ReactNode = null;
  if (which === "connection") fields = <ConnectionFields name={app.name} base={doc} c={conn} set={patchConn} misses={misses} runtimes={runtimes} />;
  else if (which === "credential") fields = <CredentialFields base={doc} c={cred} set={patchCred} misses={misses} providers={providers} />;
  else fields = <SettingsFields name={app.name} s={sets} set={patchSets} misses={misses} tools={tools} short={short} upstreamTimeout={upstreamTimeout} />;

  return (
    <>
      <div ref={body} className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4" data-change-body>
        {fields}
        <section className="flex flex-col gap-1.5">
          <span className={CAPS}>What changes</span>
          <Changes rows={rows} />
          <DryLine dry={dry} refusal={refusal} />
        </section>
        <section className="flex flex-col gap-1.5">
          <span className={CAPS}>When you publish</span>
          <div className="flex max-w-[75ch] flex-col gap-1.5 text-sm leading-relaxed text-text-2" data-when-save>
            {paras.map((p) => <p key={p} className="m-0">{p}</p>)}
          </div>
        </section>
        <ManifestFold yaml={yaml} />
      </div>
      <SheetFooter className="mt-0 border-t border-border">
        {save.note && <SaveNoteLine note={save.note} />}
        {held && <p className={cn(HINT, "m-0 max-w-[75ch]")} data-working-holds>{workingHolds(held, app.name)}</p>}
        {asked && rows.length === 0 && <p role="status" className={cn(ERROR, "m-0 text-right")} data-nothing-to-save>{NOTHING_TO_SAVE}</p>}
        <div className="flex items-center justify-end gap-2">
          <Button variant="outline" onClick={onClose} disabled={busy}>Cancel</Button>
          <Button variant="outline" onClick={() => send(false)} disabled={busy} data-change-draft>
            {save.busy === "draft" && <Loader2Icon className="animate-spin" />}
            {SAVE_DRAFT}
          </Button>
          <Button onClick={() => send(true)} disabled={busy} data-change-save>
            {save.busy === "publish" && <Loader2Icon className="animate-spin" />}
            {SAVE_PUBLISH}
          </Button>
        </div>
      </SheetFooter>
      {save.dialog}
    </>
  );
}

// Changes is "What changes": each changed field, now and after publishing.
function Changes({ rows }: { rows: ChangeRow[] }) {
  if (rows.length === 0) return <p className="m-0 rounded-md border border-dashed border-border px-3 py-2 text-[13px] text-muted-foreground" data-change-rows>{NOTHING_YET}</p>;
  const cell = "px-3 py-1.5 align-top break-words";
  return (
    <div className="overflow-x-auto rounded-md border border-border" data-change-rows>
      <table className="w-full table-fixed text-sm">
        <colgroup><col className="w-[28%]" /><col className="w-[36%]" /><col className="w-[36%]" /></colgroup>
        <thead>
          <tr className="border-b border-border text-left text-[13px] text-muted-foreground">
            <th className={cn(cell, "font-medium")}><span className="sr-only">Field</span></th>
            <th className={cn(cell, "font-medium")}>Now</th>
            <th className={cn(cell, "font-medium")}>After publishing</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r[0]} className="border-b border-border last:border-0">
              <td className={cn(cell, "text-foreground")}>{r[0]}</td>
              <td className={cn(cell, "text-muted-foreground")}>{r[1]}</td>
              <td className={cn(cell, "text-foreground")}>{r[2]}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// DryLine is the server's own check of the edited manifest, as you type.
function DryLine({ dry, refusal }: { dry: Dry; refusal: React.Ref<HTMLParagraphElement> }) {
  if (dry.state === "ok") {
    return (
      <p role="status" className={cn(HINT, "m-0")} data-dry-run="ok">
        <span className="text-ok">{dryAccepted(dry.runtime, dry.credential)}</span>{DRY_AS_YOU_TYPE}
      </p>
    );
  }
  if (dry.state === "refused") return <p ref={refusal} role="status" className={cn(ERROR, "m-0")} data-dry-run="refused">{dry.text}</p>;
  if (dry.state === "unreachable") return <p role="status" className="m-0 text-[13px] leading-snug text-unknown" data-dry-run="unreachable">{DRY_UNREACHABLE}</p>;
  if (dry.state === "pending") return <p role="status" className={cn(HINT, "m-0")} data-dry-run="pending">{DRY_PENDING}</p>;
  return null;
}
