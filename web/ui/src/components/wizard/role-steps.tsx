import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { AccessEditor } from "@/components/access-editor";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { KindGlyph, RoleKindBadge } from "@/components/role-kind";
import { ListRow, Section } from "@/components/sheet-parts";
import { CAPS, CardGroup, Code, Field, Fold, HINT, OptionCard, ProblemBlock } from "@/components/wizard/parts";
import { type Choice, type FixedRule, type Plan, choiceOf, emptyPlan, grantMatchers, inGrant, policyWord } from "@/lib/access-plan";
import type { AppRow, PackRow, RoleRow, ToolRow } from "@/lib/api";
import {
  AFTER_CREATE_HELP,
  APPROVER_NEXT,
  COMMANDS_HELP,
  COMPOSES_ROW,
  COMPOSE_HELP,
  DESCRIPTION_OPTIONAL,
  EDITOR_HEAD,
  HOLDERS_REACH,
  type Kind,
  KIND_GROUP,
  NAME_FREE,
  NAME_HELP,
  NAME_LABEL,
  NAME_MISSING,
  NOTHING_BOUND,
  NOTHING_COMPOSED,
  NO_COMPOSABLE,
  NO_DESCRIPTION,
  NO_PACK_PICKED,
  NO_SERVER_WITH_TOOLS,
  NO_TOOL_DESCRIPTION,
  ONE_SERVER_HELP,
  OPEN_IT,
  PACKS_AFTER,
  PACKS_ROW,
  PICK_ANOTHER,
  PICK_A_TOOL,
  POLICY_HELP,
  RAIL_EVERY,
  RAIL_SERVER,
  RAIL_TITLE,
  REACH_EMPTY,
  REACH_UNREAD,
  REVIEW_COMMANDS,
  REVIEW_HAPPEN,
  REVIEW_REACH,
  SERVER_MISSING,
  STATE_WORD,
  STRAZA_NOT_HERE,
  STRAZA_NOT_HERE_HELP,
  UNREACHABLE_STEP,
  WIZARD_KINDS,
  accessOpensOn,
  accessReaches,
  accessSetName,
  fixedWords,
  finishOn,
  halfLanded,
  isComposable,
  isGlob,
  kindCard,
  list,
  nameCheck,
  noToolsKnown,
  noToolsWhy,
  liveRole,
  packSwitch,
  packVersion,
  railCount,
  railLabel,
  reachPill,
  reviewCount,
  reviewEvery,
  roleTick,
  switchWarning,
  tools as toolCount,
} from "@/lib/role-words";
import { NAME_SUFFIX, foldName, openServer, previewParts, roleNameOf, rolePrefix, serverPrefixCheck, serverPrefixRefusal } from "@/lib/server-roles-words";
import { cn } from "@/lib/utils";

// The step bodies of the New role wizard. The screen holds the draft and
// runs the commit; every step
// here only draws what the draft says and hands a change back.

// Draft is the role the wizard is writing: the answers of every step, kept
// while the person walks back and forth between them.
export type Draft = {
  kind: Kind;
  // name is what the name field holds: the whole name of a business or an
  // approver role, the word after the server's prefix of an application role.
  name: string;
  description: string;
  // server is the one MCP server the role reaches. plans holds one access
  // plan per server name, so ticks survive a switch to another server and
  // back; only the picked server's plan is ever written.
  server: string;
  plans: Record<string, Plan>;
  composed: string[];
  packs: string[];
};

// Act is one row of the run: what it does, the state it landed in, and the
// tab of the role's page that finishes it when it did not.
export type Act = { id: string; label: string; state: "pending" | "running" | "done" | "failed" | "refused"; section?: string; subject?: string; error?: string };

// Pill is one server a composed role reaches, with the role it arrives
// through.
export type Pill = { server: string; via: string; tools: number };

const ACT_HUE: Record<Act["state"], string> = {
  pending: "text-muted-foreground",
  running: "text-muted-foreground",
  done: "text-ok",
  failed: "text-danger",
  refused: "text-danger",
};

const CHOICE_HUE: Record<Choice, string> = { allow: "text-text-2", approve: "text-warn", deny: "text-danger" };

export const planOf = (draft: Draft, app: string): Plan => draft.plans[app] || emptyPlan();
export const toolNames = (tools: ToolRow[]) => tools.map((t) => t.name);

