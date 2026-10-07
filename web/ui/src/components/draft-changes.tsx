import type * as React from "react";
import { DraftChip, type ChipTone } from "@/components/draft-checks";
import type { DraftDetail, DraftItem } from "@/lib/api";
import { type ChangeWhat, type RoleView, type Sides, appFacts, appRows, changeCounts, hostOf, impliedOf, manifestOf, objectOf, remoteApp, roleOf, roleToolRows, ruleMarks, sidesOf, whatOf } from "@/lib/drafts-model";
import {
  DESCRIPTION,
  GOES_WITH,
  GOES_WITH_ALL,
  IMPLIES,
  ITEM_KIND,
  JOINS,
  LEAVES,
  MARK,
  NOTHING,
  NOTHING_STARTS,
  NOT_READABLE_DOC,
  NOT_REACHED,
  NOW_AFTER,
  NO_FIELD_CHANGE,
  OFF_LINE,
  REACHED,
  REMOVED_LINE,
  REMOVED_LINE_PAST,
  RULE_COLUMN,
  RULE_MARK,
  TOOLS_NOT_READ,
  TOOLS_ROW,
  TOOL_COLUMN,
  changesLede,
  changesLedePublished,
  contactedLine,
  objectWords,
  reachesOn,
  roleKindWords,
  setFold,
  toolsWords,
} from "@/lib/drafts-words";
import { list, sentence } from "@/lib/policy-words";
import { absTime } from "@/lib/words";
import { cn } from "@/lib/utils";

// What changes: one card per item, in the shapes the
// server, role and policy pages already use. A new server reads its facts,
// a changed one the Now and After rows of the change sheets, a role its
// reach per tool, and an approval set its rules in the policy words with a
// mark. An open draft is read against live state, a published one against
// its change record (sidesOf). Every document is text: no link and no
// image from a draft ever renders.

const MARK_TONE: Record<ChangeWhat, ChipTone> = { new: "accent", changed: "warn", off: "warn", removed: "danger" };
const TH = "px-3 py-1.5 text-left text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground";
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
    ? [TOOLS_ROW, <span key="t" className="flex flex-col gap-0.5"><span className="font-mono text-[13px]">{seen.tools.join(", ")}</span><span className="text-[13px] text-muted-foreground">{contactedLine(host, absTime(seen.at), seen.tools.length)}</span></span>]
    : sides.before || !open ? null : [TOOLS_ROW, <DraftChip key="t" word={TOOLS_NOT_READ} tone="unknown" />];
  if (sides.before) {
    const rows = appRows(manifestOf(sides.before.doc) || {}, m);
    return (
      <>
        {rows.length ? <NowAfter rows={rows} /> : <p className="m-0 text-sm text-text-2">{NO_FIELD_CHANGE}</p>}
        {tools && <Facts rows={[tools]} />}
      </>
    );
  }
  return (
    <>
      <Facts rows={[...appFacts(m), ...(tools ? [tools] : [])]} />
      {open && <p className="m-0 text-[13px] text-muted-foreground">{NOTHING_STARTS}</p>}
    </>
  );
}

