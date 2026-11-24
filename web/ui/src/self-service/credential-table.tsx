// The table of the Credentials tab: one line per server and owner that says
// where the call goes, who it is for, what the server uses, the state of
// that and what one press does. The four group bands come from the rank in
// credential-rows.ts. The one sentence a row needs sits behind a help icon
// beside its status.
import * as React from "react";
import { Loader2Icon } from "lucide-react";
import type { ColumnDef } from "@tanstack/react-table";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Chip, type Tone } from "@/components/chip";
import { DataTable, plain } from "@/components/data-table";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { KindTile } from "@/components/kind-icon";
import { dayWord } from "@/lib/words";
import type { Ask } from "./credential-dialogs";
import { BANDS, type CredentialRow, expired, keyOf, oneProcess, ownOf, rank, thingOf, viaSponsor } from "./credential-rows";
import * as W from "./credential-words";

const MUTED = "text-caption text-muted-foreground";
const NAME = "min-w-0 truncate font-mono text-caption text-foreground";
// WIDTHS hold every cell on one line at 1280 px. The Uses column takes what
// is left, and MIN_WIDTH is the fixed columns plus 200 px for it, below
// which the table scrolls inside its own box.
const WIDTHS = { server: "180px", for: "200px", status: "240px", actions: "380px" };
const MIN_WIDTH = "1200px";
const LABELS = { server: W.COL_SERVER, for: W.COL_FOR, uses: W.COL_USES, status: W.COL_STATUS, actions: W.COL_ACTIONS };
// The bands are never folded here, so one empty set serves every render.
const OPEN_BANDS = new Set<string>();

// RowNote is one error block under one row, for an action that failed
// without a dialog of its own to say it in: which row, the heading that
// says what did not happen, and why.
export type RowNote = { key: string; heading: string; text: string };

// Cells is what every cell needs beyond its own row: the other rows, so an
// agent's row can read the person's, who is signed in, which row is busy,
// and the doors each action opens.
export type Cells = {
  rows: CredentialRow[];
  me: string;
  sponsored: string[];
  busy: string | null;
  note: RowNote | null;
  paste: (row: CredentialRow) => void;
  ask: (ask: Ask) => void;
  signIn: (row: CredentialRow) => void;
  toggle: (row: CredentialRow) => void;
};

// CellsContext hands every cell the Cells of the render. The columns are
// built once, so each cell keeps its elements across renders and the button
// that opened a dialog is still in the page when the focus goes back to it.
const CellsContext = React.createContext<Cells | null>(null);
const useCells = () => React.useContext(CellsContext) as Cells;

type Props = {
  // shown are the rows of the table, already in band order.
  shown: CredentialRow[];
  // none are the servers in reach that need no credential at all.
  none: string[];
  // owner is the agent the filter narrowed to, or "" for the person.
  owner: string;
  cells: Cells;
};

// CredentialTable draws the rows with their bands and, under them, the
// servers that need nothing.
export function CredentialTable({ shown, none, owner, cells }: Props) {
  const foot = none.length > 0 && (
    <div className="flex flex-col gap-2" data-credentials-foot>
      <p className="text-caption leading-snug text-muted-foreground">{W.noneLine(none, owner)}</p>
    </div>
  );
  return (
    <CellsContext.Provider value={cells}>
      <DataTable
        rows={shown}
        columns={COLUMNS}
        labels={LABELS}
        rowKey={keyOf}
        rowName={nameOf}
        dataAttr="data-credential"
        emptyText={W.EMPTY_FILTER}
        columnsMenu={false}
        widths={WIDTHS}
        minWidth={MIN_WIDTH}
        groups={{
          key: (r) => String(rank(r, cells.rows)),
          header: (key, members) => (
            <span className="flex items-center gap-2 px-2 py-1 text-dense" data-band={BANDS[Number(key)]}>
              <b className="font-semibold text-foreground">{BANDS[Number(key)]}</b>
              <span className="font-mono text-caption text-muted-foreground" data-band-count>{members.length}</span>
            </span>
          ),
          collapsed: OPEN_BANDS,
        }}
        foot={foot || undefined}
      />
    </CellsContext.Provider>
  );
}