// storedName is the name the role is stored under: an application role's
// is its server's prefix and the typed word folded, empty until both are
// there, and any other role's is the name as typed.
export function storedName(draft: Draft): string {
  if (draft.kind !== "application") return draft.name.trim();
  return draft.server && foldName(draft.name) ? roleNameOf(draft.server, draft.name) : "";
}

const CODE = "rounded bg-muted px-1 font-mono text-[13px] text-foreground";

// Acts lists the run's rows: numbered before it runs, then the state word
// each row landed in.
export function Acts({ acts, numbered }: { acts: Act[]; numbered?: boolean }) {
  return (
    <ol className="m-0 flex list-none flex-col gap-1.5 p-0 text-sm" data-landed>
      {acts.map((a, i) => (
        <li key={a.id} className="flex items-baseline gap-2.5">
          <span className={cn("w-16 shrink-0 font-mono text-xs", numbered ? "text-muted-foreground" : ACT_HUE[a.state])} data-act={a.id}>
            {numbered ? i + 1 : STATE_WORD[a.state]}
          </span>
          <span className="text-foreground">{a.label}</span>
        </li>
      ))}
    </ol>
  );
}

type NameProps = {
  draft: Draft;
  // roles is the list the name is checked against, null when it could not
  // be read.
  roles: RoleRow[] | null;
  // apps are the servers the browser holds: an application role picks the
  // one it reaches among them, and any other role's name keeps out of their
  // prefixes, since a role inside one is made on that server's page.
  apps: AppRow[];
  // read says the servers and their tools were read, so an empty list means
  // no server serves a tool rather than a read still on its way.
  read: boolean;
  toolsOf: (app: string) => ToolRow[];
  // problem says why the servers list could not be read.
  problem: string | null;
  // fromServer is the server a door opened the wizard on, said here so
  // the person knows why it is picked.
  fromServer?: string;
  miss: "name" | "refused" | "server" | null;
  // nameBox lets the footer's primary focus the name it is missing.
  nameBox: React.Ref<HTMLInputElement>;
  onKind: (kind: Kind) => void;
  onPick: (app: string) => void;
  onName: (name: string) => void;
  onDescription: (text: string) => void;
  onOpenExact: (role: RoleRow) => void;
  onOpenServer: (app: AppRow) => void;
};

