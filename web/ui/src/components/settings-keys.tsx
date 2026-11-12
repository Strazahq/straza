import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { KeyRoundIcon, Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { DataTable, plain } from "@/components/data-table";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { RetiringPanel, StagedPanel } from "@/components/settings-keys-panel";
import { WordBadge } from "@/components/users-table";
import type { ApiError } from "@/lib/api";
import {
  ASSERTION_PURPOSE, type SigningKeyRow, assertionKeyCertificate, entraBlueprints, listSigningKeys, promoteAssertionKey, retireAssertionKey, rotateAssertionKey, thumbprintOf, usesEntra,
} from "@/lib/keys-api";
import {
  CERTIFICATE, CREATE_BODY, CREATE_NEW_KEY, CREATE_TITLE, HIDE_RETIRED, KEYS_EMPTY_TITLE, KEY_COLUMN, PROMOTE, PROMOTE_BODY, READING_KEYS, RETIRE, RETIRE_ACTIVE_BODY,
  RETIRE_ENTRA, RETIRE_STAGED_BODY, RETIRED_LINE, ROTATE_TITLE, SUBJECT_KEYS, certFile, downloadedToast, keysCount, keysEmptyBody, keysLede, promotePlainBody, promoteTick, promoteTitle, promotedToast,
  retireTitle, retiredToast, rotateBody, shortKid, showRetired, stagedToast,
} from "@/lib/keys-words";
import { notify } from "@/lib/notify";
import { CANCEL } from "@/lib/role-words";
import { navigate } from "@/lib/router";
import { readFailed, refused } from "@/lib/say";
import { downloadText } from "@/lib/utils";
import { absTime } from "@/lib/words";

// The Agent signing keys tab of Settings: the client assertion keys, the
// rotation panel while a rotation runs, and the three writes, each behind
// a confirm. Only a full administrator sees the tab, because the server
// takes every write from one only. The certificate and every Entra step
// show only when a provider uses Entra, or when the providers could not
// be read.

const DANGER_BUTTON = "border-danger text-danger hover:bg-danger-bg hover:text-danger";
const WIDTHS: Record<string, string> = { kid: "130px", status: "110px", created: "170px", until: "130px" };
const LABELS: Record<string, string> = { kid: KEY_COLUMN.kid, status: KEY_COLUMN.status, created: KEY_COLUMN.created, until: KEY_COLUMN.until };
const TONE: Record<string, "ok" | "warn" | "plain"> = { active: "ok", staged: "warn" };

type State =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; rows: SigningKeyRow[]; lastRead: Date; problem: string | null };

type Ask = { kind: "rotate" } | { kind: "promote"; key: SigningKeyRow } | { kind: "retire"; key: SigningKeyRow };

type Cert = { pem: string; thumb: string | null };

type Props = {
  // rotateRequest is the page head's counter: Rotate key moves it, and the
  // tab opens its confirm when it moves.
  rotateRequest: number;
  // onRows hands each fresh read to the page, which counts the keys and
  // picks the head's action from them.
  onRows: (rows: SigningKeyRow[]) => void;
};

