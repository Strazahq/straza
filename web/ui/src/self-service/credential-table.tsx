// The table of the Credentials tab: six
// columns that answer, for one server and one owner, what it uses, what
// state that is in, what the row holds and what one press does. The four
// group bands come from the rank in credential-rows.ts, and a sentence
// that is true of a kind of row rather than of one row is printed once at
// the foot instead of under every row it applies to.
import * as React from "react";
import { Loader2Icon } from "lucide-react";
import type { ColumnDef } from "@tanstack/react-table";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { DataTable, plain } from "@/components/data-table";
import { RefusedError } from "@/components/error-state";
import { WordBadge } from "@/components/users-table";
import { CAPS } from "@/components/wizard/parts";
import { cn } from "@/lib/utils";
import { dayOf, relTimeText } from "@/lib/words";
import type { Ask } from "./credential-dialogs";
import { BANDS, type CredentialRow, expired, keyOf, needing, oneProcess, ownOf, rank, thingOf, viaSponsor } from "./credential-rows";
import * as W from "./credential-words";

const SMALL = "text-[13px] leading-snug text-muted-foreground";
const DANGER_BUTTON = "border-danger text-danger hover:bg-danger-bg";
const WIDTHS = { server: "14%", for: "15%", credential: "15%", status: "16%", detail: "26%", actions: "14%" };
const LABELS = { server: W.COL_SERVER, for: W.COL_FOR, credential: W.COL_CREDENTIAL, status: W.COL_STATUS, detail: W.COL_DETAIL, actions: W.COL_ACTIONS };
// The bands are never folded here, so one empty set serves every render.
const OPEN_BANDS = new Set<string>();

// RowNote is one sentence under one row, for an action that failed without
// a dialog of its own to say it in: which row, what was refused, and the
// sentence the server answered with.
export type RowNote = { key: string; subject: string; text: string };

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

type Props = {
  // shown are the rows of the table, already in band order.
  shown: CredentialRow[];
  // scoped are every row the chip strip lets through, the kind-none ones
  // included, which is what the count and the foot line are counted from.
  scoped: CredentialRow[];
  // none are the servers in reach that need no credential at all.
  none: string[];
  // owner is the agent the chip strip narrowed to, or "" for the person.
  owner: string;
  cells: Cells;
};

// CredentialTable draws the rows with their bands and the sentences that
// belong to the whole table at its foot.
export function CredentialTable({ shown, scoped, none, owner, cells }: Props) {
  const hints: string[] = [];
  for (const r of shown) {
    if (oneProcess(r)) add(hints, W.oneProcessHint(r.runtime));
    if (r.who && r.kind === "oauth" && !r.connected && !viaSponsor(r, ownOf(cells.rows, r.app)) && r.agents !== "sponsor" && r.agents !== "shared") {
      add(hints, W.ADMIN_ROUTE);
    }
  }
  const foot = (none.length > 0 || hints.length > 0) && (
    <div className="flex flex-col gap-3" data-credentials-foot>
      {none.length > 0 && <p className={SMALL}>{W.noneLine(none, owner)}</p>}
      {hints.length > 0 && (
        <div className="flex flex-col gap-1">
          <span className={CAPS}>{W.HINTS_TITLE}</span>
          {hints.map((t) => <p key={t} className={cn(SMALL, "max-w-[75ch]")}>{t}</p>)}
        </div>
      )}
    </div>
  );
  return (
    <DataTable
      rows={shown}
      columns={buildColumns(cells)}
      labels={LABELS}
      rowKey={keyOf}
      rowName={nameOf}
      dataAttr="data-credential"
      count={(n) => W.countWords(n, needing(scoped))}
      emptyText={W.emptyBody(owner)}
      widths={WIDTHS}
      groups={{
        key: (r) => String(rank(r)),
        header: (key, members) => (
          <span className="flex items-center gap-2 px-2 py-1 text-sm" data-band={BANDS[Number(key)]}>
            <b className="font-semibold text-foreground">{BANDS[Number(key)]}</b>
            <span className="text-muted-foreground">{members.length}</span>
          </span>
        ),
        collapsed: OPEN_BANDS,
      }}
      foot={foot || undefined}
    />
  );
}

// nameOf is how the table names one row: the server, and the agent when
// the row is not the person's own.
export const nameOf = (r: CredentialRow) => (r.who ? r.app + " for " + r.who : r.app);

// add keeps a sentence that is true of a kind of row rather than of one
// row, so the table prints it once at its foot.
function add(hints: string[], text: string) {
  if (!hints.includes(text)) hints.push(text);
}

// setterOf reads who set a credential the way the person reads it: their
// own username is "you".
function setterOf(row: CredentialRow, me: string): string {
  const who = row.set_by || row.who || me;
  return who === me ? "you" : who;
}

type Tone = "ok" | "warn" | "danger" | "plain";