// NameStep is the first step: the kind, then for an application role the
// one server it reaches and the name that server's prefix opens, then for
// every role the description. A business or an approver role keeps a free
// name, kept out of every server's prefix.
export function NameStep({ draft, roles, apps, read, toolsOf, problem, fromServer, miss, nameBox, onKind, onPick, onName, onDescription, onOpenExact, onOpenServer }: NameProps) {
  const owned = draft.kind === "application";
  const stored = storedName(draft);
  // A server's prefix is that server's alone, and the server refuses the
  // name at create, so the check says it here with the way to it. An
  // application role's name opens with its own server's prefix.
  const owner = owned ? null : serverPrefixCheck(draft.name, apps);
  const check = nameCheck(stored, roles, owned && draft.server ? rolePrefix(draft.server) : "");
  const exact = check && check.exact;
  const hue = check ? (check.level === "error" ? "text-danger" : check.level === "warn" ? "text-warn" : "text-muted-foreground") : "";
  const preview = previewParts(draft.server ? roleNameOf(draft.server, draft.name) : "");
  return (
    <div className="flex max-w-[900px] flex-col gap-4">
      {fromServer && owned && draft.server === fromServer && (
        <p className="m-0 text-sm text-text-2" data-from-server>{accessOpensOn(fromServer)}</p>
      )}
      <CardGroup label={KIND_GROUP} value={draft.kind} onPick={(v) => onKind(v as Kind)} className="sm:grid-cols-3">
        {WIZARD_KINDS.map((k) => (
          <OptionCard key={k.kind} value={k.kind} label={kindCard(k.kind)} lead={<KindGlyph mark={k.kind} />} name={k.name} tag={k.kind} line={k.line} on={draft.kind === k.kind} />
        ))}
      </CardGroup>
      <p className={cn(HINT, "m-0 flex items-center gap-1.5")}>
        {STRAZA_NOT_HERE}
        <HelpTip label={STRAZA_NOT_HERE} text={STRAZA_NOT_HERE_HELP} />
      </p>
      {owned && <ServerRail draft={draft} apps={apps} read={read} toolsOf={toolsOf} problem={problem} missing={miss === "server"} onPick={onPick} />}
      <Field id="nr-name" label={NAME_LABEL} error={miss === "name" ? NAME_MISSING : miss === "refused" ? PICK_ANOTHER : undefined} hint={NAME_HELP}>
        {owned ? (
          <div className="flex w-[420px] max-w-full items-center rounded-md border border-border bg-background">
            <span className="pl-2.5 font-mono text-[13px] whitespace-nowrap text-muted-foreground" data-name-prefix>{draft.server ? rolePrefix(draft.server) : ""}</span>
            <Input
              id="nr-name"
              ref={nameBox}
              aria-label={NAME_SUFFIX}
              autoFocus
              spellCheck={false}
              value={draft.name}
              onChange={(e) => onName(e.target.value)}
              className="h-9 border-0 bg-transparent font-mono shadow-none focus-visible:ring-0"
            />
          </div>
        ) : (
          <Input id="nr-name" ref={nameBox} autoFocus value={draft.name} onChange={(e) => onName(e.target.value)} className="w-[360px] max-w-full font-mono" />
        )}
        {owner ? (
          <p className="m-0 max-w-[75ch] text-[13px] leading-snug text-danger" data-name-check="server">
            {serverPrefixRefusal(owner.name)}
            <Button variant="link" size="sm" className="ml-1.5 h-auto p-0 text-[13px]" onClick={() => onOpenServer(owner)}>{openServer(owner.name)}</Button>
          </p>
        ) : check ? (
          <p className={cn("m-0 max-w-[75ch] text-[13px] leading-snug", hue)} data-name-check={check.level}>
            {check.text}
            {exact && (
              <Button variant="link" size="sm" className="ml-1.5 h-auto p-0 text-[13px]" onClick={() => onOpenExact(exact)}>{OPEN_IT}</Button>
            )}
          </p>
        ) : stored ? (
          <p className="m-0 text-[13px] text-ok" data-name-check="free">{NAME_FREE}</p>
        ) : null}
        {owned && draft.server && (
          <p className={cn(HINT, "m-0")} data-name-preview>
            {preview.lead}<span className={CODE}>{preview.stored}</span>{preview.mid}<span className={CODE}>{preview.ar}</span>{preview.end}
          </p>
        )}
      </Field>
      <Field id="nr-description" label={DESCRIPTION_OPTIONAL}>
        <Input id="nr-description" value={draft.description} onChange={(e) => onDescription(e.target.value)} className="w-[520px] max-w-full" />
      </Field>
    </div>
  );
}

type RailProps = {
  draft: Draft;
  apps: AppRow[];
  read: boolean;
  toolsOf: (app: string) => ToolRow[];
  problem: string | null;
  // missing says Next found no server picked.
  missing: boolean;
  onPick: (app: string) => void;
};

