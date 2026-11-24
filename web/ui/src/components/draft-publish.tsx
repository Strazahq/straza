import * as React from "react";
import { CheckIcon, Loader2Icon, UserRoundIcon, WrenchIcon } from "lucide-react";
import { ChangeChip, Chip, OutcomeChip, Pill, ToolChip } from "@/components/chip";
import { ChipDiff } from "@/components/draft-chips";
import { Arrow, HoldersPill, Muted, ObjectTag } from "@/components/draft-parts";
import { type Field, type OnChecked, SecretFields, SecretRunView, askState, fieldsOf, useSecretRun } from "@/components/draft-secrets";
import { GatePills, RuleOutcome } from "@/components/draft-set-card";
import { RefusedError } from "@/components/error-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import type { ApiError, Draft, DraftDetail, DraftItem, DraftPublished, DraftVerdict } from "@/lib/api";
import { coverageNouns, objectsMove } from "@/lib/drafts-access";
import { publishDraft, refusalOf } from "@/lib/drafts-api";
import {
  type Acks, type ConfirmBlock, acknowledged, agentAccepts, changeCounts, confirmBlocks, firstMissing, gainStory, gainsHidden, iconKind, objectOf, publishBody, roleOf, type SecretAsk, secretAsks, typedWrongly, whatOf,
} from "@/lib/drafts-model";
import { GAIN_OUTCOME, type Marked, namesMove, outcomeOf, ruleAspects } from "@/lib/drafts-rules";
import { type Side, type Widens, widensOf } from "@/lib/drafts-widens";
import {
  ACCEPT,
  ACK_MISSING,
  BLOCKED,
  CANCEL,
  CONFIRM,
  GROUP,
  MATCHED,
  NOT_PUBLISHED,
  NOT_REGISTERED,
  ON,
  OUTCOME,
  PUBLISH,
  SAME_AS_NOW,
  agentTitle,
  agentWrote,
  answered,
  appliesTo,
  blockTitle,
  doesOf,
  everyType,
  goLive,
  notSent,
  objectsLabel,
  publishOne,
  publishTitle,
  publishedToast,
  sponsorLine,
  toolCount,
  typePrompt,
  typedWrong,
  undoLine,
} from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { EVERY_WORD } from "@/lib/policy-words";
import { snapshot } from "@/lib/session";
import { cn } from "@/lib/utils";

// The publish dialog of a draft, shared by the review page and the editors'
// Save and publish. It groups the risks into confirm blocks, one tick or
// one typed text each, and keeps Publish off until every block is
// confirmed. It sends back each line's key and typed text and the digest of
// the verdict on screen, and shows every refusal as the server answered
// it: publish is the server's decision, and this dialog only collects the
// confirmations.

export type DraftPublishProps = {
  draft: Draft;
  // verdict is the reading the person reviewed; its keys and digest travel
  // back in the publish.
  verdict: DraftVerdict;
  // live is the draft's live state, which tells a policy the draft turns
  // back on. Only the review page has it.
  live?: DraftDetail["live"];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onClosed runs once the dialog is gone, so a caller that opens it with
  // no trigger can put focus back on the button that opened it.
  onClosed?: () => void;
  // onPublished takes the answer of a publish that landed. stays marks one
  // after which the dialog stays open to store the values it was given,
  // so the caller shows the answer and leaves the dialog open.
  onPublished: (answer: DraftPublished, stays?: boolean) => void;
  // onVerdict takes the verdict a 409 carries, so the page shows a line
  // that appeared since the check before the next try.
  onVerdict?: (verdict: DraftVerdict) => void;
  // onRefused takes every refused publish after the dialog shows it, so an
  // editor can close the dialog on a draft that waits for someone else.
  onRefused?: (err: ApiError) => void;
  // title replaces "Publish draft {id}?" for an editor's Save and publish,
  // whose person never saw the draft's id. A draft that adds one object
  // and changes nothing else is titled by that object's name instead.
  title?: string;
  // warnings lists the verdict's warnings under the lead, for an editor's
  // Save and publish, which has no review page above the dialog.
  warnings?: boolean;
  // quiet leaves the toast after a publish to the caller, whose toast
  // carries Undo.
  quiet?: boolean;
  // onChecked turns on the Secrets section, which asks for each value a
  // new server needs and nothing stores yet, and stores and checks them
  // once the publish lands. It takes each server after its check. Only the
  // review page passes it: the Add MCP server wizard asks for its secret
  // in a step of its own.
  onChecked?: OnChecked;
};

