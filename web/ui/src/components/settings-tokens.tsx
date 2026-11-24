import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { KeyRoundIcon, Loader2Icon, PlusIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button, DANGER_FILL } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Chip } from "@/components/chip";
import { DataTable, plain } from "@/components/data-table";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { KindTile } from "@/components/kind-icon";
import { MintSheet } from "@/components/settings-mint-sheet";
import { GrantChips, TokenSheet } from "@/components/settings-token-sheet";
import { type ApiError, type ApiTokenRow, listApiTokens, revokeApiToken } from "@/lib/api";
import { notify } from "@/lib/notify";
import { CANCEL } from "@/lib/role-words";
import { readFailed, refused } from "@/lib/say";
import {
  GRANTS_HELP,
  LAST_USED_HELP,
  NEVER,
  NEVER_TIP,
  NEW_TOKEN,
  READING_TOKENS,
  REVOKE,
  REVOKE_HELP,
  REVOKE_VERB,
  SUBJECT_TOKENS,
  TOKENS_EMPTY_BODY,
  TOKENS_EMPTY_TITLE,
  TOKENS_LEDE,
  TOKEN_COLUMN,
  byWord,
  expiryOf,
  revokeName,
  revokeTitle,
  revokeBody,
  revokedToast,
  usedWord,
} from "@/lib/settings-words";
import { absTime, dayWord } from "@/lib/words";

// The API tokens tab of Settings: the
// credentials automation signs in with, one row each with the grants in
// words, the token's own sheet behind a row, the mint sheet the tab's own
// button opens, and Revoke at the row end.

const WIDTHS: Record<string, string> = { name: "240px", created: "110px", expires: "180px", used: "110px", revoke: "96px" };
// MIN_WIDTH is the fixed columns and 220 px for the scopes.
const MIN_WIDTH = "956px";
const LABELS: Record<string, string> = {
  name: TOKEN_COLUMN.name,
  grants: TOKEN_COLUMN.grants,
  created: TOKEN_COLUMN.created,
  expires: TOKEN_COLUMN.expires,
  used: TOKEN_COLUMN.used,
};

type State =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; rows: ApiTokenRow[]; lastRead: Date; problem: string | null };