// ServerRail is the one server an application role reaches, as a radio
// list: only the picked server shows a tick count, and a server with no
// known tool is listed greyed with its reason. Ticks on another server stay
// in the draft, unshown and never written, and the line under the list
// says so.
function ServerRail({ draft, apps, read, toolsOf, problem, missing, onPick }: RailProps) {
  const miss = missing && <p role="alert" className="m-0 text-[13px] leading-snug text-danger" data-server-miss>{SERVER_MISSING}</p>;
  if (problem) return <><FetchError subject={RAIL_TITLE} detail={problem} />{miss}</>;
  if (!read) return null;
  const withTools = apps.filter((a) => toolsOf(a.name).length > 0);
  if (!withTools.length) return <><p className="m-0 max-w-[75ch] text-sm text-text-2" data-no-server>{NO_SERVER_WITH_TOOLS}</p>{miss}</>;
  const picked = draft.server;
  // ticked counts the tools a server's plan grants: every tool under a glob,
  // otherwise the ticks.
  const ticked = (app: string) => {
    const names = toolNames(toolsOf(app));
    const m = grantMatchers(planOf(draft, app), names);
    if (!m.length) return 0;
    return isGlob(m) ? names.length : m.length;
  };
  const elsewhere = apps.filter((a) => a.name !== picked && ticked(a.name) > 0);
  return (
    <div className="flex flex-col gap-1.5">
      <span className="flex items-center gap-1.5">
        <span className={CAPS}>{RAIL_SERVER}</span>
        <HelpTip label={RAIL_SERVER} text={ONE_SERVER_HELP} />
      </span>
      <div role="radiogroup" aria-label={RAIL_SERVER} className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-1.5">
        {apps.map((a) => {
          const names = toolNames(toolsOf(a.name));
          const plan = planOf(draft, a.name);
          const none = names.length === 0;
          const why = noToolsKnown(noToolsWhy(a));
          const on = a.name === picked;
          const every = on && plan.reach === "later";
          const count = !on ? toolCount(names.length) : every ? RAIL_EVERY : railCount(grantMatchers(plan, names).length, names.length);
          return (
            <button
              key={a.name}
              type="button"
              role="radio"
              aria-label={railLabel(a.name)}
              aria-checked={on}
              aria-disabled={none || undefined}
              title={none ? why : undefined}
              onClick={() => { if (!none) onPick(a.name); }}
              className={cn(
                "flex flex-col gap-0.5 rounded-md border px-2.5 py-2 text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
                on ? "border-link bg-accent-bg" : "border-border bg-card",
                none && "cursor-not-allowed opacity-60",
              )}
            >
              <span className="flex items-center gap-2">
                <span className="truncate font-mono text-[13px] text-foreground">{a.name}</span>
                {!none && <span className={cn("ml-auto shrink-0 font-mono text-xs", every ? "text-warn" : on ? "text-foreground" : "text-muted-foreground")}>{count}</span>}
              </span>
              {none && <span className="text-[13px] leading-snug text-muted-foreground">{why}</span>}
            </button>
          );
        })}
      </div>
      {elsewhere.map((a) => (
        <p key={a.name} role="status" className="m-0 max-w-[80ch] text-[13px] leading-snug text-warn" data-switch-warning={a.name}>
          {switchWarning(ticked(a.name), a.name, picked)}
        </p>
      ))}
      {miss}
    </div>
  );
}

type AccessProps = {
  draft: Draft;
  // app is the server picked on the first step.
  app: AppRow;
  toolsOf: (app: string) => ToolRow[];
  onPlan: (update: (p: Plan) => Plan) => void;
  // fixed are the rules of other sets that decide a tool of the picked
  // server, from the server's own preview of the draft grant.
  fixed: Record<string, FixedRule>;
  approvers: RoleRow[];
  roleName: string;
  // readOnly is set for a session that may not publish policy.
  readOnly: boolean;
  // missing says Next found no tool given.
  missing: boolean;
};

// AccessStep is the tools of the one server picked on the first step: the
// line that names it and where it changes, then the access editor.
export function AccessStep({ draft, app, toolsOf, onPlan, fixed, approvers, roleName, readOnly, missing }: AccessProps) {
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <p className="m-0 max-w-[80ch] text-sm text-text-2" data-access-server>{accessReaches(roleName, app.name)}</p>
      {missing && <p role="alert" className="m-0 text-[13px] leading-snug text-danger" data-access-miss>{PICK_A_TOOL}</p>}
      <AccessEditor
        role={roleName}
        app={app}
        tools={toolsOf(app.name)}
        plan={planOf(draft, app.name)}
        onPlan={onPlan}
        fixed={fixed}
        approvers={approvers}
        ownSet={accessSetName(roleName)}
        readOnly={readOnly}
      />
    </div>
  );
}

type ComposeProps = {
  draft: Draft;
  roles: RoleRow[];
  pills: Pill[];
  unread: boolean;
  onToggle: (name: string) => void;
};

export function ComposeStep({ draft, roles, pills, unread, onToggle }: ComposeProps) {
  const composable = roles.filter(isComposable);
  return (
    <div className="flex max-w-[900px] flex-col gap-3">
      {composable.length === 0 && <p className="m-0 text-sm text-muted-foreground">{NO_COMPOSABLE}</p>}
      {composable.map((r) => (
        <ListRow key={r.id}>
          <input
            type="checkbox"
            aria-label={roleTick(r.name)}
            checked={draft.composed.includes(r.name)}
            onChange={() => onToggle(r.name)}
            className="size-3.5 shrink-0 accent-[var(--link)]"
          />
          <span className="font-mono text-[13px] text-foreground">{r.name}</span>
          <span className="min-w-0 flex-1 truncate text-[13px] text-muted-foreground" title={r.description || NO_DESCRIPTION}>{r.description || NO_DESCRIPTION}</span>
        </ListRow>
      ))}
      <Section title={HOLDERS_REACH} action={<HelpTip label={HOLDERS_REACH} text={COMPOSE_HELP} />}>
        {draft.composed.length === 0 && <p className="m-0 text-sm text-muted-foreground">{REACH_EMPTY}</p>}
        <div className="flex flex-wrap gap-1.5">
          {pills.map((p) => (
            <span key={p.server + "/" + p.via} className="inline-flex items-center gap-1.5 rounded-md border border-border bg-card px-2 py-1 text-sm" data-reach-pill={p.server}>
              <span className="font-mono text-foreground">{p.server}</span>
              <span className="text-[13px] text-muted-foreground">{reachPill(p.tools, p.via)}</span>
            </span>
          ))}
        </div>
        {unread && <p className="m-0 text-[13px] text-muted-foreground">{REACH_UNREAD}</p>}
      </Section>
    </div>
  );
}

