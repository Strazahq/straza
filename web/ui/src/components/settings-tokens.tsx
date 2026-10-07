import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { KeyRoundIcon, Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { DataTable, plain } from "@/components/data-table";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { MintSheet } from "@/components/settings-mint-sheet";
import { GrantChips, TokenSheet } from "@/components/settings-token-sheet";
import { type ApiError, type ApiTokenRow, listApiTokens, revokeApiToken } from "@/lib/api";
import { notify } from "@/lib/notify";
import { CANCEL } from "@/lib/role-words";
import { readFailed, refused } from "@/lib/say";
import {
  EXPIRED,
  GRANTS_HELP,
  LAST_USED_HELP,
  NEVER,
  NEVER_TIP,
  READING_TOKENS,
  REVOKE,
  REVOKE_HELP,
  REVOKE_VERB,
  SUBJECT_TOKENS,
  TOKENS_EMPTY_BODY,
  TOKENS_EMPTY_TITLE,
  TOKENS_LEDE,
  TOKEN_COLUMN,
  agoWord,
  byWord,
  expiresWord,
  revokeName,
  revokeTitle,
  revokeBody,
  revokedToast,
  tokensCount,
} from "@/lib/settings-words";
import { absTime } from "@/lib/words";

// The API tokens tab of Settings: the
// credentials automation signs in with, one row each with the grants in
// words, the token's own sheet behind a row, the mint sheet the page head
// opens, and Revoke at the row end.

const DANGER_BUTTON = "border-danger text-danger hover:bg-danger-bg hover:text-danger";
const WIDTHS: Record<string, string> = { name: "190px", created: "110px", expires: "110px", used: "110px", revoke: "96px" };
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

type Props = {
  // mintRequest is the page head's counter: the New API token button moves
  // it, and the tab opens the mint sheet when it moves.
  mintRequest: number;
};

export function TokensTab({ mintRequest }: Props) {
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

  // The head owns the action and the tab owns the sheet, so the counter
  // moving is the request; its first value opens nothing.
  const seen = React.useRef(mintRequest);
  React.useEffect(() => {
    if (seen.current === mintRequest) return;
    seen.current = mintRequest;
    setMinting(true);
  }, [mintRequest]);

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
        <span className="flex flex-wrap items-baseline gap-1.5">
          <b className="font-semibold text-foreground">{row.original.name}</b>
          {row.original.created_by && <span className="text-[13px] text-muted-foreground">{byWord(row.original.created_by)}</span>}
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
      cell: ({ row }) => <Stamp iso={row.original.created} text={agoWord(row.original.created)} />,
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
        ? <Stamp iso={row.original.lastUsed} text={agoWord(row.original.lastUsed)} />
        : <span className="text-muted-foreground" data-last-used={NEVER}>{NEVER}</span>),
    },
    {
      id: "revoke",
      header: plain<ApiTokenRow>(""),
      enableHiding: false,
      cell: ({ row }) => (
        <span className="flex justify-end">
          <Button
            variant="outline"
            size="sm"
            className={DANGER_BUTTON}
            aria-label={revokeName(row.original.name)}
            onClick={(e) => { e.stopPropagation(); setRefusal(null); setAsk(row.original); }}
          >
            {REVOKE}
          </Button>
        </span>
      ),
    },
  ], []);

  if (state.kind === "loading") return <p className="text-sm text-muted-foreground">{READING_TOKENS}</p>;
  if (state.kind === "error") return <FetchError subject={SUBJECT_TOKENS} detail={state.message} />;

  const rows = state.rows;

  return (
    <div className="flex flex-col gap-4">
      {state.problem && <FetchError subject={SUBJECT_TOKENS} detail={state.problem} lastRead={state.lastRead} />}
      <p className="max-w-[75ch] text-sm text-text-2" data-tokens-lede>{TOKENS_LEDE}</p>

      {rows.length === 0 ? (
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
          count={(shown) => tokensCount(shown)}
          emptyText={TOKENS_EMPTY_BODY}
          widths={WIDTHS}
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
              <span className="flex flex-wrap items-center gap-1 text-sm text-muted-foreground">
                {ask ? revokeBody(ask) : ""}
                <HelpTip label={REVOKE_VERB} text={REVOKE_HELP} />
              </span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          {refusal && <RefusedError subject={REVOKE_VERB} message={refusal} />}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-danger text-white hover:bg-danger/90"
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

// Expires reads an expiry in the future tense. No expiry is the amber
// word, since a credential that lives until revoked is a posture choice,
// and a stamp already past is the danger word.
function Expires({ iso }: { iso: string | undefined }) {
  if (!iso) return <span className="text-warn" title={NEVER_TIP} data-expires={NEVER}>{NEVER}</span>;
  const word = expiresWord(iso);
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className={"cursor-default tabular-nums " + (word === EXPIRED ? "text-danger" : "text-text-2")} data-expires={word}>{word}</span>
      </TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}
