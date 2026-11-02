import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { ToneBadge } from "@/components/overview-parts";
import { NoRole, RoleChip } from "@/components/status-badge";
import { type ApiError, type AppRow, type ObjectDraft, type ObjectListing, type ObjectRow, type ObjectsAnswer, type RoleRow, appObjects, query } from "@/lib/api";
import { toYAML } from "@/lib/manifest";
import {
  COLUMN, LISTING, NEW, NOT_SET, NO_AGENT, NO_ROLE, type Nouns,
  cap, coveredHelp, declaresTitle, draftLink, draftsHelp, drift, emptyList, filterLabel, idHelp, idLabel, listingHelp, noMarks, noMatch, nounsOf, reading, setByHelp, stripOf,
} from "@/lib/object-words";
import { LOAD_MORE, shownWords } from "@/lib/role-words";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { readFailed } from "@/lib/say";
import { cn } from "@/lib/utils";

// The tab of an MCP server's page named by the plural of the type its
// manifest pins, such as Repos: how Straza lists them, the list the
// answering replica holds with the roles that reach each one and the open
// drafts that would change them, and what the server's own marks declare.
// It changes nothing.

const DEBOUNCE_MS = 300;
const STRIP: Record<string, string> = {
  plain: "border-border bg-card",
  ok: "border-ok/40 bg-ok-bg",
  danger: "border-danger/40 bg-danger-bg",
};
const NEW_BADGE = "rounded-md px-2 text-[13px] font-normal bg-accent-bg text-link border-link/40";
// The fixed shares of the table in percent. An attribute column gets at
// most ATTR_MAX and shrinks with the count, so Covered by keeps COVERED_MIN
// however many attributes a manifest declares.
const NAME_W = 28;
const DRAFTS_W = 26;
const ATTR_MAX = 12;
const COVERED_MIN = 20;
const attrWidth = (n: number) => Math.round(Math.min(ATTR_MAX, (100 - NAME_W - DRAFTS_W - COVERED_MIN) / n) * 100) / 100;

type State =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; answer: ObjectsAnswer; rows: ObjectRow[]; problem: string | null };

export type ServerObjectsProps = {
  app: AppRow;
  // roles is the roles list the page read, so a covering role's chip opens
  // that role's page. A role the list does not carry is named without a link.
  roles: RoleRow[];
};