export function PacksStep({ draft, packs, onToggle }: { draft: Draft; packs: PackRow[]; onToggle: (id: string) => void }) {
  return (
    <div className="flex max-w-[900px] flex-col gap-2">
      {packs.map((p) => {
        const on = draft.packs.includes(p.id);
        return (
          <ListRow key={p.id}>
            {/* The app carries no Radix switch, so this is a button in the
                switch role, the shape the editor's own switch paints. */}
            <button
              type="button"
              role="switch"
              aria-checked={on}
              aria-label={packSwitch(p.name)}
              onClick={() => onToggle(p.id)}
              className={cn(
                "inline-flex h-5 w-9 shrink-0 items-center rounded-full border p-px outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50",
                on ? "border-link bg-link" : "border-border bg-background",
              )}
            >
              <span aria-hidden="true" className={cn("size-3.5 rounded-full transition-transform", on ? "translate-x-4 bg-background" : "translate-x-0 bg-muted-foreground")} />
            </button>
            <span className="font-mono text-[13px] text-foreground">{p.name}</span>
            {p.version && <span className="text-[13px] text-muted-foreground">{packVersion(p.version)}</span>}
          </ListRow>
        );
      })}
    </div>
  );
}

type ReviewProps = {
  draft: Draft;
  apps: AppRow[];
  toolsOf: (app: string) => ToolRow[];
  // fixed are the rules of other sets that decide a tool of the picked
  // server, drawn in their own words.
  fixed: Record<string, FixedRule>;
  packs: PackRow[];
  // hadPacks says whether the Packs step ran, so the review names the packs
  // only where the person was asked about them.
  hadPacks: boolean;
  acts: Act[];
  commands: string;
};