const NO_ACKS: Acks = { ticked: {}, typed: {} };
// REMOVAL_CODE is the risk of a server removal, a block shown loud like an
// agent's self-loosening, because publishing the old state cannot bring
// back the server's secrets and connections.
const REMOVAL_CODE = "server.removal";
const BOX = "flex flex-col gap-2 rounded-md border px-3 py-2.5 text-dense leading-relaxed text-text-2";
const TICK = "mt-1 size-4 shrink-0 accent-primary";
// FOCUS_RING rings Cancel on any focus. The dialog opens from a mouse press
// and focuses Cancel from code, which a browser does not treat as
// focus-visible, so the button's own ring would stay off.
const FOCUS_RING = "focus:border-ring focus:ring-[3px] focus:ring-ring/50";

// proposerAgent is the first agent among the draft's authors, or null when
// only people or a file wrote it. The server reads a draft any agent
// revised as an agent's draft.
function proposerAgent(draft: Draft): string | null {
  return draft.authors.find((p) => p.agent)?.username || null;
}

// sponsoredAgent is an agent author of the draft that the person
// publishing it sponsors, the case where a borrowed login matters and
// second-person publishing bars the sponsor, or null.
function sponsoredAgent(draft: Draft): string | null {
  const me = snapshot()?.user || "";
  return (me && draft.authors.find((p) => p.agent && p.sponsor === me)?.username) || null;
}

const loosens = (b: ConfirmBlock) => b.lines.some(agentAccepts);

// soleAdd is the object a draft that only adds is titled by: its one item,
// or the one role or server that comes with policies of its own, or null.
function soleAdd(draft: Draft): DraftItem | null {
  if (!draft.items.length || draft.items.some((it) => whatOf(it) !== "new")) return null;
  const main = draft.items.length === 1 ? draft.items : draft.items.filter((it) => it.kind !== "PolicySet");
  return main.length === 1 ? main[0] : null;
}

// titleOf is a block's heading: the agent's change of its own access, else
// what publishing does to the objects its lines name. A block of one line
// said in the server's own sentence has none, because the sentence says it.
function titleOf(b: ConfirmBlock, w: Widens): string | null {
  if (loosens(b)) return agentTitle(b.lines);
  if (b.lines.length === 1 && w.lines.length === 1) return null;
  return blockTitle(doesOf(b.lines[0].code, b.lines[0].object), new Set(b.lines.map((f) => f.object || f.key)).size);
}

