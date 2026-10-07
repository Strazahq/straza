import * as React from "react";
import { ChevronRightIcon, CopyIcon, FingerprintIcon, Trash2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { HEAD } from "@/components/data-table";
import { HelpTip } from "@/components/help-tip";
import { WordBadge } from "@/components/users-table";
import { type ApiError, deleteAttestationHash, getConfig, listAttestationHashes } from "@/lib/api";
import { copyText } from "@/lib/clipboard";
import { SUBJECT_CONFIG } from "@/lib/config-words";
import { notify } from "@/lib/notify";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { CANCEL } from "@/lib/role-words";
import { readFailed, refused } from "@/lib/say";
import {
  ALLOWED,
  ALLOWED_TIP,
  COPY_HASH,
  CURRENT,
  DIRTY,
  DIRTY_TIP,
  HASH_COPIED,
  HASH_HELP,
  MANAGED_PATH,
  READING_REGISTRY,
  REGISTRY_ADVISORY_LINE,
  REGISTRY_COLUMN,
  REGISTRY_EMPTY_BODY,
  REGISTRY_EMPTY_TITLE,
  REGISTRY_LEDE_MANAGED,
  REGISTRY_LEDE_NONE,
  REGISTRY_MEASURED,
  REGISTRY_NONE_LINE,
  REGISTRY_NONE_STRIP,
  REGISTRY_VERIFY,
  type Render,
  RETIRE,
  RETIRE_VERB,
  STATUS_HELP,
  SUBJECT_REGISTRY,
  TABS,
  agoWord,
  filePathLine,
  rendersOf,
  rendersWord,
  retireTitle,
  retiredToast,
  retireBody,
  shortHash,
} from "@/lib/settings-words";
import { absTime } from "@/lib/words";

// The Attestation registry tab of Settings: the hook wiring a managed box
// must present at check-in, one row
// per render and banded by artifact, with the floor's own strip while the
// registry decides nothing and Retire at the row end.

type State =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; renders: Render[]; lastRead: Date; problem: string | null };

// FLOOR_ROW is the configuration row the strip's door opens, the address
// Overview uses for the same setting.
const FLOOR_ROW = "floor";