// nameOf is how the table names one row: the server, and the agent when
// the row is not the person's own.
export const nameOf = (r: CredentialRow) => (r.who ? r.app + " for " + r.who : r.app);

// lent says an agent's row has nothing of its own and runs on the person's
// own row for the same server.
const lent = (r: CredentialRow, rows: CredentialRow[]) => !!r.who && !r.connected && viaSponsor(r, ownOf(rows, r.app));

// setterOf reads who set a credential the way the person reads it: their
// own username is "you".
function setterOf(row: CredentialRow, me: string): string {
  const who = row.set_by || row.who || me;
  return who === me ? "you" : who;
}

// Said is the Status cell: the word and its tone, the few muted words after
// it, and the sentence behind its help icon, each empty where a row has
// none. The word alone says the state, so the tone only repeats it.
type Said = { word: string; tone: Tone; muted: string; help: string };

function statusOf(row: CredentialRow, c: Cells): Said {
  const { app, who } = row;
  const said = (word: string, tone: Tone, muted: string, help: string): Said => ({ word, tone, muted, help });
  if (!row.reached) return said(W.OUT_OF_REACH, "plain", "", W.leftover(app, who, thingOf(row)));
  if (oneProcess(row)) return said(W.SHARED_ONLY, "plain", W.SHARED_LINE, "");
  if (row.kind !== "token" && row.kind !== "oauth") return said(W.SHARED, "plain", W.SHARED_LINE, "");
  if (row.kind === "oauth" && row.connected) return said(W.SIGNED_IN, "ok", W.RENEWS_LINE, "");
  if (row.connected && expired(row)) {
    const day = dayWord(row.expires_at);
    return said(W.EXPIRED, "danger", day, W.expiredSentence(app, day, who));
  }
  if (row.connected) {
    const since = row.updated_at ? dayWord(row.updated_at) : "";
    const muted = row.expires_at ? W.untilLine(dayWord(row.expires_at)) : since ? W.sinceLine(since) : "";
    return said(W.SET, "ok", muted, W.setSentence(who, since, setterOf(row, c.me)));
  }
  if (!who) {
    if (row.kind === "oauth") return said(W.NOT_SIGNED_IN, "warn", "", W.ownNotSignedIn(app, row.provider || ""));
    return said(W.NOT_SET, "warn", "", W.ownNotSet(app));
  }
  if (lent(row, c.rows)) return said(W.USES_YOURS, "ok", "", W.lentSentence(who, app));
  if (row.agents === "shared") return said(W.SHARED_ACCOUNT, "plain", "", W.sharedAccountSentence(who, app));
  const sponsor = row.agents === "sponsor";
  if (row.kind === "oauth") return said(W.CANNOT_SIGN_IN, "warn", "", W.cannotSignIn(who, app, sponsor));
  return said(W.NOT_SET, "warn", "", W.agentNotSet(who, app, sponsor));
}

function StatusCell({ row }: { row: CredentialRow }) {
  const said = statusOf(row, useCells());
  return (
    <span className="flex items-center gap-1.5 whitespace-nowrap">
      <Chip tone={said.tone} data-status={said.word}>{said.word}</Chip>
      {said.muted && <span className={MUTED} data-status-line>{said.muted}</span>}
      {said.help && <HelpTip label={W.whyName(nameOf(row))} text={said.help} />}
    </span>
  );
}

// AgentSwitch is the person's opt-in for the agents they sponsor, offered
// only on their own row where the server lets an agent run on its
// sponsor's credential. The help names those agents and says how their
// calls are recorded, since that is the whole consequence of turning it on.
function AgentSwitch({ row, c }: { row: CredentialRow; c: Cells }) {
  const on = !!row.allow_agents;
  const thing = thingOf(row);
  return (
    <span className="flex items-center gap-2">
      <Switch
        checked={on}
        disabled={c.busy === keyOf(row)}
        onCheckedChange={() => c.toggle(row)}
        aria-label={W.SWITCH_LABEL}
        data-agents-switch={row.app}
      />
      <span className="text-dense text-foreground">{W.SWITCH_LABEL}</span>
      <HelpTip
        label={W.actionName(W.SWITCH_LABEL, nameOf(row))}
        text={on ? W.switchOn(c.sponsored, row.app, thing) : W.switchOff(c.sponsored, row.app, thing)}
      />
    </span>
  );
}

