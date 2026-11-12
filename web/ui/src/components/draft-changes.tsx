import * as React from "react";
import { ChipDiff, HoldersChip, KindIcon, MARK_TONE, SummaryChips } from "@/components/draft-chips";
import { DraftChip, NameChips } from "@/components/draft-checks";
import { Button } from "@/components/ui/button";
import { coversKey } from "@/lib/access-covers";
import { coversOn, coversText } from "@/lib/access-object-words";
import { type ApiError, type DraftCoverage, type DraftDetail, type DraftItem, listApps, listRoles } from "@/lib/api";
import { coverageFor, coverageNouns, objectsMove } from "@/lib/drafts-access";
import { wordDiff } from "@/lib/drafts-diff";
import { type ItemFacts, cardId, itemFacts } from "@/lib/drafts-items";
import { type RoleView, type Sides, type ToolsMove, appFacts, appRows, hostOf, impliedOf, manifestOf, objectOf, remoteApp, ruleMarks, secretAsks } from "@/lib/drafts-model";
import {
  DESCRIPTION,
  EVERY_TOOL,
  FILE_MASKED,
  GOES_WITH,
  GOES_WITH_ALL,
  IMPLIES,
  LIVE_LIST,
  MARK,
  NOTHING,
  NOTHING_STARTS,
  NOT_READABLE_DOC,
  NOW_AFTER,
  NO_FIELD_CHANGE,
  NO_OTHER_FIELD,
  OFF_LINE,
  REMOVED_LINE,
  REMOVED_LINE_PAST,
  RULE_COLUMN,
  RULE_MARK,
  SECRET_LINE,
  SECRET_LINE_BOTH,
  SECRET_ROW,
  TOOLS_NOT_READ,
  TOOLS_ROW,
  contactedLine,
  everyType,
  fileFold,
  notLive,
  objectWords,
  objectsLabel,
  openLive,
  roleKindWords,
  toolsOn,
} from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { list, sentence } from "@/lib/policy-words";
import { navigate } from "@/lib/router";
import { readFailed } from "@/lib/say";
import { cn } from "@/lib/utils";
import { absTime } from "@/lib/words";

// What changes: one card per item, in the shapes the server, role and
// policy pages already use, each with its own file. A new server reads its
// facts, a changed one the Now and After rows of the change sheets, a new
// role its tools and objects, a changed role one row of chips each for its
// tools and, on a server that pins an object type, its objects in that
// server's own word, and a policy set its rules in the policy words with a
// mark, an edited rule over its old sentence. An open draft is read against
// live state, a published one against its change record (sidesOf). Every
// document is text: no link and no image from a draft ever renders.

const TH = "px-3 py-1.5 text-left text-[13px] font-semibold text-muted-foreground";
const TD = "px-3 py-1.5 align-top";

