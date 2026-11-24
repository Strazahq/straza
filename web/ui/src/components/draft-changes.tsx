import * as React from "react";
import { CodeXmlIcon } from "lucide-react";
import { ChangeChip, Chip, Pill } from "@/components/chip";
import { NameChips } from "@/components/draft-checks";
import { Arrow, Facts } from "@/components/draft-parts";
import { RoleBody } from "@/components/draft-role-card";
import { SetBody } from "@/components/draft-set-card";
import { KindTile } from "@/components/kind-icon";
import { Button } from "@/components/ui/button";
import { type ApiError, type DraftDetail, type DraftItem, listApps, listRoles } from "@/lib/api";
import { coverageFor } from "@/lib/drafts-access";
import { type ItemFacts, cardId, itemFacts } from "@/lib/drafts-items";
import { type Sides, appFacts, appRows, hostOf, iconKind, impliedOf, manifestOf, objectOf, remoteApp, roleOf, secretAsks } from "@/lib/drafts-model";
import { setReadings } from "@/lib/drafts-rules";
import { impliedSplit } from "@/lib/drafts-short";
import {
  KIND_PILL,
  LIVE_LIST,
  MARK,
  NOT_READABLE_DOC,
  SHORT_ALSO,
  NO_FIELD_CHANGE,
  REMOVED_LINE,
  REMOVED_LINE_PAST,
  SECRET_LINE,
  SECRET_LINE_BOTH,
  SECRET_ROW,
  TOOLS_NOT_READ,
  TOOLS_ROW,
  VIEW_YAML,
  contactedLine,
  notLive,
  objectWords,
  openLive,
  ownedBy,
  roleKindPill,
} from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { readFailed } from "@/lib/say";
import { absTime } from "@/lib/words";

// What changes: one card per item, its kind and change in the header with
// View as YAML and the link to its live page, and a facts grid under it. A
// server reads in the server pages' words, a role says where it reaches, a
// policy set says who it applies to and lists its policy rules. An open
// draft is read against live state, a published one against its change
// record (sidesOf). Every document is text: no link and no image from a
// draft ever renders.

// Changed is a field a draft writes differently: the value now, the arrow,
// and the value after publishing.
const Changed = ({ was, now }: { was: string; now: string }) => (
  <><span className="break-words text-muted-foreground" data-was>{was}</span><Arrow /><span className="break-words" data-now>{now}</span></>
);

function Implied({ objects, title }: { objects: string[]; title: string }) {
  if (!objects.length) return null;
  return (
    <div className="flex flex-col gap-1 text-dense">
      <span className="font-semibold text-foreground">{title}</span>
      <ul className="m-0 list-disc pl-5 text-text-2">{objects.map((o) => <li key={o}>{objectWords(o)}</li>)}</ul>
    </div>
  );
}

const Note = ({ children, ...data }: { children: React.ReactNode; [key: `data-${string}`]: boolean | undefined }) => <p className="m-0 text-caption text-muted-foreground" {...data}>{children}</p>;

function AppBody({ item, sides, detail, open, remoteRps }: { item: DraftItem; sides: Sides; detail: DraftDetail; open: boolean; remoteRps: number | null }) {
  const m = manifestOf(sides.after.doc);
  if (!m) return <p className="m-0 text-dense text-text-2">{NOT_READABLE_DOC}</p>;
  const seen = (detail.contacted || {})[objectOf(item)];
  const url = m.straza?.runtime?.remote?.url || "";
  const host = hostOf(url, item.name);
  const tools = !remoteApp(sides.after.doc) ? null : seen
    ? { label: TOOLS_ROW, value: <span className="flex flex-col gap-1"><NameChips names={seen.tools} /><span className="text-caption text-muted-foreground">{contactedLine(host, absTime(seen.at), seen.tools.length)}</span></span> }
    : sides.before || !open ? null : { label: TOOLS_ROW, value: <Chip tone="unknown">{TOOLS_NOT_READ}</Chip> };
  // An open draft whose check warns that the server has no secret says so
  // on its card, and that the publish dialog asks for it, with the listing
  // credential when that is missing too.
  const ask = detail.draft.state === "open" ? secretAsks(detail.draft, detail.verdict).find((a) => a.name === item.name && a.secret) : undefined;
  const secret = ask ? { label: SECRET_ROW, value: <span className="text-warn" data-secret-row>{ask.listing ? SECRET_LINE_BOTH : SECRET_LINE}</span> } : null;
  if (sides.before) {
    const rows = appRows(manifestOf(sides.before.doc) || {}, m, remoteRps);
    const facts = [...rows.map(([label, was, now]) => ({ label, value: <Changed was={was} now={now} /> })), secret, tools].filter((r) => r !== null);
    return (
      <>
        {facts.length > 0 && <Facts rows={facts} />}
        {!rows.length && <Note data-no-other>{NO_FIELD_CHANGE}</Note>}
      </>
    );
  }
  const facts = appFacts(m, remoteRps).map(([label, value]) => ({ label, value: <span className="min-w-0 break-words">{value}</span> as React.ReactNode }));
  if (secret) facts.splice(facts.findIndex((r) => r.label === "Sent as") + 1, 0, secret);
  return <Facts rows={[...facts, ...(tools ? [tools] : [])]} />;
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
    <Button variant="outline" size="sm" disabled={busy} onClick={() => void open()} data-open-live={item.name}>
      {openLive(item.name)}
    </Button>
  );
}