// statusOf is the Status cell: the word, its hue and the small line under
// it. The word alone says the state, so the hue only repeats it.
function statusOf(row: CredentialRow, c: Cells): { word: string; tone: Tone; line: string } {
  if (!row.reached) return { word: row.who ? W.NOT_IN_ITS_REACH : W.NOT_IN_YOUR_REACH, tone: "plain", line: W.LEFTOVER_LINE };
  if (row.kind === "static" || oneProcess(row)) {
    const one = oneProcess(row);
    return { word: one ? W.SHARED_ONLY : W.SHARED, tone: "plain", line: one ? W.ONE_PROCESS_LINE : W.SHARED_LINE };
  }
  const via = row.who ? viaSponsor(row, ownOf(c.rows, row.app)) : false;
  if (row.kind === "oauth") {
    if (row.connected) return { word: W.SIGNED_IN, tone: "ok", line: W.signedInLine(row.updated_at ? dayOf(row.updated_at) : "") };
    if (via) return { word: W.USES_YOUR_SIGN_IN, tone: "ok", line: W.WHILE_SWITCH_ON };
    return { word: row.who ? W.AGENT_SIGN_IN_UNAVAILABLE : W.NOT_SIGNED_IN, tone: "warn", line: row.who ? "" : W.OWN_SIGN_IN_LINE };
  }
  if (row.connected && expired(row)) return { word: W.expiredWord(dayOf(row.expires_at)), tone: "danger", line: W.EXPIRED_LINE };
  if (row.connected) return { word: W.SET, tone: "ok", line: W.setByLine(setterOf(row, c.me), row.updated_at ? dayOf(row.updated_at) : "") };
  if (via) return { word: W.USES_YOUR_TOKEN, tone: "ok", line: W.WHILE_SWITCH_ON };
  const line = row.who ? (row.agents === "shared" ? W.agentSharedLine(row.app) : W.agentRefusedLine(row.app)) : W.OWN_TOKEN_LINE;
  return { word: W.NOT_SET, tone: "warn", line };
}

// AgentSwitch is the person's opt-in for the agents they sponsor, offered
// only where the server lets an agent run on its sponsor's credential. The
// caption names those agents and says how their calls are recorded, since
// that is the whole consequence of turning it on.
function AgentSwitch({ row, c }: { row: CredentialRow; c: Cells }) {
  const on = !!row.allow_agents;
  const thing = thingOf(row);
  return (
    <span className="flex flex-col gap-1">
      <span className="flex items-center gap-2">
        <Switch
          checked={on}
          disabled={c.busy === keyOf(row)}
          onCheckedChange={() => c.toggle(row)}
          aria-label={W.SWITCH_LABEL}
          data-agents-switch={row.app}
        />
        <span className="text-[13px] text-foreground">{W.SWITCH_LABEL}</span>
      </span>
      <span className={SMALL}>{on ? W.switchOn(c.sponsored, row.app, thing) : W.switchOff(c.sponsored, row.app, thing)}</span>
    </span>
  );
}

// DetailCell is what the row has and what it means: the fingerprint and
// when a token stops working, the scopes and the grant's end for a sign-in,
// the switch where it applies, and the one sentence the row needs.
function DetailCell({ row, c }: { row: CredentialRow; c: Cells }) {
  const parts: React.ReactNode[] = [];
  const via = row.who ? viaSponsor(row, ownOf(c.rows, row.app)) : false;
  const line = (text: string) => <p key={text} className={cn(SMALL, "max-w-[60ch] whitespace-normal")}>{text}</p>;
  const mono = (text: string) => <span key={"mono:" + text} className="font-mono text-[12.5px] text-foreground">{text}</span>;

  if (!row.reached) {
    if (row.fingerprint) parts.push(mono(row.fingerprint));
    parts.push(line(W.leftover(row.app, row.who, thingOf(row))));
  } else if (row.kind === "token") {
    if (row.connected) {
      if (row.fingerprint) parts.push(mono(row.fingerprint));
      parts.push(line(W.stopsLine(expired(row), row.expires_at ? dayOf(row.expires_at) : "")));
      if (expired(row)) parts.push(line(W.expiredSentence(row.app, dayOf(row.expires_at), row.who)));
      else if (!row.who && row.agents === "sponsor") parts.push(<AgentSwitch key="switch" row={row} c={c} />);
      else if (row.who) parts.push(line(W.agentOwnToken(row.who, row.app)));
    } else {
      parts.push(line(row.who ? W.agentNoToken(row.who, row.app, row.agents, via) : W.ownNotSet(row.app)));
    }
  } else if (row.kind === "oauth") {
    if (row.connected) {
      if (row.scopes && row.scopes.length) parts.push(mono(row.scopes.join(" ")));
      if (row.expires_at) parts.push(line(W.grantLine(relTimeText(row.expires_at))));
      if (!row.who && row.agents === "sponsor") parts.push(<AgentSwitch key="switch" row={row} c={c} />);
      else if (!row.who && row.agents === "own") parts.push(line(W.NO_SWITCH_HERE));
    } else if (row.who) {
      parts.push(line(W.agentSignIn(row.who, row.app, row.provider || "", row.agents, via)));
    } else {
      parts.push(line(W.ownNotSignedIn(row.app, row.provider || "")));
    }
  }
  const note = c.note && c.note.key === keyOf(row) ? c.note : null;
  if (note) parts.push(<RefusedError key="note" subject={note.subject} message={note.text} />);
  if (!parts.length) return null;
  return <span className="flex flex-col items-start gap-1">{parts}</span>;
}