function RoleBody({ after, before }: { after: RoleView | null; before: RoleView | null }) {
  if (!after) return <p className="m-0 text-sm text-text-2">{NOT_READABLE_DOC}</p>;
  if (!before) {
    const rows: [string, React.ReactNode][] = [];
    if (after.description) rows.push([DESCRIPTION, after.description]);
    for (const b of after.bindings) rows.push([reachesOn(b.app), <span key={b.app} className="font-mono text-[13px]">{toolsWords(b.tools)}</span>]);
    if (after.implies.length) rows.push([IMPLIES, list(after.implies)]);
    return <Facts rows={rows.length ? rows : [[reachesOn(after.server || NOTHING), NOTHING]]} />;
  }
  const changed: [string, string, string][] = [];
  if (before.description !== after.description) changed.push([DESCRIPTION, before.description || NOTHING, after.description || NOTHING]);
  if (list(before.implies) !== list(after.implies)) changed.push([IMPLIES, list(before.implies) || NOTHING, list(after.implies) || NOTHING]);
  const tools = roleToolRows(before, after);
  return (
    <>
      {changed.length > 0 && <NowAfter rows={changed} />}
      {tools.length > 0 && (
        <table className="w-full table-fixed border-collapse overflow-hidden rounded-md border border-border text-sm">
          <thead className="border-b border-border bg-secondary/40"><tr><th className={TH}>{TOOL_COLUMN(tools[0].server)}</th><th className={TH}>{NOW_AFTER.now}</th><th className={TH}>{NOW_AFTER.after}</th></tr></thead>
          <tbody>
            {tools.map((t) => (
              <tr key={t.server + "/" + t.tool} className="border-b border-border last:border-b-0" data-tool={t.tool}>
                <td className={cn(TD, "font-mono text-[13px]")}>{t.tool === "*" ? toolsWords(["*"]) : t.tool}</td>
                <td className={cn(TD, "text-muted-foreground")}>{t.before ? REACHED : NOT_REACHED}</td>
                <td className={TD}>
                  {t.before === t.after ? (t.after ? REACHED : NOT_REACHED) : <DraftChip word={t.after ? JOINS : LEAVES} tone={t.after ? "accent" : "danger"} />}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

function SetBody({ item, sides }: { item: DraftItem; sides: Sides }) {
  const rules = ruleMarks(sides.before ? sides.before.doc : null, sides.after.doc);
  return (
    <>
      {sides.after.op === "off" && <p className="m-0 text-sm text-warn">{OFF_LINE}</p>}
      {rules.length > 0 && (
        <table className="w-full table-fixed border-collapse overflow-hidden rounded-md border border-border text-sm">
          <colgroup><col style={{ width: "30%" }} /><col /><col style={{ width: "96px" }} /></colgroup>
          <thead className="border-b border-border bg-secondary/40"><tr><th className={TH}>{RULE_COLUMN.rule}</th><th className={TH}>{RULE_COLUMN.does}</th><th className={TH}><span className="sr-only">{RULE_COLUMN.rule}</span></th></tr></thead>
          <tbody>
            {rules.map(({ rule, mark }) => (
              // The sentence is what the row says, so it reads at body size. The
              // cells share its baseline, which puts the name and the mark on
              // its first line.
              <tr key={rule.id + mark} className="border-b border-border last:border-b-0" data-rule={rule.id}>
                <td className={cn(TD, "break-all align-baseline font-mono text-[13px]")}>{rule.id}</td>
                <td className={cn(TD, "align-baseline text-base", mark === "removed" ? "text-muted-foreground line-through" : "text-foreground")}>{sentence(rule)}</td>
                <td className={cn(TD, "align-baseline")}>{mark !== "same" && <DraftChip word={RULE_MARK[mark]} tone={mark === "new" ? "accent" : mark === "removed" ? "danger" : "warn"} attr="data-rule-mark" />}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <details>
        <summary className="cursor-pointer text-[13px] text-muted-foreground hover:text-foreground">{setFold(item.name)}</summary>
        <pre className="mt-1 overflow-x-auto rounded-md border border-border bg-secondary/40 px-3 py-2 font-mono text-[12.5px] leading-relaxed text-foreground">{sides.after.doc}</pre>
      </details>
    </>
  );
}

function ItemCard({ item, detail, implied }: { item: DraftItem; detail: DraftDetail; implied: string[] }) {
  const object = objectOf(item);
  const open = detail.draft.state !== "published";
  const sides = sidesOf(detail, item);
  const what = whatOf({ op: sides.after.op, existed: !!sides.before });
  const role = item.kind === "Role" ? roleOf(sides.after.doc || sides.before?.doc) : null;
  const kind = item.kind === "Role" ? roleKindWords(role?.kind || "", role?.server || "") : ITEM_KIND[item.kind];
  let body: React.ReactNode;
  if (item.withheld) body = <p className="m-0 text-sm text-text-2">{item.withheld}</p>;
  else if (sides.after.op === "remove") body = <><p className="m-0 text-sm text-text-2">{open ? REMOVED_LINE : REMOVED_LINE_PAST}</p><Implied objects={implied} title={GOES_WITH} /></>;
  else if (item.kind === "App") body = <AppBody item={item} sides={sides} detail={detail} open={open} />;
  else if (item.kind === "Role") body = <RoleBody after={role} before={sides.before ? roleOf(sides.before.doc) : null} />;
  else body = <SetBody item={item} sides={sides} />;
  return (
    <article className="rounded-md border border-border bg-card" data-item={object}>
      <header className="flex flex-wrap items-center gap-2.5 border-b border-border px-3.5 py-2">
        <span className="text-[13px] text-muted-foreground">{kind}</span>
        <span className="font-mono text-[15px] font-semibold text-foreground">{item.name}</span>
        <DraftChip word={MARK[what]} tone={MARK_TONE[what]} attr="data-mark" />
      </header>
      <div className="flex flex-col gap-2.5 px-3.5 py-2.5">{body}</div>
    </article>
  );
}

export function DraftChanges({ detail }: { detail: DraftDetail }) {
  const items = detail.draft.items;
  const implied = impliedOf(detail);
  const removals = items.filter((it) => it.op === "remove").length;
  const counts = changeCounts(items.map((it) => { const sd = sidesOf(detail, it); return { kind: it.kind, op: sd.after.op, existed: !!sd.before }; }));
  return (
    <div className="flex flex-col gap-3" data-changes>
      <p className="m-0 max-w-[75ch] text-sm text-text-2">{detail.draft.state === "published" ? changesLedePublished(counts) : changesLede(counts)}</p>
      {items.map((it) => <ItemCard key={objectOf(it)} item={it} detail={detail} implied={removals === 1 ? implied : []} />)}
      {removals > 1 && implied.length > 0 && (
        <div className="rounded-md border border-border bg-card px-3.5 py-2.5"><Implied objects={implied} title={GOES_WITH_ALL} /></div>
      )}
    </div>
  );
}