export function RegistryTab() {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [floor, setFloor] = React.useState<string | null>(null);
  const [configProblem, setConfigProblem] = React.useState<string | null>(null);
  const [ask, setAsk] = React.useState<Render | null>(null);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    listAttestationHashes().then(
      (rows) => setState({ kind: "ready", renders: rendersOf(rows || []), lastRead: new Date(), problem: null }),
      (e: ApiError) => {
        if (e.status === 401) return;
        const message = readFailed(SUBJECT_REGISTRY, e);
        setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      },
    );
  }, []);

  React.useEffect(() => {
    load();
    getConfig().then(
      (answer) => setFloor((answer.governance && answer.governance.min_attestation) || "none"),
      (e: ApiError) => { if (e.status !== 401) setConfigProblem(readFailed(SUBJECT_CONFIG, e)); },
    );
  }, [load]);

  const retire = async (render: Render) => {
    setBusy(true);
    setRefusal(null);
    try {
      for (const row of render.rows) await deleteAttestationHash(row.id);
      notify.ok(retiredToast(shortHash(render.hash)));
      setAsk(null);
      load();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal(refused(err));
    } finally {
      setBusy(false);
    }
  };

  if (state.kind === "loading") return <p className="text-sm text-muted-foreground">{READING_REGISTRY}</p>;
  if (state.kind === "error") return <FetchError subject={SUBJECT_REGISTRY} detail={state.message} />;

  const { renders } = state;
  const artifacts = [...new Set(renders.map((r) => r.artifact))];
  const lede = floor === "managed" ? REGISTRY_LEDE_MANAGED : floor === null ? "" : REGISTRY_LEDE_NONE;

  return (
    <div className="flex flex-col gap-4">
      {state.problem && <FetchError subject={SUBJECT_REGISTRY} detail={state.problem} lastRead={state.lastRead} />}
      {configProblem && <FetchError subject={SUBJECT_CONFIG} detail={configProblem} />}
      {(floor === "none" || floor === "advisory") && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-warn/40 bg-warn-bg px-4 py-3 text-sm" data-floor-strip={floor}>
          <b className="font-semibold text-warn">{REGISTRY_NONE_STRIP}</b>
          <span className="max-w-[75ch] text-text-2">{floor === "none" ? REGISTRY_NONE_LINE : REGISTRY_ADVISORY_LINE}</span>
          <FloorDoor />
        </div>
      )}

      <p className="flex max-w-[75ch] flex-wrap items-center gap-1.5 text-sm text-text-2" data-registry-lede>
        {lede ? lede + " " + REGISTRY_VERIFY : REGISTRY_VERIFY}
        <HelpTip label={TABS[1].label} text={REGISTRY_MEASURED} />
      </p>

      {renders.length === 0 ? (
        <EmptyState icon={FingerprintIcon} title={REGISTRY_EMPTY_TITLE}>{REGISTRY_EMPTY_BODY}</EmptyState>
      ) : (
        <div className="overflow-hidden rounded-md border border-border">
          <Table className="table-fixed">
            <colgroup>
              <col style={{ width: "280px" }} />
              <col />
              <col style={{ width: "130px" }} />
              <col style={{ width: "150px" }} />
              <col style={{ width: "56px" }} />
            </colgroup>
            <TableHeader>
              <TableRow>
                <TableHead className={HEAD}><span className="inline-flex items-center gap-1">{REGISTRY_COLUMN.hash}<HelpTip label={REGISTRY_COLUMN.hash} text={HASH_HELP} /></span></TableHead>
                <TableHead className={HEAD}>{REGISTRY_COLUMN.platforms}</TableHead>
                <TableHead className={HEAD}><span className="inline-flex items-center gap-1">{REGISTRY_COLUMN.status}<HelpTip label={REGISTRY_COLUMN.status} text={STATUS_HELP} /></span></TableHead>
                <TableHead className={HEAD}>{REGISTRY_COLUMN.registered}</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {artifacts.map((artifact) => {
                const band = renders.filter((r) => r.artifact === artifact);
                const platforms = band.reduce((n, r) => n + r.platforms.length, 0);
                const path = MANAGED_PATH[artifact];
                return (
                  <React.Fragment key={artifact}>
                    <TableRow className="bg-secondary/60 hover:bg-secondary/60" data-band={artifact}>
                      <TableCell colSpan={5} className="py-2">
                        <span className="flex flex-wrap items-baseline gap-2">
                          <b className="font-mono font-semibold text-foreground">{artifact}</b>
                          <span className="text-[13px] text-muted-foreground">{rendersWord(band.length, platforms)}</span>
                          {path && <span className="text-[13px] text-muted-foreground">{filePathLine(path)}</span>}
                        </span>
                      </TableCell>
                    </TableRow>
                    {band.map((r) => <Row key={r.hash} render={r} onRetire={() => { setRefusal(null); setAsk(r); }} />)}
                  </React.Fragment>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}

      <AlertDialog open={ask !== null} onOpenChange={(o) => { if (!o && !busy) { setAsk(null); setRefusal(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle className="font-mono">{retireTitle(shortHash(ask ? ask.hash : ""))}</AlertDialogTitle>
            <AlertDialogDescription>{retireBody(ask ? ask.platforms.length : 0)}</AlertDialogDescription>
          </AlertDialogHeader>
          {refusal && <RefusedError subject={RETIRE_VERB} message={refusal} />}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-danger text-white hover:bg-danger/90"
              onClick={(e) => { e.preventDefault(); if (ask) void retire(ask); }}
            >
              {RETIRE_VERB}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// FloorDoor opens the configuration row that sets the floor, at the
// address Overview links to, so the strip ends where the change is made.
function FloorDoor() {
  const href = pathFor("settings", ["configuration"]) + "#" + FLOOR_ROW;
  return (
    <a
      href={href}
      className="ml-auto inline-flex items-center gap-1 text-sm text-link underline-offset-4 hover:underline"
      onClick={(e) => {
        if (!isPlainClick(e)) return;
        e.preventDefault();
        window.history.pushState(null, "", href);
        navigate("settings", ["configuration"], true);
      }}
    >
      {TABS[0].label}
      <ChevronRightIcon className="size-3.5" aria-hidden="true" />
    </a>
  );
}

// Row is one render: the hash it registers, the platforms it covers, what
// the server thinks of it now, when it was registered, and Retire.
function Row({ render, onRetire }: { render: Render; onRetire: () => void }) {
  const copy = () => copyText(render.hash, HASH_COPIED);
  return (
    <TableRow data-render={render.hash}>
      <TableCell>
        <span className="flex items-center gap-1">
          <span className="truncate font-mono text-[13px] text-foreground" title={render.hash}>{shortHash(render.hash)}</span>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button variant="ghost" size="icon-xs" aria-label={COPY_HASH} onClick={copy}><CopyIcon /></Button>
            </TooltipTrigger>
            <TooltipContent>{COPY_HASH}</TooltipContent>
          </Tooltip>
        </span>
      </TableCell>
      <TableCell>
        <span className="flex flex-wrap gap-1">
          {render.platforms.map((p) => <WordBadge key={p} word={p} tone="plain" mono attr="data-platform" />)}
        </span>
      </TableCell>
      <TableCell>
        <span className="flex flex-wrap items-center gap-1">
          {render.current
            ? <WordBadge word={CURRENT} tone="ok" mono attr="data-render-status" />
            : <WordBadge word={ALLOWED} tone="plain" mono title={ALLOWED_TIP} attr="data-render-status" />}
          {render.dirty && <WordBadge word={DIRTY} tone="warn" title={DIRTY_TIP} attr="data-dirty" />}
        </span>
      </TableCell>
      <TableCell>
        <span className="flex flex-col">
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="cursor-default text-text-2">{agoWord(render.created_at)}</span>
            </TooltipTrigger>
            <TooltipContent>{absTime(render.created_at)}</TooltipContent>
          </Tooltip>
          {!render.current && render.note && <span className="truncate text-[13px] text-muted-foreground" title={render.note}>{render.note}</span>}
        </span>
      </TableCell>
      <TableCell className="text-right">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="ghost" size="icon-xs" aria-label={RETIRE} onClick={onRetire}><Trash2Icon /></Button>
          </TooltipTrigger>
          <TooltipContent>{RETIRE}</TooltipContent>
        </Tooltip>
      </TableCell>
    </TableRow>
  );
}