// Typed is the field of a typed block: what to type, set as code and as
// the placeholder, a check mark once it matches, and a plain line when the
// text can no longer match.
function Typed({ b, acks, verb, onType }: { b: ConfirmBlock; acks: Acks; verb: string; onType: (text: string) => void }) {
  const id = React.useId();
  const [pre, post] = typePrompt(verb);
  const wrong = typedWrongly(b, acks);
  return (
    <>
      <div className="flex flex-wrap items-center gap-2.5">
        <label htmlFor={id}>
          {pre} <code className="rounded-md border border-border bg-secondary px-1 font-mono text-caption font-semibold text-foreground">{b.expect}</code> {post}
        </label>
        <Input
          id={id}
          value={acks.typed[b.id] || ""}
          placeholder={b.expect}
          onChange={(e) => onType(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={wrong || undefined}
          className="h-8 w-56 max-w-full font-mono text-caption"
        />
        <span aria-live="polite" className="text-ok">
          {acknowledged(b, acks) && (
            <span data-matched>
              <CheckIcon className="size-4" aria-hidden="true" />
              <span className="sr-only">{MATCHED}</span>
            </span>
          )}
        </span>
      </div>
      {wrong && <p className="m-0 text-caption text-danger" data-typed-wrong>{typedWrong(b.expect, verb)}</p>}
    </>
  );
}

// Objects names the objects of a block's lines as tags: a policy the draft
// turns off or removes with its change, and a changed policy that applies
// to the proposer with that pill. me is the reader's username. It is a span,
// since a tick's label holds it.
function Objects({ b, draft, live, me }: { b: ConfirmBlock; draft: Draft; live?: DraftDetail["live"]; me: string }) {
  const out: React.ReactNode[] = [];
  const seen = new Set<string>();
  const proposer = draft.authors[0]?.username || "";
  for (const f of b.lines) {
    if (f.object && !seen.has(f.object)) {
      seen.add(f.object);
      const kind = f.object.split("/")[0];
      const it = draft.items.find((x) => objectOf(x) === f.object);
      const what = it ? whatOf({ op: it.op, existed: it.existed, was: live?.[f.object]?.op }) : null;
      out.push(<ObjectTag key={f.object} kind={kind === "Role" && it ? iconKind(kind, roleOf(it.doc)) : iconKind(kind)} name={f.object.slice(kind.length + 1)} />);
      if (what === "off" || what === "removed") out.push(<ChangeChip key={f.object + " change"} change={what} />);
      if (b.lines.some((l) => l.object === f.object && l.binds_proposer)) out.push(<Pill key={f.object + " applies"} icon={UserRoundIcon} data-applies>{appliesTo(proposer === me ? null : proposer)}</Pill>);
    }
  }
  return out.length ? <span className={FACT} data-block-objects>{out}</span> : null;
}

const FACT = "flex flex-wrap items-center gap-1.5";

// SideFacts is one side of a tools row as the Checks table draws it: the
// outcome, then the facts of its gate, or its words as the hover where the
// server sent no gate.
function SideFacts({ side }: { side: Side }) {
  const o = GAIN_OUTCOME[side.outcome];
  return (
    <>
      {o ? <OutcomeChip outcome={o} title={side.words} /> : <Chip tone="unknown" title={side.words}>{OUTCOME[side.outcome] || side.outcome}</Chip>}
      {side.gate && <GatePills gate={side.gate} />}
    </>
  );
}

// RuleFact is a loosened policy rule: its name, the names it adds and
// drops, and its outcome now and after publishing.
function RuleFact({ m }: { m: Marked }) {
  const was = m.was || m.rule;
  const aspects = m.was ? ruleAspects(m.was, m.rule).map((a) => a.aspect) : [];
  const moved = m.was && aspects.includes("names") ? namesMove(m.was, m.rule, (r) => EVERY_WORD[r.lane]) : null;
  let after: React.ReactNode = <ChangeChip change="removed" />;
  if (m.mark !== "removed" && m.was) after = aspects.includes("outcome") ? <RuleOutcome rule={m.rule} /> : <><OutcomeChip outcome={outcomeOf(m.rule)} /><Muted>{SAME_AS_NOW}</Muted></>;
  return (
    <span className={FACT} data-block-fact={m.rule.id}>
      <span className="font-mono text-caption font-semibold break-all text-foreground">{m.rule.id}</span>
      {moved && <ChipDiff added={moved.join} removed={moved.leave} same={[]} />}
      <RuleOutcome rule={was} /><Arrow />{after}
    </span>
  );
}

// Facts says what a block's lines do with the facts the Checks table draws
// for them: one tools row per tool and change, the objects a role comes to
// cover, the loosened policy rules, and the server's sentence for a line
// no row says.
function Facts({ w }: { w: Widens }) {
  return (
    <>
      {w.tools.map((r) => (
        <span key={JSON.stringify([r.server, r.tools, r.now, r.after])} className={FACT} data-block-fact={r.server + "/" + r.tools.join(",")}>
          {r.tools.length === 1 ? <ToolChip>{r.tools[0]}</ToolChip> : <Pill icon={WrenchIcon}>{toolCount(r.tools.length)}</Pill>}
          <Muted>{ON}</Muted>
          <ObjectTag kind={r.local ? "local-server" : "server"} name={r.server} />
          {r.local && <Pill>{NOT_REGISTERED}</Pill>}
          <SideFacts side={r.now} /><Arrow /><SideFacts side={r.after} />
        </span>
      ))}
      {w.covers.map((c) => {
        const m = objectsMove(c);
        return (
          <span key={c.role + "/" + c.server} className={FACT} data-block-fact={c.role + "/" + c.server}>
            <Pill>{objectsLabel(coverageNouns(c))}</Pill><Muted>{ON}</Muted><ObjectTag kind="server" name={c.server} />
            {c.every_after ? <Chip tone="accent">{everyType(coverageNouns(c))}</Chip> : <ChipDiff added={m.added.map((o) => o.value)} removed={m.dropped.map((o) => o.value)} same={[]} />}
          </span>
        );
      })}
      {w.rules.map((m) => <RuleFact key={m.rule.id} m={m} />)}
      {w.lines.map((f) => <span key={f.key} className="block" data-block-fact={f.code}>{f.sentence}</span>)}
    </>
  );
}

// Block is one confirm block: its heading where it has one, the objects
// its lines name and what they do, and then its tick, its field, or why
// this reader cannot confirm it. A tick's label is all of that, so the box
// names what it confirms. An agent's change of its own access is loud.
function Block({ b, title, w, acks, agent, draft, live, me, onTick, onType }: { b: ConfirmBlock; title: string | null; w: Widens; acks: Acks; agent: string | null; draft: Draft; live?: DraftDetail["live"]; me: string; onTick: (on: boolean) => void; onType: (text: string) => void }) {
  const agents = loosens(b);
  const loud = agents || b.lines.some((f) => f.code === REMOVAL_CODE);
  const said = (
    <span className="flex min-w-0 flex-col gap-2">
      {title && <span className={cn("font-semibold", agents ? "text-danger" : "text-foreground")} data-block-title>{title}</span>}
      {agents && <span>{agentWrote(agent, b.lines)}</span>}
      <Objects b={b} draft={draft} live={live} me={me} />
      <Facts w={w} />
    </span>
  );
  return (
    <div className={cn(BOX, loud ? "border-danger/40 bg-danger-bg" : "border-border bg-card")} data-block={b.id} data-loud={loud || undefined}>
      {b.kind === "tick" ? (
        <label className="flex items-start gap-2.5"><input type="checkbox" checked={!!acks.ticked[b.id]} onChange={(e) => onTick(e.target.checked)} className={TICK} />{said}</label>
      ) : (
        <>
          {said}
          {b.blocked ? <p className="m-0 text-muted-foreground" data-blocked>{BLOCKED}</p> : <Typed b={b} acks={acks} verb={agents ? ACCEPT : CONFIRM} onType={onType} />}
        </>
      )}
    </div>
  );
}

const NO_CHECKS: OnChecked = () => undefined;

function Body({ draft, verdict, live, onOpenChange, onPublished, onVerdict, onRefused, title, warnings, quiet, onChecked }: Omit<DraftPublishProps, "open">) {
  const [acks, setAcks] = React.useState<Acks>(NO_ACKS);
  // A new verdict, such as the one a 409 carries, can add a line to a
  // block the person already confirmed, so every block starts again.
  const [digest, setDigest] = React.useState(verdict.risk_digest);
  if (digest !== verdict.risk_digest) {
    setDigest(verdict.risk_digest);
    setAcks(NO_ACKS);
  }
  const [busy, setBusy] = React.useState(false);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  // values holds each value typed until the click, later the fields ticked
  // for later, and dropped counts the values a refused publish dropped.
  const [values, setValues] = React.useState<Record<string, string>>({});
  const [later, setLater] = React.useState<Record<string, boolean>>({});
  const [dropped, setDropped] = React.useState(0);
  const run = useSecretRun(onChecked || NO_CHECKS);
  const hint = React.useId();
  const blocks = confirmBlocks(verdict.risks || []);
  const missing = firstMissing(blocks, acks);
  const blocked = blocks.some((b) => b.blocked);
  // widensOf reads only the draft, the verdict and live state of a detail.
  const detail: DraftDetail = { draft, verdict, live: live || {}, revisions: [], may_publish: true };
  const facts = blocks.map((b) => widensOf(detail, b.lines));
  // Two blocks never show the same heading: their facts tell them apart.
  const titles = blocks.map((b, i) => titleOf(b, facts[i]));
  const shown = titles.map((t) => (t && titles.indexOf(t) === titles.lastIndexOf(t) ? t : null));
  const story = gainStory(verdict.gains);
  // That nobody holds the roles that gain is a claim about every row, so it
  // is left out for a reader the server hid rows from.
  const nobody = story.nobodyHolds.length && !story.gain.length && !gainsHidden(verdict) ? story.nobodyHolds : [];
  // A title that names the one object the draft adds needs no lead. The
  // lead counts the objects by what publishing does to them, which no
  // block's heading says.
  const sole = soleAdd(draft);
  const lead = sole && draft.items.length === 1 ? "" : goLive(changeCounts(draft.items, live));
  const sponsors = sponsoredAgent(draft);
  const asks = onChecked ? secretAsks(draft, verdict) : [];
  const fields = fieldsOf(asks);
  const ask = askState(fields, values, later);
  const lines = [missing && !blocked ? ACK_MISSING : "", ask.need].filter(Boolean);

  // publish sends the confirmations. With values to store, the dialog shows
  // the run's rows at once and takes each value out of its state, so a
  // value lives in the run alone and goes out only once the publish landed.
  const publish = async () => {
    if (busy || missing || ask.need) return;
    const take = (a: SecretAsk, kind: Field["kind"]) => {
      const f = fields.find((x) => x.ask === a && x.kind === kind);
      return f && !later[f.key] ? values[f.key] : null;
    };
    const sends = asks.map((a) => ({ ask: a, secret: take(a, "secret"), listing: take(a, "listing") }));
    const count = sends.reduce((n, x) => n + (x.secret !== null ? 1 : 0) + (x.listing !== null ? 1 : 0), 0);
    const stays = count > 0;
    setValues({});
    setBusy(true);
    setRefusal(null);
    setDropped(0);
    if (stays) run.begin(sends.map((x) => ({ ask: x.ask, secret: x.secret !== null, listing: x.listing !== null })), PUBLISH);
    try {
      const answer = await publishDraft(draft.id, publishBody(draft.revision, verdict, acks));
      // A dialog that stays says the draft is live in its own title once the
      // run ends, so only a dialog that closes leaves it to a toast.
      if (!quiet && !stays) notify.ok(publishedToast(draft.id));
      if (stays) {
        onPublished(answer, true);
        void run.start(sends);
      } else {
        onPublished(answer);
      }
    } catch (e) {
      const err = e as ApiError;
      run.reset();
      if (err.status === 401) return;
      // A publish nobody answered may have landed, so the line that says
      // nothing was published stays for a refusal the server answered.
      if (!err.unreachable) setDropped(count);
      const body = refusalOf(err);
      if (body && body.verdict && onVerdict) onVerdict(body.verdict);
      setRefusal(answered(err));
      onRefused?.(err);
    } finally {
      setBusy(false);
    }
  };

  const agent = proposerAgent(draft);
  const me = snapshot()?.user || "";
  if (run.runs) return <SecretRunView id={draft.id} run={run} onClose={() => onOpenChange(false)} />;
  return (
    <>
      <DialogHeader>
        <DialogTitle>{sole ? publishOne(sole.name) : title || publishTitle(draft.id)}</DialogTitle>
        {lead && <DialogDescription className="text-dense leading-relaxed text-text-2">{lead}</DialogDescription>}
        {nobody.length > 0 && (
          <div className="flex flex-wrap items-center justify-center gap-1.5 sm:justify-start" data-nobody>
            {nobody.map((r) => (
              <React.Fragment key={r}>
                <ObjectTag kind={iconKind("Role", roleOf(draft.items.find((x) => x.kind === "Role" && x.name === r)?.doc))} name={r} />
                <HoldersPill n={0} />
              </React.Fragment>
            ))}
          </div>
        )}
      </DialogHeader>

      {warnings && (verdict.warnings || []).length > 0 && (
        <div className="flex flex-col gap-1.5" data-warnings>
          <p className="m-0 text-caption font-semibold text-muted-foreground">{GROUP.warning}</p>
          {verdict.warnings.map((f) => <p key={f.key} className="m-0 border-l-[3px] border-warn bg-card px-3 py-2 text-dense leading-relaxed text-text-2">{f.sentence}</p>)}
        </div>
      )}

      {blocks.length > 0 && (
        <div className="flex flex-col gap-2" data-blocks>
          {blocks.map((b, i) => (
            <Block
              key={b.id}
              b={b}
              title={shown[i]}
              w={facts[i]}
              acks={acks}
              agent={agent}
              draft={draft}
              live={live}
              me={me}
              onTick={(on) => setAcks((a) => ({ ...a, ticked: { ...a.ticked, [b.id]: on } }))}
              onType={(text) => setAcks((a) => ({ ...a, typed: { ...a.typed, [b.id]: text } }))}
            />
          ))}
          {/* A draft that only adds objects has no old version to publish again. */}
          {draft.items.some((it) => it.existed) && <p className="m-0 text-dense text-muted-foreground" data-undo>{undoLine(blocks.some((b) => b.kind !== "tick"))}</p>}
        </div>
      )}

      {fields.length > 0 && (
        <SecretFields
          asks={asks}
          agent={agent}
          values={values}
          later={later}
          onValue={(key, value) => setValues((v) => ({ ...v, [key]: value }))}
          onLater={(key, on) => {
            setLater((l) => ({ ...l, [key]: on }));
            if (on) setValues((v) => ({ ...v, [key]: "" }));
          }}
        />
      )}

      {sponsors && (
        <p className="border-t border-border pt-3 text-dense leading-relaxed text-text-2" data-sign>
          {sponsorLine(sponsors, blocks.length > 0)}
        </p>
      )}

      {refusal && <RefusedError subject={PUBLISH} heading={NOT_PUBLISHED} message={refusal}>{dropped ? notSent(dropped) : null}</RefusedError>}
      {lines.length > 0 && (
        <div id={hint} className="flex flex-col gap-0.5 text-dense text-muted-foreground" data-ack-missing>
          {lines.map((l) => <span key={l}>{l}</span>)}
        </div>
      )}

      <DialogFooter>
        <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy} className={FOCUS_RING} data-cancel>{CANCEL}</Button>
        <Button onClick={() => void publish()} disabled={busy || !!missing || !!ask.need} aria-describedby={lines.length ? hint : undefined} data-publish>{busy && <Loader2Icon className="animate-spin" />}{PUBLISH}</Button>
      </DialogFooter>
    </>
  );
}

export function DraftPublish({ open, onClosed, ...rest }: DraftPublishProps) {
  return (
    <Dialog open={open} onOpenChange={rest.onOpenChange}>
      <DialogContent
        className="max-h-[90vh] overflow-y-auto sm:max-w-[620px]"
        data-draft-publish={rest.draft.id}
        // A dialog titled by the one object its draft adds has no lead to
        // describe it.
        {...(soleAdd(rest.draft) && rest.draft.items.length === 1 ? { "aria-describedby": undefined } : {})}
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          (e.currentTarget as HTMLElement).querySelector<HTMLButtonElement>("[data-cancel]")?.focus();
        }}
        onCloseAutoFocus={onClosed ? (e) => { e.preventDefault(); onClosed(); } : undefined}
      >
        <Body {...rest} />
      </DialogContent>
    </Dialog>
  );
}