// Facts is a list of labelled facts, the server cards' grid.
function Facts({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="m-0 grid gap-x-4 gap-y-1.5 text-sm sm:grid-cols-[200px_1fr]">
      {rows.map(([label, value]) => (
        <div key={label} className="contents" data-fact={label}>
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="m-0 break-words text-foreground">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

// NowAfter is the table the change sheets show for a live object.
function NowAfter({ rows }: { rows: [string, string, string][] }) {
  return (
    <table className="w-full table-fixed border-collapse overflow-hidden rounded-md border border-border text-sm">
      <thead className="border-b border-border bg-secondary/40"><tr><th className={TH}>{NOW_AFTER.field}</th><th className={TH}>{NOW_AFTER.now}</th><th className={TH}>{NOW_AFTER.after}</th></tr></thead>
      <tbody>
        {rows.map(([label, was, now]) => (
          <tr key={label} className="border-b border-border last:border-b-0" data-row={label}>
            <td className={TD}>{label}</td><td className={cn(TD, "break-words text-muted-foreground")}>{was}</td><td className={cn(TD, "break-words text-foreground")}>{now}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Implied({ objects, title }: { objects: string[]; title: string }) {
  if (!objects.length) return null;
  return (
    <div className="flex flex-col gap-1 text-sm">
      <span className="font-semibold text-foreground">{title}</span>
      <ul className="m-0 list-disc pl-5 text-text-2">{objects.map((o) => <li key={o}>{objectWords(o)}</li>)}</ul>
    </div>
  );
}

function AppBody({ item, sides, detail, open }: { item: DraftItem; sides: Sides; detail: DraftDetail; open: boolean }) {
  const m = manifestOf(sides.after.doc);
  if (!m) return <p className="m-0 text-sm text-text-2">{NOT_READABLE_DOC}</p>;
  const seen = (detail.contacted || {})[objectOf(item)];
  const url = m.straza?.runtime?.remote?.url || "";
  const host = hostOf(url, item.name);
  const tools: [string, React.ReactNode] | null = !remoteApp(sides.after.doc) ? null : seen
    ? [TOOLS_ROW, <span key="t" className="flex flex-col gap-1"><NameChips names={seen.tools} /><span className="text-[13px] text-muted-foreground">{contactedLine(host, absTime(seen.at), seen.tools.length)}</span></span>]
    : sides.before || !open ? null : [TOOLS_ROW, <DraftChip key="t" word={TOOLS_NOT_READ} tone="unknown" />];
  // An open draft whose check warns that the server has no secret says so
  // on its card, and that the publish dialog asks for it, with the listing
  // credential when that is missing too.
  const ask = detail.draft.state === "open" ? secretAsks(detail.draft, detail.verdict).find((a) => a.name === item.name && a.secret) : undefined;
  const secret: [string, React.ReactNode] | null = ask ? [SECRET_ROW, <span key="s" className="text-warn" data-secret-row>{ask.listing ? SECRET_LINE_BOTH : SECRET_LINE}</span>] : null;
  if (sides.before) {
    const rows = appRows(manifestOf(sides.before.doc) || {}, m);
    const facts = [secret, tools].filter((r) => r !== null);
    return (
      <>
        {rows.length ? <NowAfter rows={rows} /> : null}
        {facts.length > 0 && <Facts rows={facts} />}
        <p className="m-0 text-[13px] text-muted-foreground" data-no-other>{rows.length ? NO_OTHER_FIELD : NO_FIELD_CHANGE}</p>
      </>
    );
  }
  const facts: [string, React.ReactNode][] = appFacts(m);
  if (secret) facts.splice(facts.findIndex((r) => r[0] === "Sent as") + 1, 0, secret);
  return (
    <>
      <Facts rows={[...facts, ...(tools ? [tools] : [])]} />
      {open && <p className="m-0 text-[13px] text-muted-foreground">{NOTHING_STARTS}</p>}
    </>
  );
}

// Tools names a new role's tools as chips, every tool as one chip.
function Tools({ tools }: { tools: string[] }) {
  if (tools.includes("*")) return <DraftChip word={EVERY_TOOL} tone="accent" />;
  return tools.length ? <NameChips names={tools} /> : <>{NOTHING}</>;
}

// Objects names the objects a new role covers, every object as one chip,
// and nothing where it covers none.
function Objects({ c }: { c: DraftCoverage }) {
  if (c.every_after) return <DraftChip word={everyType(coverageNouns(c))} tone="accent" />;
  const names = c.after.map((o) => o.value);
  return names.length ? <NameChips names={names} /> : <span className="text-muted-foreground">{NOTHING}</span>;
}

// ObjectsDiff is a changed role's objects as one row of chips: what it
// gains, what it drops, and what it keeps, with every object as one chip.
function ObjectsDiff({ c }: { c: DraftCoverage }) {
  const every = everyType(coverageNouns(c));
  const m = objectsMove(c);
  const values = (os: { value: string }[]) => os.map((o) => o.value);
  if (m.every === "kept") return <ChipDiff added={[]} removed={[]} same={[every]} />;
  if (m.every === "gained") return <ChipDiff added={[every]} removed={[]} same={values(c.before)} />;
  if (m.every === "lost") return <ChipDiff added={[]} removed={[every]} same={values(c.after)} />;
  const added = new Set(values(m.added));
  return <ChipDiff added={[...added]} removed={values(m.dropped)} same={values(c.after).filter((v) => !added.has(v))} />;
}

// coverOf answers a role's coverage entry on a server, or undefined where
// the server pins no type or sent no list, so that row keeps the covers
// line as the file writes it.
type CoverOf = (role: string, app: string) => DraftCoverage | undefined;

function RoleBody({ name, after, before, tools, coverOf }: { name: string; after: RoleView | null; before: RoleView | null; tools: ToolsMove[]; coverOf: CoverOf }) {
  if (!after) return <p className="m-0 text-sm text-text-2">{NOT_READABLE_DOC}</p>;
  const one = new Set([...after.bindings, ...(before?.bindings || [])].map((b) => b.app)).size <= 1;
  if (!before) {
    const rows: [string, React.ReactNode][] = [];
    if (after.description) rows.push([DESCRIPTION, after.description]);
    for (const b of after.bindings) {
      rows.push([toolsOn(b.app, one), <Tools key={b.app} tools={b.tools} />]);
      const c = coverOf(name, b.app);
      if (c) rows.push([objectsLabel(coverageNouns(c)), <Objects key={b.app + "/objects"} c={c} />]);
      else if (b.covers) rows.push([coversOn(b.app), <span key={b.app + "/covers"} className="font-mono text-[13px]">{coversText(b.covers)}</span>]);
    }
    if (after.implies.length) rows.push([IMPLIES, list(after.implies)]);
    return <Facts rows={rows.length ? rows : [[TOOLS_ROW, NOTHING]]} />;
  }
  const changed: [string, string, string][] = [];
  const facts: [string, React.ReactNode][] = [];
  if (before.description !== after.description) changed.push([DESCRIPTION, before.description || NOTHING, after.description || NOTHING]);
  if (list(before.implies) !== list(after.implies)) changed.push([IMPLIES, list(before.implies) || NOTHING, list(after.implies) || NOTHING]);
  // Each server reads as one row of tool chips, then its objects. Where the
  // server sent a coverage entry, the objects are compared by id, so a value
  // written differently is no change. Elsewhere the files carry values, not
  // ids, so a covers line the draft writes differently is a Now and After
  // row.
  for (const m of tools) {
    facts.push([toolsOn(m.server, one), <ChipDiff key={m.server} added={m.added} removed={m.removed} same={m.same} />]);
    const c = coverOf(name, m.server);
    if (c) {
      facts.push([objectsLabel(coverageNouns(c)), <ObjectsDiff key={m.server + "/objects"} c={c} />]);
      continue;
    }
    const was = before.bindings.find((b) => b.app === m.server)?.covers;
    const now = after.bindings.find((b) => b.app === m.server)?.covers;
    if (coversKey(was) !== coversKey(now)) changed.push([coversOn(m.server), coversText(was), coversText(now)]);
  }
  return (
    <>
      {changed.length > 0 && <NowAfter rows={changed} />}
      {facts.length > 0 && <Facts rows={facts} />}
    </>
  );
}

// Edited shows an edited rule: the old sentence struck through above the
// new one, with the words that differ marked on both.
function Edited({ was, now }: { was: string; now: string }) {
  const d = wordDiff(was, now);
  const join = (parts: React.ReactNode[]) => parts.flatMap((p, i) => (i ? [" ", p] : [p]));
  return (
    <>
      <span className="block text-muted-foreground line-through" data-was>
        {join(d.was.map((p, i) => (p.changed ? <del key={i} className="rounded-sm bg-danger-bg px-0.5 text-danger">{p.text}</del> : p.text)))}
      </span>
      <span className="block" data-now>
        {join(d.now.map((p, i) => (p.changed ? <ins key={i} className="rounded-sm bg-ok-bg px-0.5 text-ok no-underline">{p.text}</ins> : p.text)))}
      </span>
    </>
  );
}

function SetBody({ sides }: { sides: Sides }) {
  const rules = ruleMarks(sides.before ? sides.before.doc : null, sides.after.doc);
  return (
    <>
      {sides.after.op === "off" && <p className="m-0 text-sm text-warn">{OFF_LINE}</p>}
      {rules.length > 0 && (
        <table className="w-full table-fixed border-collapse overflow-hidden rounded-md border border-border text-sm">
          <colgroup><col style={{ width: "30%" }} /><col /><col style={{ width: "96px" }} /></colgroup>
          <thead className="border-b border-border bg-secondary/40"><tr><th className={TH}>{RULE_COLUMN.rule}</th><th className={TH}>{RULE_COLUMN.does}</th><th className={TH}><span className="sr-only">{RULE_COLUMN.rule}</span></th></tr></thead>
          <tbody>
            {rules.map(({ rule, mark, was }) => (
              // The sentence is what the row says, so it reads at body size. The
              // cells share its baseline, which puts the name and the mark on
              // its first line.
              <tr key={rule.id + mark} className="border-b border-border last:border-b-0" data-rule={rule.id}>
                <td className={cn(TD, "break-all align-baseline font-mono text-[13px]")}>{rule.id}</td>
                <td className={cn(TD, "align-baseline text-base", mark === "removed" ? "text-muted-foreground line-through" : "text-foreground")}>
                  {was ? <Edited was={sentence(was)} now={sentence(rule)} /> : sentence(rule)}
                </td>
                <td className={cn(TD, "align-baseline")}>{mark !== "same" && <DraftChip word={RULE_MARK[mark]} tone={mark === "new" ? "accent" : mark === "removed" ? "danger" : "warn"} attr="data-rule-mark" />}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

// LiveLink opens the live page of the object a card names. A server's and a
// role's pages are keyed by id, so it reads the list to find the id, and
// says so when the list cannot be read or no longer holds the object.
function LiveLink({ item }: { item: DraftItem }) {
  const [busy, setBusy] = React.useState(false);
  const open = async () => {
    if (item.kind === "PolicySet") { navigate("policies", [item.name]); return; }
    const kind = item.kind;
    setBusy(true);
    try {
      const rows: { id: string; name: string }[] = kind === "App" ? await listApps() : await listRoles();
      const row = rows.find((r) => r.name === item.name);
      if (row) navigate(kind === "App" ? "servers" : "roles", [row.id]);
      else notify.failed(notLive(item.name));
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) notify.failed(readFailed(LIVE_LIST[kind], err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Button variant="link" size="sm" className="ml-auto h-auto p-0 text-link" disabled={busy} onClick={() => void open()} data-open-live={item.name}>
      {openLive(item.name)}
    </Button>
  );
}

function ItemCard({ facts: f, detail, implied, coverOf }: { facts: ItemFacts; detail: DraftDetail; implied: string[]; coverOf: CoverOf }) {
  const { item, sides } = f;
  const open = detail.draft.state !== "published";
  let body: React.ReactNode;
  if (item.withheld) body = <p className="m-0 text-sm text-text-2">{item.withheld}</p>;
  else if (sides.after.op === "remove") body = <><p className="m-0 text-sm text-text-2">{open ? REMOVED_LINE : REMOVED_LINE_PAST}</p><Implied objects={implied} title={GOES_WITH} /></>;
  else if (item.kind === "App") body = <AppBody item={item} sides={sides} detail={detail} open={open} />;
  else if (item.kind === "Role") body = <RoleBody name={item.name} after={f.role} before={f.before} tools={f.tools} coverOf={coverOf} />;
  else body = <SetBody sides={sides} />;
  const kind = f.role ? roleKindWords(f.role.kind, f.owner) : f.kind;
  return (
    <article id={cardId(f.object)} tabIndex={-1} className="scroll-mt-40 rounded-md border border-border bg-card outline-none focus-visible:ring-2 focus-visible:ring-ring/50" data-item={f.object}>
      <header className="flex flex-wrap items-center gap-2.5 border-b border-border px-3.5 py-2">
        <span className="inline-flex items-center gap-1.5 text-sm text-muted-foreground" data-kind={kind}><KindIcon kind={item.kind} />{kind}</span>
        <span className="font-mono text-[15px] font-semibold text-foreground">{item.name}</span>
        <DraftChip word={MARK[f.what]} tone={MARK_TONE[f.what]} attr="data-mark" />
        <HoldersChip n={f.holders} />
        {f.outcomes && <DraftChip word={f.outcomes} tone="plain" attr="data-outcomes" />}
        <SummaryChips chips={f.summary} />
        {f.live && <LiveLink item={item} />}
      </header>
      <div className="flex flex-col gap-2.5 px-3.5 py-2.5">{body}</div>
      {item.doc && !item.withheld && (
        <details className="border-t border-border px-3.5 py-2" data-file>
          <summary className="cursor-pointer text-[13px] text-muted-foreground hover:text-foreground">{fileFold(item.name, detail.draft.state)}</summary>
          <pre className="mt-1.5 mb-1 overflow-x-auto rounded-md border border-border bg-secondary/40 px-3 py-2 font-mono text-[12.5px] leading-relaxed text-foreground">{item.doc}</pre>
          <p className="m-0 text-[13px] text-muted-foreground">{FILE_MASKED}</p>
        </details>
      )}
    </article>
  );
}

export function DraftChanges({ detail }: { detail: DraftDetail }) {
  const items = detail.draft.items;
  const implied = impliedOf(detail);
  const removals = items.filter((it) => it.op === "remove").length;
  const coverOf: CoverOf = (role, app) => coverageFor(detail.verdict, role, app);
  return (
    <div className="flex flex-col gap-3" data-changes>
      {items.map((it) => <ItemCard key={objectOf(it)} facts={itemFacts(detail, it)} detail={detail} implied={removals === 1 ? implied : []} coverOf={coverOf} />)}
      {removals > 1 && implied.length > 0 && (
        <div className="rounded-md border border-border bg-card px-3.5 py-2.5"><Implied objects={implied} title={GOES_WITH_ALL} /></div>
      )}
    </div>
  );
}