export function KeysTab({ rotateRequest, onRows }: Props) {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [certs, setCerts] = React.useState<Record<string, Cert>>({});
  const [downloaded, setDownloaded] = React.useState<Set<string>>(new Set());
  const [removed, setRemoved] = React.useState<Set<string>>(new Set());
  const [leaves, setLeaves] = React.useState<number | null>(null);
  const [ask, setAsk] = React.useState<Ask | null>(null);
  const [ticked, setTicked] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [retiredOpen, setRetiredOpen] = React.useState(false);
  const [blueprints, setBlueprints] = React.useState<string[] | null | undefined>(undefined);

  React.useEffect(() => {
    let alive = true;
    void entraBlueprints().then((b) => { if (alive) setBlueprints(b); });
    return () => { alive = false; };
  }, []);
  const known = blueprints === undefined ? null : blueprints;
  const entra = usesEntra(known);

  const load = React.useCallback(() => {
    listSigningKeys().then(
      (all) => {
        const rows = (all || []).filter((k) => k.purpose === ASSERTION_PURPOSE);
        setState({ kind: "ready", rows, lastRead: new Date(), problem: null });
        onRows(rows);
      },
      (e: ApiError) => {
        if (e.status === 401) return;
        const message = readFailed(SUBJECT_KEYS, e);
        setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      },
    );
  }, [onRows]);

  React.useEffect(() => { load(); }, [load]);

  const seen = React.useRef(rotateRequest);
  React.useEffect(() => {
    if (seen.current === rotateRequest) return;
    seen.current = rotateRequest;
    setRefusal(null);
    setAsk({ kind: "rotate" });
  }, [rotateRequest]);

  const rows = state.kind === "ready" ? state.rows : [];
  const staged = rows.find((k) => k.status === "staged") || null;
  const active = rows.find((k) => k.status === "active") || null;
  const retiring = rows.find((k) => k.status === "retiring") || null;
  const retired = rows.filter((k) => k.status === "retired");

  // While a rotation runs, the list is read again every 30 seconds, so the
  // previous key's retirement and a peer administrator's step show up
  // without a reload.
  const rotating = !!(staged || retiring);
  React.useEffect(() => {
    if (!rotating) return;
    const t = setInterval(load, 30_000);
    return () => clearInterval(t);
  }, [rotating, load]);

  // The certificate is fetched once per key, and its thumbprint with it,
  // for the key a rotation panel shows.
  const fetchCert = React.useCallback(async (kid: string): Promise<Cert | null> => {
    try {
      const pem = await assertionKeyCertificate(kid);
      const cert = { pem, thumb: await thumbprintOf(pem) };
      setCerts((c) => ({ ...c, [kid]: cert }));
      return cert;
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setProblem(refused(err));
      return null;
    }
  }, []);
  const panelKid = staged ? staged.kid : retiring ? retiring.kid : "";
  React.useEffect(() => {
    if (blueprints !== undefined && entra && panelKid && !certs[panelKid]) void fetchCert(panelKid);
  }, [blueprints, entra, panelKid, certs, fetchCert]);

  // The table's buttons read the cache through a ref, so a certificate
  // that lands does not rebuild the table's rows under a click.
  const certsRef = React.useRef(certs);
  React.useEffect(() => { certsRef.current = certs; }, [certs]);
  const download = React.useCallback(async (kid: string) => {
    setProblem(null);
    const cert = certsRef.current[kid] || (await fetchCert(kid));
    if (!cert) return;
    downloadText(certFile(kid), cert.pem, "application/x-pem-file");
    setDownloaded((d) => new Set(d).add(kid));
    notify.ok(downloadedToast(kid));
  }, [fetchCert]);

  const run = async (fn: () => Promise<string>) => {
    if (busy) return;
    setBusy(true);
    setRefusal(null);
    try {
      notify.ok(await fn());
      setAsk(null);
      load();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal(refused(err));
    } finally {
      setBusy(false);
    }
  };
  const confirm = () => {
    if (!ask) return;
    if (ask.kind === "rotate") void run(async () => stagedToast((await rotateAssertionKey()).kid));
    if (ask.kind === "promote") void run(async () => {
      const got = await promoteAssertionKey(ask.key.kid);
      setLeaves(got.previous_key_leaves_document_in_seconds ?? null);
      return promotedToast(ask.key.kid);
    });
    if (ask.kind === "retire") void run(async () => { await retireAssertionKey(ask.key.kid); return retiredToast(ask.key.kid); });
  };
  const askFor = React.useCallback((next: Ask) => { setRefusal(null); setTicked(false); setAsk(next); }, []);

  const columns = React.useMemo<ColumnDef<SigningKeyRow>[]>(() => [
    { id: "kid", header: plain<SigningKeyRow>(KEY_COLUMN.kid), cell: ({ row }) => <span className="font-mono text-[13px]" title={row.original.kid}>{shortKid(row.original.kid)}</span> },
    { id: "status", header: plain<SigningKeyRow>(KEY_COLUMN.status), cell: ({ row }) => <WordBadge word={row.original.status} tone={TONE[row.original.status] || "plain"} mono attr="data-key-status" /> },
    { id: "created", header: plain<SigningKeyRow>(KEY_COLUMN.created), cell: ({ row }) => <span className="tabular-nums text-text-2">{absTime(row.original.created_at)}</span> },
    ...(entra ? [{ id: "until", header: plain<SigningKeyRow>(KEY_COLUMN.until), cell: ({ row }: { row: { original: SigningKeyRow } }) => <span className="tabular-nums text-text-2">{row.original.certificate_expires_at ? absTime(row.original.certificate_expires_at).slice(0, 10) : "none"}</span> }] : []),
    {
      id: "actions",
      header: plain<SigningKeyRow>(""),
      enableHiding: false,
      cell: ({ row }) => {
        const k = row.original;
        if (k.status === "retired") return null;
        return (
          <span className="flex justify-end gap-1.5">
            {entra && <Button variant="outline" size="sm" aria-label={CERTIFICATE + " " + shortKid(k.kid)} onClick={() => void download(k.kid)}>{CERTIFICATE}</Button>}
            {k.status !== "retiring" && (
              <Button variant="outline" size="sm" className={DANGER_BUTTON} aria-label={RETIRE + " " + shortKid(k.kid)} onClick={() => askFor({ kind: "retire", key: k })}>{RETIRE}</Button>
            )}
          </span>
        );
      },
    },
  ], [download, askFor, entra]);

  if (state.kind === "loading" || blueprints === undefined) return <p className="text-sm text-muted-foreground">{READING_KEYS}</p>;
  if (state.kind === "error") return <FetchError subject={SUBJECT_KEYS} detail={state.message} />;

  const live = rows.filter((k) => k.status !== "retired");
  const thumbOf = (kid: string) => (certs[kid] ? certs[kid].thumb : undefined);
  const dialog = ask && words(ask, active, entra);

  return (
    <div className="flex flex-col gap-4">
      {state.problem && <FetchError subject={SUBJECT_KEYS} detail={state.problem} lastRead={state.lastRead} />}
      {problem && <RefusedError subject={CERTIFICATE} message={problem} />}
      <p className="max-w-[75ch] text-sm text-text-2" data-keys-lede>{keysLede(entra)}</p>

      {staged && (
        <StagedPanel staged={staged} active={active} blueprints={known} thumb={thumbOf(staged.kid)} downloaded={downloaded.has(staged.kid)}
          onDownload={() => void download(staged.kid)} onPromote={() => askFor({ kind: "promote", key: staged })} />
      )}
      {!staged && retiring && (
        <RetiringPanel active={active} retiring={retiring} blueprints={known} thumb={thumbOf(retiring.kid)} leaves={leaves} removed={removed.has(retiring.kid)}
          onRemoved={() => setRemoved((r) => new Set(r).add(retiring.kid))} onUsers={() => navigate("users")} />
      )}

      {rows.length === 0 ? (
        <EmptyState icon={KeyRoundIcon} title={KEYS_EMPTY_TITLE}>{keysEmptyBody(known)}</EmptyState>
      ) : (
        <DataTable
          rows={retiredOpen ? rows : live}
          columns={columns}
          labels={LABELS}
          rowKey={(k) => k.kid}
          rowName={(k) => k.kid}
          dataAttr="data-key"
          count={(shown) => keysCount(shown)}
          emptyText={keysEmptyBody(known)}
          widths={WIDTHS}
          rowClassName={(k) => (k.status === "retired" ? "text-muted-foreground" : "")}
        />
      )}
      {retired.length > 0 && (
        <div className="flex flex-col items-start gap-1">
          <Button variant="link" size="sm" className="h-auto p-0 text-[13px] text-link" onClick={() => setRetiredOpen((o) => !o)}>
            {retiredOpen ? HIDE_RETIRED : showRetired(retired.length)}
          </Button>
          {retiredOpen && entra && <p className="text-[13px] text-muted-foreground">{RETIRED_LINE}</p>}
        </div>
      )}

      <AlertDialog open={ask !== null} onOpenChange={(o) => { if (!o && !busy) { setAsk(null); setRefusal(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          {dialog && (
            <>
              <AlertDialogHeader>
                <AlertDialogTitle>{dialog.title}</AlertDialogTitle>
                <AlertDialogDescription asChild>
                  <div className="flex flex-col gap-2 text-sm text-muted-foreground">{dialog.body.map((b) => <p key={b}>{b}</p>)}</div>
                </AlertDialogDescription>
              </AlertDialogHeader>
              {ask.kind === "promote" && entra && (
                <label className="flex items-start gap-2 text-sm">
                  <input type="checkbox" className="mt-1" checked={ticked} onChange={(e) => setTicked(e.target.checked)} data-promote-tick />
                  <span>{promoteTick(thumbOf(ask.key.kid) || null, known)}</span>
                </label>
              )}
              {refusal && <RefusedError subject={dialog.verb} message={refusal} />}
              <AlertDialogFooter>
                <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
                <AlertDialogAction
                  className={dialog.danger ? "bg-danger text-white hover:bg-danger/90" : undefined}
                  disabled={ask.kind === "promote" && entra && !ticked}
                  aria-busy={busy || undefined}
                  onClick={(e) => { e.preventDefault(); confirm(); }}
                >
                  {busy && <Loader2Icon className="animate-spin" />} {dialog.verb}
                </AlertDialogAction>
              </AlertDialogFooter>
            </>
          )}
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// words is each confirm's title, body, verb and tone. entra keeps the
// sentences about the blueprint.
function words(ask: Ask, active: SigningKeyRow | null, entra: boolean): { title: string; body: string[]; verb: string; danger: boolean } {
  switch (ask.kind) {
    case "rotate":
      return active
        ? { title: ROTATE_TITLE, body: [rotateBody(active.kid)], verb: CREATE_NEW_KEY, danger: false }
        : { title: CREATE_TITLE, body: [CREATE_BODY], verb: CREATE_NEW_KEY, danger: false };
    case "promote":
      return { title: promoteTitle(ask.key.kid), body: [entra ? PROMOTE_BODY : promotePlainBody(active ? active.kid : null)], verb: PROMOTE, danger: false };
    case "retire": {
      const body = [ask.key.status === "staged" ? RETIRE_STAGED_BODY : RETIRE_ACTIVE_BODY];
      return { title: retireTitle(ask.key.kid), body: entra ? [...body, RETIRE_ENTRA] : body, verb: RETIRE, danger: true };
    }
  }
}