export function ServerObjects({ app, roles }: ServerObjectsProps) {
  const [typed, setTyped] = React.useState("");
  const [q, setQ] = React.useState("");
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [busy, setBusy] = React.useState(false);
  const seq = React.useRef(0);
  const pinned = app.manifest?.straza?.objects;
  const fallback = nounsOf(pinned || { type: "" });

  React.useEffect(() => {
    const t = setTimeout(() => setQ(typed.trim()), DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [typed]);

  React.useEffect(() => {
    const my = ++seq.current;
    appObjects(app.id, query({ q })).then(
      (answer) => { if (my === seq.current) setState({ kind: "ready", answer, rows: answer.objects || [], problem: null }); },
      (e) => {
        if (my !== seq.current || (e as ApiError).status === 401) return;
        const message = readFailed("The " + fallback.plural, e as ApiError);
        setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      },
    );
  }, [app.id, q]); // eslint-disable-line react-hooks/exhaustive-deps

  const more = async () => {
    if (state.kind !== "ready" || !state.answer.next) return;
    const my = ++seq.current;
    setBusy(true);
    try {
      const answer = await appObjects(app.id, query({ q, after: state.answer.next }));
      if (my === seq.current) setState((s) => (s.kind === "ready" ? { kind: "ready", answer, rows: [...s.rows, ...(answer.objects || [])], problem: null } : s));
    } catch (e) {
      if (my === seq.current && (e as ApiError).status !== 401) setState((s) => (s.kind === "ready" ? { ...s, problem: readFailed("The " + fallback.plural, e as ApiError) } : s));
    } finally {
      setBusy(false);
    }
  };

  if (state.kind === "loading") return <p className="text-[13px] text-muted-foreground">{reading(fallback)}</p>;
  if (state.kind === "error") return <FetchError subject={cap(fallback.plural)} detail={state.message} />;

  const { answer, rows } = state;
  const w = answer.type ? nounsOf(answer) : fallback;
  const listing: ObjectListing = answer.listing || { tool: pinned?.list.tool || "", credential: "", outcome: "never", version: 0, count: 0 };
  const attrs = answer.attributes || [];
  const roleIds = new Map(roles.map((r) => [r.name, r.id]));
  return (
    <div className="flex flex-col gap-3" data-objects-tab={app.name}>
      {state.problem && <FetchError subject={cap(w.plural)} detail={state.problem} />}
      <Strip app={app.name} w={w} listing={listing} />
      <div className="flex flex-wrap items-center gap-3">
        <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={filterLabel(w)} aria-label={filterLabel(w)} className="h-9 w-full max-w-xs" />
        <span className="ml-auto text-[13px] text-muted-foreground">{shownWords(rows.length, answer.total)}</span>
      </div>
      <div className="rounded-md border border-border">
        <Table className="table-fixed" data-server-objects>
          <colgroup>
            <col style={{ width: NAME_W + "%" }} />
            {attrs.map((a) => <col key={a.name} style={{ width: attrWidth(attrs.length) + "%" }} />)}
            <col />
            <col style={{ width: DRAFTS_W + "%" }} />
          </colgroup>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>{cap(w.type)}</TableHead>
              {attrs.map((a) => <TableHead key={a.name}><Head label={a.name} help={setByHelp(a.setBy, w)} /></TableHead>)}
              <TableHead><Head label={COLUMN.covered} help={coveredHelp(app.name, w)} /></TableHead>
              <TableHead><Head label={COLUMN.drafts} help={draftsHelp(w)} /></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 && (
              <TableRow>
                <TableCell colSpan={attrs.length + 3} className="whitespace-normal text-muted-foreground">{q ? noMatch(w, q) : emptyList(w)}</TableCell>
              </TableRow>
            )}
            {rows.map((o) => (
              <TableRow key={o.id} className="text-sm">
                <TableCell className="align-top whitespace-normal">
                  <span className="font-mono break-words text-foreground" title={o.title || undefined} data-value>{o.value}</span>
                  <HelpTip label={idLabel(o.value)} text={idHelp(w, o.id)} className="ml-1.5" />
                  {o.new && <Badge variant="outline" className={cn("ml-2", NEW_BADGE)} data-new>{NEW}</Badge>}
                </TableCell>
                {attrs.map((a) => <TableCell key={a.name} className="align-top whitespace-normal"><Attr name={a.name} value={o.attributes?.[a.name]} /></TableCell>)}
                <TableCell className="align-top whitespace-normal">
                  {(o.coveredBy || []).length ? (o.coveredBy || []).map((r) => <Covering key={r} name={r} id={roleIds.get(r)} />) : <><NoRole text={NO_ROLE} />{" "}<span className="text-[13px] text-muted-foreground">{NO_AGENT}</span></>}
                </TableCell>
                <TableCell className="align-top whitespace-normal" data-drafts>
                  {(o.drafts || []).map((d) => <DraftLine key={d.draft + "/" + d.role + "/" + d.change} d={d} />)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      {answer.next && (
        <div>
          <Button variant="outline" size="sm" disabled={busy} onClick={() => void more()}>
            {busy && <Loader2Icon className="animate-spin" />} {LOAD_MORE}
          </Button>
        </div>
      )}
      {answer.drift && <p className="rounded-md border border-warn/40 bg-warn-bg px-3 py-2 text-[13px] leading-snug text-foreground" data-drift>{drift(app.name, w)}</p>}
      <Declared app={app.name} w={w} answer={answer} />
    </div>
  );
}

// Strip is the status of the listing in words, the reason of a failed one
// verbatim, with how the listing runs behind its help icon.
function Strip({ app, w, listing }: { app: string; w: Nouns; listing: ObjectListing }) {
  const s = stripOf(app, w, listing);
  return (
    <div className={cn("flex items-start gap-2 rounded-md border px-3 py-2 text-sm leading-relaxed text-text-2", STRIP[s.tone])} data-listing-strip data-tone={s.tone}>
      <div className="min-w-0 flex-1">{s.lines.map((l, i) => <p key={i} data-line>{l}</p>)}</div>
      <HelpTip label={LISTING} text={listingHelp(app, listing.tool, listing.credential)} className="mt-1" />
    </div>
  );
}

function Head({ label, help }: { label: string; help: string }) {
  return <span className="inline-flex items-center gap-1">{label}<HelpTip label={label} text={help} /></span>;
}

// Attr is one attribute cell: a value or a list of values, each a plain
// badge, or not set when the listing gave none.
function Attr({ name, value }: { name: string; value: string | string[] | undefined }) {
  const values = value === undefined ? [] : Array.isArray(value) ? value : [value];
  if (!values.length) return <span data-not-set><NoRole text={NOT_SET} /></span>;
  return <span className="inline-flex flex-wrap gap-1">{values.map((v) => <span key={v} data-attr={name}><ToneBadge tone="plain" word={v} /></span>)}</span>;
}

// Covering is one role chip that opens the role's page, a real link so it
// also opens in a new tab.
function Covering({ name, id }: { name: string; id: string | undefined }) {
  if (!id) return <span data-covered-by><RoleChip name={name} /></span>;
  return (
    <a
      href={pathFor("roles", [id])}
      className="hover:underline"
      data-covered-by
      onClick={(e) => {
        if (!isPlainClick(e)) return;
        e.preventDefault();
        navigate("roles", [id]);
      }}
    >
      <RoleChip name={name} />
    </a>
  );
}

// DraftLine is one open draft that would change which roles cover the
// row: the draft as a real link to its page, what it does, and the role.
// An older server sends no drafts, so the cell stays empty there.
function DraftLine({ d }: { d: ObjectDraft }) {
  return (
    <div className="leading-relaxed" data-object-draft={d.draft}>
      <a
        href={pathFor("drafts", [d.draft])}
        className="text-link hover:underline"
        onClick={(e) => {
          if (!isPlainClick(e)) return;
          e.preventDefault();
          navigate("drafts", [d.draft]);
        }}
      >
        {draftLink(d.draft)}
      </a>
      {" " + d.change + " "}
      <RoleChip name={d.role} />
    </div>
  );
}

// Declared is the fold with what the server's marks declare as YAML, the
// faults that make a declaration unusable, or the line that it sends none.
function Declared({ app, w, answer }: { app: string; w: Nouns; answer: ObjectsAnswer }) {
  return (
    <details data-declared>
      <summary className="cursor-pointer text-[13px] text-muted-foreground hover:text-foreground">{declaresTitle(app)}</summary>
      {answer.declared
        ? <pre className="mt-1 overflow-x-auto rounded-md border border-border bg-card px-3 py-2 font-mono text-[13px] leading-relaxed text-text-2">{toYAML(answer.declared)}</pre>
        : <p className="mt-1 text-[13px] text-muted-foreground">{noMarks(app, w)}</p>}
      {(answer.declaredFaults || []).map((f) => <p key={f} className="mt-1 text-[13px] text-warn" data-declared-fault>{f}</p>)}
    </details>
  );
}