// ActionsCell is what one press does to the row, as small outline buttons
// with the destructive one in the danger hue.
function ActionsCell({ row, c }: { row: CredentialRow; c: Cells }) {
  const key = keyOf(row);
  const busy = c.busy === key;
  const named = (label: string) => W.actionName(label, nameOf(row));
  const out: React.ReactNode[] = [];
  const danger = (label: string, ask: Ask) => (
    <Button key={label} variant="outline" size="sm" className={DANGER_BUTTON} disabled={busy} aria-label={named(label)} onClick={() => c.ask(ask)}>
      {label}
    </Button>
  );

  if (!row.reached) {
    if (row.kind === "token" || row.kind === "oauth") out.push(danger(W.REMOVE, { row, kind: "leftover" }));
  } else if (row.kind === "token") {
    const label = row.connected ? (expired(row) ? W.PASTE_NEW : W.REPLACE) : W.PASTE;
    out.push(
      <Button key="paste" variant="outline" size="sm" disabled={busy} aria-label={named(label)} onClick={() => c.paste(row)}>{label}</Button>,
    );
    if (row.connected) out.push(danger(W.REMOVE, { row, kind: "remove" }));
  } else if (row.kind === "oauth") {
    if (row.connected) {
      if (!row.who) {
        out.push(
          <Button key="again" variant="outline" size="sm" disabled={busy} aria-label={named(W.SIGN_IN_AGAIN)} onClick={() => c.signIn(row)}>
            {busy && <Loader2Icon className="animate-spin" />}{W.SIGN_IN_AGAIN}
          </Button>,
        );
      }
      out.push(danger(W.DISCONNECT, { row, kind: "disconnect" }));
    } else if (!row.who) {
      out.push(
        <Button key="signin" variant="outline" size="sm" disabled={busy} aria-label={named(W.signInWith(row.provider || ""))} onClick={() => c.signIn(row)}>
          {busy && <Loader2Icon className="animate-spin" />}{W.signInWith(row.provider || "")}
        </Button>,
      );
    }
  }
  if (!out.length) return null;
  return <span className="flex flex-wrap justify-end gap-2">{out}</span>;
}

// buildColumns draws the six columns: where the call goes, who it is for,
// what the server uses, the state, the detail, and what to press.
function buildColumns(c: Cells): ColumnDef<CredentialRow>[] {
  return [
    {
      id: "server",
      header: plain<CredentialRow>(W.COL_SERVER),
      cell: ({ row }) => (
        <span className="inline-block rounded-md border border-link/40 bg-background px-1.5 py-px font-mono text-[13px] text-link" data-server={row.original.app}>
          {row.original.app}
        </span>
      ),
    },
    {
      id: "for",
      header: plain<CredentialRow>(W.COL_FOR),
      cell: ({ row }) => (
        <span className="flex flex-col items-start gap-0.5">
          <b className="font-semibold text-foreground">{row.original.who || W.YOU}</b>
          {row.original.who && <span className={SMALL}>{W.SPONSORED_LINE}</span>}
        </span>
      ),
    },
    {
      id: "credential",
      header: plain<CredentialRow>(W.COL_CREDENTIAL, W.CREDENTIAL_HELP),
      cell: ({ row }) => <span className="text-sm text-text-2">{W.kindWords(row.original.kind, row.original.who, row.original.provider)}</span>,
    },
    {
      id: "status",
      header: plain<CredentialRow>(W.COL_STATUS),
      cell: ({ row }) => {
        const said = statusOf(row.original, c);
        return (
          <span className="flex flex-col items-start gap-0.5">
            <WordBadge word={said.word} tone={said.tone} attr="data-status" />
            <span className={cn(SMALL, "whitespace-normal")}>{said.line}</span>
          </span>
        );
      },
    },
    {
      id: "detail",
      header: plain<CredentialRow>(W.COL_DETAIL),
      cell: ({ row }) => <DetailCell row={row.original} c={c} />,
    },
    {
      id: "actions",
      header: plain<CredentialRow>(""),
      enableHiding: false,
      cell: ({ row }) => <ActionsCell row={row.original} c={c} />,
    },
  ];
}