// ReachTable is what an application role reaches once it exists: the one
// server's heading row, then every tool it grants and what a call to each
// does, the approval named where a person decides.
function ReachTable({ draft, apps, toolsOf, fixed }: Pick<ReviewProps, "draft" | "apps" | "toolsOf" | "fixed">) {
  const picked = apps.filter((a) => a.name === draft.server && grantMatchers(planOf(draft, a.name), toolNames(toolsOf(a.name))).length > 0);
  if (!picked.length) return <p className="m-0 max-w-[75ch] text-sm text-muted-foreground" data-nothing-bound>{NOTHING_BOUND}</p>;
  const cell = (plan: Plan, tool: string) => {
    const f = fixed[tool];
    if (f) return <span className={f.status === "hidden_policy" ? "text-danger" : "text-warn"}>{fixedWords(f.word, f.set)}</span>;
    return <span className={CHOICE_HUE[choiceOf(plan, tool) || "allow"]}>{policyWord(plan, tool)}</span>;
  };
  return (
    <div className="rounded-md border border-border">
      <Table className="table-fixed">
        <colgroup>
          <col className="w-[24%]" />
          <col />
          <col className="w-[36%]" />
        </colgroup>
        <TableHeader>
          <TableRow>
            <TableHead>{EDITOR_HEAD.tool}</TableHead>
            <TableHead>{EDITOR_HEAD.what}</TableHead>
            <TableHead>
              <span className="inline-flex items-center gap-1.5">{EDITOR_HEAD.policy}<HelpTip label={EDITOR_HEAD.policy} text={POLICY_HELP} /></span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {picked.map((a) => {
            const tools = toolsOf(a.name);
            const names = toolNames(tools);
            const plan = planOf(draft, a.name);
            const matchers = grantMatchers(plan, names);
            const glob = isGlob(matchers);
            return (
              <React.Fragment key={a.name}>
                <TableRow className="bg-card" data-server-band={a.name}>
                  <TableCell colSpan={3} className="whitespace-normal">
                    <b className="font-mono text-[13px] font-semibold text-foreground">{a.name}</b>
                    <span className="ml-2 text-[13px] text-muted-foreground">{glob ? reviewEvery(names.length) : reviewCount(matchers.length, names.length)}</span>
                  </TableCell>
                </TableRow>
                {tools.filter((t) => inGrant(plan, t.name)).map((t) => (
                  <TableRow key={a.name + "/" + t.name} data-review-tool={t.name}>
                    <TableCell className="truncate font-mono text-[13px] text-foreground" title={t.name}>{t.name}</TableCell>
                    <TableCell className="truncate text-text-2" title={t.description || NO_TOOL_DESCRIPTION}>{t.description || NO_TOOL_DESCRIPTION}</TableCell>
                    <TableCell className="whitespace-normal text-[13px]">{cell(plan, t.name)}</TableCell>
                  </TableRow>
                ))}
              </React.Fragment>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

export function ReviewStep({ draft, apps, toolsOf, fixed, packs, hadPacks, acts, commands }: ReviewProps) {
  const picked = packs.filter((p) => draft.packs.includes(p.id));
  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-wrap items-baseline gap-3">
        <b className="font-mono text-base font-semibold text-foreground">{storedName(draft)}</b>
        <RoleKindBadge role={{ kind: draft.kind }} />
        {draft.description.trim() && <span className="text-sm text-muted-foreground">{draft.description.trim()}</span>}
      </div>

      {draft.kind === "application" && (
        <Section title={REVIEW_REACH}>
          <ReachTable draft={draft} apps={apps} toolsOf={toolsOf} fixed={fixed} />
        </Section>
      )}
      {draft.kind === "business" && (
        <Section title={COMPOSES_ROW}>
          <p className="m-0 max-w-[75ch] text-sm text-text-2" data-composes>
            {draft.composed.length ? list(draft.composed) : NOTHING_COMPOSED}
          </p>
        </Section>
      )}
      {draft.kind === "approver" && <p className="m-0 max-w-[75ch] text-sm text-text-2" data-approver-next>{APPROVER_NEXT}</p>}
      {hadPacks && (
        <Section title={PACKS_ROW}>
          <p className="m-0 text-sm text-text-2" data-packs-row>{picked.length ? list(picked.map((p) => p.name)) : NO_PACK_PICKED}</p>
          {picked.length > 0 && <p className="m-0 max-w-[80ch] text-[13px] leading-snug text-muted-foreground" data-packs-after>{PACKS_AFTER}</p>}
        </Section>
      )}

      <Section title={REVIEW_HAPPEN}>
        <Acts acts={acts} numbered />
      </Section>

      <div className="flex items-start gap-1.5">
        <div className="min-w-0 flex-1">
          <Fold title={REVIEW_COMMANDS}>
            <Code className="rounded-none border-0">{commands}</Code>
          </Fold>
        </div>
        <HelpTip label={REVIEW_COMMANDS} text={COMMANDS_HELP} className="mt-1.5" />
      </div>
    </div>
  );
}

type DoneProps = {
  acts: Act[];
  // name is the role the publish made.
  name: string;
  failed: Act | null;
  running: boolean;
};

// DoneStep is what landed after the publish: the rows in the state each
// one reached, and above them the one sentence the run ends on. A pack that
// failed to bind after the publish says where to finish it.
export function DoneStep({ acts, name, failed, running }: DoneProps) {
  const landed = acts.filter((a) => a.state === "done").length;
  return (
    <div className="flex flex-col gap-4">
      {failed ? (
        <>
          <div role="status" className="max-w-[80ch] rounded-md border border-warn/40 bg-warn-bg px-4 py-3 text-sm leading-relaxed text-foreground" data-half-landed>
            <b className="font-semibold">{halfLanded(landed, acts.length)}</b> {failed.section ? finishOn(failed.section) : ""}
          </div>
          <ProblemBlock problem={{ subject: failed.subject || "", text: failed.error || "", unreachable: failed.error === UNREACHABLE_STEP }} />
        </>
      ) : !running && name ? (
        <p className="m-0 flex items-center gap-1.5 text-sm text-ok" data-live-role>
          {liveRole(name)}
          <HelpTip label={name} text={AFTER_CREATE_HELP} />
        </p>
      ) : null}
      <Acts acts={acts} />
    </div>
  );
}