// Tile is the kind tile of a card, and the Roles icon on the same square
// for a role whose kind the page does not know.
function Tile({ kind }: { kind: ReturnType<typeof iconKind> }) {
  if (kind) return <KindTile kind={kind} size="md" />;
  const Icon = routeByKey("roles").icon;
  return <span className="inline-grid size-7 shrink-0 place-items-center rounded-md bg-secondary text-foreground"><Icon className="size-4" aria-hidden="true" /></span>;
}

type Props = {
  detail: DraftDetail;
  // registered is the registered server list after publishing, null where
  // the page cannot tell a server on a person's machine apart.
  registered: string[] | null;
  // holders is how many hold a role, null where the page does not know.
  holders: (role: string) => number | null;
  // onYaml opens the YAML sheet on an item's file, by its Kind/Name.
  onYaml: (name: string) => void;
  // remoteRps is the rate limit a remote server without its own gets, null
  // where the config did not name it.
  remoteRps: number | null;
};

export function DraftChanges({ detail, registered, holders, onYaml, remoteRps }: Props) {
  const d = detail.draft;
  const open = d.state !== "published";
  const implied = impliedOf(detail);
  // What the removals take along reads in two lists, what goes and what
  // only changes, under the words In short uses for them.
  const split = impliedSplit(detail);
  const tense = open ? 0 : 1;
  const along = (
    <>
      <Implied objects={split.gone.map((a) => a.object)} title={SHORT_ALSO.removed[tense]} />
      <Implied objects={split.changed.map((a) => a.object)} title={SHORT_ALSO.changed[tense]} />
    </>
  );
  const removals = d.items.filter((it) => it.op === "remove").length;
  const sets = setReadings(detail);
  const roleKind = (role: string) => {
    const it = d.items.find((x) => x.kind === "Role" && x.name === role);
    return it ? iconKind("Role", roleOf(it.doc)) : null;
  };
  const card = (f: ItemFacts) => {
    const { item, sides } = f;
    let body: React.ReactNode;
    if (item.withheld) body = <p className="m-0 text-dense text-text-2">{item.withheld}</p>;
    else if (item.kind === "PolicySet") body = <SetBody what={f.what} reading={sets.find((x) => x.item === item) || null} past={!open} ctx={{ registered, roleKind }} />;
    else if (sides.after.op === "remove") body = <><p className="m-0 text-dense text-text-2">{open ? REMOVED_LINE : REMOVED_LINE_PAST}</p>{removals === 1 && along}</>;
    else if (item.kind === "App") body = <AppBody item={item} sides={sides} detail={detail} open={open} remoteRps={remoteRps} />;
    else {
      body = (
        <RoleBody
          name={item.name} after={f.role} before={f.before} tools={f.tools}
          coverOf={(role, app) => coverageFor(detail.verdict, role, app)}
          holders={holders(item.name)}
          gains={d.state === "open" ? (detail.verdict.gains || []).filter((g) => g.role === item.name) : []}
          sets={sets} registered={registered}
        />
      );
    }
    const kindPill = item.kind === "Role" ? (f.role ? roleKindPill(f.role.kind) : KIND_PILL.Role) : KIND_PILL[item.kind];
    return (
      <article key={f.object} id={cardId(f.object)} tabIndex={-1} className="scroll-mt-40 rounded-md border border-border bg-card outline-none focus-visible:ring-2 focus-visible:ring-ring/50" data-item={f.object}>
        <header className="flex flex-wrap items-center gap-x-2.5 gap-y-2 border-b border-border px-4 py-3">
          <Tile kind={iconKind(item.kind, f.role)} />
          <h3 className="m-0 min-w-0 font-mono text-body font-semibold break-all text-foreground">{item.name}</h3>
          <Pill data-kind={kindPill}>{kindPill}</Pill>
          {f.owner && <Pill data-owner={f.owner}>{ownedBy(f.owner)}</Pill>}
          <ChangeChip change={f.what} data-mark={MARK[f.what]} />
          <span className="flex w-full flex-wrap items-center gap-2 sm:ml-auto sm:w-auto">
            {item.doc && !item.withheld && <Button variant="ghost" size="sm" onClick={() => onYaml(f.object)} data-view-yaml={f.object}><CodeXmlIcon />{VIEW_YAML}</Button>}
            {f.live && <LiveLink item={item} />}
          </span>
        </header>
        <div className="flex flex-col gap-3 p-4">{body}</div>
      </article>
    );
  };
  return (
    <div className="flex flex-col gap-3" data-changes>
      {d.items.map((it) => card(itemFacts(detail, it)))}
      {removals > 1 && implied.length > 0 && (
        <div className="flex flex-col gap-3 rounded-md border border-border bg-card px-4 py-3">{along}</div>
      )}
    </div>
  );
}