export function TokensTab() {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [open, setOpen] = React.useState<ApiTokenRow | null>(null);
  const [ask, setAsk] = React.useState<ApiTokenRow | null>(null);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [minting, setMinting] = React.useState(false);

  const load = React.useCallback(() => {
    listApiTokens().then(
      (rows) => setState({ kind: "ready", rows: rows || [], lastRead: new Date(), problem: null }),
      (e: ApiError) => {
        if (e.status === 401) return;
        const message = readFailed(SUBJECT_TOKENS, e);
        // A failed read keeps the last table on screen behind the sentence.
        setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      },
    );
  }, []);

  React.useEffect(() => { load(); }, [load]);

  const revoke = async (row: ApiTokenRow) => {
    setBusy(true);
    setRefusal(null);
    try {
      await revokeApiToken(row.id);
      notify.ok(revokedToast(row.name));
      setAsk(null);
      load();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal(refused(err));
    } finally {
      setBusy(false);
    }
  };

  const columns = React.useMemo<ColumnDef<ApiTokenRow>[]>(() => [
    {
      id: "name",
      header: plain<ApiTokenRow>(TOKEN_COLUMN.name),
      cell: ({ row }) => (
        <span className="flex min-w-0 items-center gap-2">
          <KindTile kind="api-token" />
          <span className="flex min-w-0 flex-wrap items-baseline gap-x-1.5">
            <span className="font-mono break-all text-foreground">{row.original.name}</span>
            {/* A long minter name is cut inside the cell, with the whole of it on hover. */}
            {row.original.created_by && <span className="max-w-full truncate text-caption text-muted-foreground" title={byWord(row.original.created_by)}>{byWord(row.original.created_by)}</span>}
          </span>
        </span>
      ),
    },
    {
      id: "grants",
      header: plain<ApiTokenRow>(TOKEN_COLUMN.grants, GRANTS_HELP),
      cell: ({ row }) => <GrantChips scope={row.original.scope} />,
    },
    {
      id: "created",
      header: plain<ApiTokenRow>(TOKEN_COLUMN.created),
      cell: ({ row }) => <Stamp iso={row.original.created} text={dayWord(row.original.created)} />,
    },
    {
      id: "expires",
      header: plain<ApiTokenRow>(TOKEN_COLUMN.expires),
      cell: ({ row }) => <Expires iso={row.original.expires} />,
    },
    {
      id: "used",
      header: plain<ApiTokenRow>(TOKEN_COLUMN.used, LAST_USED_HELP),
      cell: ({ row }) => (row.original.lastUsed
        ? <Stamp iso={row.original.lastUsed} text={usedWord(row.original.lastUsed)} />
        : <span className="text-muted-foreground" data-last-used={NEVER}>{NEVER}</span>),
    },
    {
      id: "revoke",
      header: plain<ApiTokenRow>(""),
      enableHiding: false,
      cell: ({ row }) => (
        <span className="flex justify-end">
          <Button
            variant="ghost"
            size="sm"
            aria-label={revokeName(row.original.name)}
            onClick={(e) => { e.stopPropagation(); setRefusal(null); setAsk(row.original); }}
          >
            {REVOKE}
          </Button>
        </span>
      ),
    },
  ], []);

  // The first line and its button show in every state, so a token can be
  // minted while the list is still read or could not be read.
  const rows = state.kind === "ready" ? state.rows : [];

  return (
    <div className="flex flex-col gap-4">
      {state.kind === "ready" && state.problem && <FetchError subject={SUBJECT_TOKENS} detail={state.problem} lastRead={state.lastRead} />}
      <div className="flex flex-wrap items-center gap-3">
        <p className="max-w-[75ch] flex-1 basis-[280px] text-dense text-text-2" data-tokens-lede>{TOKENS_LEDE}</p>
        <Button className="ml-auto" onClick={() => setMinting(true)}><PlusIcon /> {NEW_TOKEN}</Button>
      </div>

      {state.kind === "loading" ? (
        <p className="text-dense text-muted-foreground">{READING_TOKENS}</p>
      ) : state.kind === "error" ? (
        <FetchError subject={SUBJECT_TOKENS} detail={state.message} />
      ) : rows.length === 0 ? (
        <EmptyState icon={KeyRoundIcon} title={TOKENS_EMPTY_TITLE}>{TOKENS_EMPTY_BODY}</EmptyState>
      ) : (
        <DataTable
          rows={rows}
          columns={columns}
          labels={LABELS}
          rowKey={(t) => t.id}
          rowName={(t) => t.name}
          dataAttr="data-token"
          onOpen={(t) => setOpen(t)}
          openKey={open ? open.id : null}
          emptyText={TOKENS_EMPTY_BODY}
          widths={WIDTHS}
          minWidth={MIN_WIDTH}
        />
      )}

      {open && (
        <TokenSheet
          token={open}
          open={true}
          onOpenChange={(o) => { if (!o) setOpen(null); }}
          onRevoke={(t) => { setOpen(null); setRefusal(null); setAsk(t); }}
        />
      )}

      <MintSheet
        open={minting}
        onOpenChange={setMinting}
        rows={rows}
        onMinted={load}
      />

      <AlertDialog open={ask !== null} onOpenChange={(o) => { if (!o && !busy) { setAsk(null); setRefusal(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{revokeTitle(ask ? ask.name : "")}</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <span className="flex flex-wrap items-center gap-1 text-dense text-muted-foreground">
                {ask ? revokeBody(ask) : ""}
                <HelpTip label={REVOKE_VERB} text={REVOKE_HELP} />
              </span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          {refusal && <RefusedError subject={REVOKE_VERB} message={refusal} />}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction
              className={DANGER_FILL}
              aria-busy={busy || undefined}
              onClick={(e) => { e.preventDefault(); if (ask && !busy) void revoke(ask); }}
            >
              {busy && <Loader2Icon className="animate-spin" />} {REVOKE_VERB}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// Stamp is a time the operator way: the relative reading, the absolute
// stamp with its zone on hover.
function Stamp({ iso, text }: { iso: string | undefined; text: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default tabular-nums text-text-2">{text}</span>
      </TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}

// Expires reads an expiry in calendar days: plain text while it is far, a
// warn chip when it is near and a danger chip once it has passed. A token
// with no expiry reads never, with its tip on hover.
function Expires({ iso }: { iso: string | undefined }) {
  if (!iso) return <span className="text-foreground" title={NEVER_TIP} data-expires={NEVER}>{NEVER}</span>;
  const { word, tone } = expiryOf(iso);
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default tabular-nums text-text-2" data-expires={word}>
          {tone === "plain" ? word : <Chip tone={tone}>{word}</Chip>}
        </span>
      </TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}