type Variant = "default" | "outline" | "ghost";

// ActionsCell is what one press does to the row, as small buttons on one
// line. None of them is red: the confirm a removal opens carries the red.
function ActionsCell({ row }: { row: CredentialRow }) {
  const c = useCells();
  const key = keyOf(row);
  const busy = c.busy === key;
  const out: React.ReactNode[] = [];
  const button = (label: string, variant: Variant, onClick: () => void, spins = false) => (
    <Button key={label} variant={variant} size="sm" disabled={busy} aria-label={W.actionName(label, nameOf(row))} onClick={onClick}>
      {spins && busy && <Loader2Icon className="animate-spin" />}{label}
    </Button>
  );
  const paste = () => c.paste(row);
  const remove = () => c.ask({ row, kind: "remove" });
  const sponsorSwitch = !row.who && row.agents === "sponsor" ? <AgentSwitch key="switch" row={row} c={c} /> : null;

  if (!row.reached) {
    if (row.kind === "token" || row.kind === "oauth") out.push(button(W.REMOVE, "ghost", () => c.ask({ row, kind: "leftover" })));
  } else if (row.kind === "token") {
    if (!row.connected) out.push(button(W.PASTE, row.who ? "outline" : "default", paste));
    else if (expired(row)) out.push(button(W.PASTE_NEW, "default", paste), button(W.REMOVE, "ghost", remove));
    else out.push(sponsorSwitch, button(W.REPLACE, "ghost", paste), button(W.REMOVE, "ghost", remove));
  } else if (row.kind === "oauth" && !row.who) {
    const provider = row.provider || "";
    if (!row.connected) out.push(button(W.signInWith(provider), "default", () => c.signIn(row), true));
    else out.push(sponsorSwitch, button(W.DISCONNECT, "ghost", () => c.ask({ row, kind: "disconnect" })));
  }
  const note = c.note && c.note.key === key ? c.note : null;
  if (!out.length && !note) return null;
  return (
    <span className="flex flex-col items-end gap-2 whitespace-normal">
      {out.length > 0 && <span className="flex items-center justify-end gap-1.5 whitespace-nowrap">{out}</span>}
      {note && <RefusedError subject="" heading={note.heading} message={note.text} />}
    </span>
  );
}

// UsesCell is the Uses column: what the server uses for the row's owner.
function UsesCell({ row }: { row: CredentialRow }) {
  const words = W.usesWords(row.kind, row.who, row.provider, lent(row, useCells().rows));
  return <span className="block truncate text-text-2" title={words}>{words}</span>;
}

// COLUMNS are the five columns: where the call goes, who it is for, what
// the server uses, the state, and what to press.
const COLUMNS: ColumnDef<CredentialRow>[] = [
  {
    id: "server",
    header: plain<CredentialRow>(W.COL_SERVER),
    cell: ({ row }) => (
      <span className="flex min-w-0 items-center gap-2" data-server={row.original.app}>
        <KindTile kind="server" size="sm" />
        <span className={NAME} title={row.original.app}>{row.original.app}</span>
      </span>
    ),
  },
  {
    id: "for",
    header: plain<CredentialRow>(W.COL_FOR),
    cell: ({ row }) => {
      const who = row.original.who;
      if (!who) return <span className="text-foreground">{W.YOU}</span>;
      return (
        <span className="flex min-w-0 items-center gap-2" data-for={who}>
          <KindTile kind="agent" size="sm" />
          <span className={NAME} title={who}>{who}</span>
        </span>
      );
    },
  },
  {
    id: "uses",
    header: plain<CredentialRow>(W.COL_USES),
    cell: ({ row }) => <UsesCell row={row.original} />,
  },
  {
    id: "status",
    header: plain<CredentialRow>(W.COL_STATUS),
    cell: ({ row }) => <StatusCell row={row.original} />,
  },
  {
    id: "actions",
    header: plain<CredentialRow>(""),
    enableHiding: false,
    cell: ({ row }) => <ActionsCell row={row.original} />,
  },
];
