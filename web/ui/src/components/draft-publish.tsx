import * as React from "react";
import { CheckIcon, Loader2Icon } from "lucide-react";
import { type Field, type OnChecked, SecretFields, SecretRunView, askState, fieldsOf, useSecretRun } from "@/components/draft-secrets";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import type { ApiError, Draft, DraftPublished, DraftVerdict } from "@/lib/api";
import { publishDraft, refusalOf } from "@/lib/drafts-api";
import { AGENT_LOOSEN_CODE, type Acks, type ConfirmBlock, acknowledged, changeCounts, confirmBlocks, firstMissing, gainStory, gainsHidden, publishBody, type SecretAsk, secretAsks, typedWrongly } from "@/lib/drafts-model";
import {
  ACCEPT,
  ACK_MISSING,
  AGENT_LOOSENS,
  BLOCKED,
  CANCEL,
  CONFIRM,
  GROUP,
  MATCHED,
  NOBODY_UNTIL,
  NOT_PUBLISHED,
  PUBLISH,
  SIGN_HELP,
  SIGN_LABEL,
  agentWrote,
  answered,
  blockTitle,
  doesOf,
  goLive,
  notSent,
  publishLead,
  publishTitle,
  publishedToast,
  readLines,
  showLines,
  signLine,
  typePrompt,
  typedWrong,
} from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
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
  open: boolean;
  onOpenChange: (open: boolean) => void;
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
  // whose person never saw the draft's id.
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
const BOX = "flex flex-col gap-2 rounded-md border px-3 py-2.5 text-sm leading-relaxed text-text-2";
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

const loosens = (b: ConfirmBlock) => b.lines.some((f) => f.code === AGENT_LOOSEN_CODE);

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
          {pre} <code className="rounded border border-border bg-secondary px-1 font-mono text-[13px] font-semibold text-foreground">{b.expect}</code> {post}
        </label>
        <Input
          id={id}
          value={acks.typed[b.id] || ""}
          placeholder={b.expect}
          onChange={(e) => onType(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={wrong || undefined}
          className="h-8 w-56 max-w-full font-mono text-[13px]"
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
      {wrong && <p className="m-0 text-[13px] text-danger" data-typed-wrong>{typedWrong(b.expect, verb)}</p>}
    </>
  );
}

// Block is one confirm block. A lone tick line is its tick, labelled with
// the line. Every other block has a heading, its lines, folded for a count
// or a tick group, and then its tick, its field, or why this reader cannot
// confirm it.
function Block({ b, acks, agent, onTick, onType }: { b: ConfirmBlock; acks: Acks; agent: string | null; onTick: (on: boolean) => void; onType: (text: string) => void }) {
  const tick = <input type="checkbox" checked={!!acks.ticked[b.id]} onChange={(e) => onTick(e.target.checked)} className={TICK} />;
  if (b.kind === "tick" && b.lines.length === 1) {
    return (
      <label className={cn(BOX, "flex-row gap-2.5 border-border bg-card")} data-block={b.id}>
        {tick}
        <span>{b.lines[0].sentence}</span>
      </label>
    );
  }
  const agents = loosens(b);
  const loud = agents || b.lines.some((f) => f.code === REMOVAL_CODE);
  const does = doesOf(b.lines[0].code, b.lines[0].object);
  const objects = new Set(b.lines.map((f) => f.object || f.key)).size;
  return (
    <div className={cn(BOX, loud ? "border-danger/40 bg-danger-bg" : "border-border bg-card")} data-block={b.id} data-loud={loud || undefined}>
      <p className={cn("m-0 font-semibold", agents ? "text-danger" : "text-foreground")}>{agents ? AGENT_LOOSENS : blockTitle(does, objects)}</p>
      {agents && <p className="m-0">{agentWrote(agent)}</p>}
      {b.kind === "name" ? (
        b.lines.map((f) => <p key={f.key} className="m-0">{f.sentence}</p>)
      ) : (
        <details>
          <summary className="cursor-pointer text-muted-foreground hover:text-foreground">{showLines(does, objects)}</summary>
          <ul className="mt-1.5 mb-0 flex list-disc flex-col gap-1 pl-5">{b.lines.map((f) => <li key={f.key}>{f.sentence}</li>)}</ul>
        </details>
      )}
      {b.blocked ? (
        <p className="m-0 text-muted-foreground" data-blocked>{BLOCKED}</p>
      ) : b.kind === "tick" ? (
        <label className="flex items-start gap-2.5">{tick}<span>{readLines(b.lines.length)}</span></label>
      ) : (
        <Typed b={b} acks={acks} verb={agents ? ACCEPT : CONFIRM} onType={onType} />
      )}
    </div>
  );
}

const NO_CHECKS: OnChecked = () => undefined;

function Body({ draft, verdict, onOpenChange, onPublished, onVerdict, onRefused, title, warnings, quiet, onChecked }: Omit<DraftPublishProps, "open">) {
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
  const story = gainStory(verdict.gains);
  // Nobody gains is a claim about every row, so it is left out for a reader
  // the server hid rows from.
  const nobody = story.nobodyHolds.length && !story.gain.length && !gainsHidden(verdict) ? " " + NOBODY_UNTIL : "";
  const lead = (blocks.length ? publishLead(blocks.flatMap((b) => b.lines)) : goLive(changeCounts(draft.items))) + nobody;
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
    if (stays) run.begin(sends.map((x) => ({ ask: x.ask, secret: x.secret !== null, listing: x.listing !== null })), ask.label);
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
  if (run.runs) return <SecretRunView id={draft.id} run={run} onClose={() => onOpenChange(false)} />;
  return (
    <>
      <DialogHeader>
        <DialogTitle>{title || publishTitle(draft.id)}</DialogTitle>
        <DialogDescription className="text-sm leading-relaxed text-text-2">{lead}</DialogDescription>
      </DialogHeader>

      {warnings && (verdict.warnings || []).length > 0 && (
        <div className="flex flex-col gap-1.5" data-warnings>
          <p className="m-0 text-[13px] font-semibold text-muted-foreground">{GROUP.warning}</p>
          {verdict.warnings.map((f) => <p key={f.key} className="m-0 border-l-[3px] border-warn bg-card px-3 py-2 text-sm leading-relaxed text-text-2">{f.sentence}</p>)}
        </div>
      )}

      {blocks.length > 0 && (
        <div className="flex flex-col gap-2" data-blocks>
          {blocks.map((b) => (
            <Block
              key={b.id}
              b={b}
              acks={acks}
              agent={agent}
              onTick={(on) => setAcks((a) => ({ ...a, ticked: { ...a.ticked, [b.id]: on } }))}
              onType={(text) => setAcks((a) => ({ ...a, typed: { ...a.typed, [b.id]: text } }))}
            />
          ))}
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

      <p className="flex items-start gap-2 border-t border-border pt-3 text-sm leading-relaxed text-text-2" data-sign>
        <span>{signLine(sponsoredAgent(draft), blocks.some((b) => loosens(b) && !b.blocked), blocks.length > 0)}</span>
        <HelpTip label={SIGN_LABEL} text={SIGN_HELP} className="mt-0.5" />
      </p>

      {refusal && <RefusedError subject={PUBLISH} heading={NOT_PUBLISHED} message={refusal}>{dropped ? notSent(dropped) : null}</RefusedError>}
      {lines.length > 0 && (
        <div id={hint} className="flex flex-col gap-0.5 text-sm text-muted-foreground" data-ack-missing>
          {lines.map((l) => <span key={l}>{l}</span>)}
        </div>
      )}

      <DialogFooter>
        <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy} className={FOCUS_RING} data-cancel>{CANCEL}</Button>
        <Button onClick={() => void publish()} disabled={busy || !!missing || !!ask.need} aria-describedby={lines.length ? hint : undefined} data-publish>{busy && <Loader2Icon className="animate-spin" />}{ask.label}</Button>
      </DialogFooter>
    </>
  );
}

export function DraftPublish({ open, ...rest }: DraftPublishProps) {
  return (
    <Dialog open={open} onOpenChange={rest.onOpenChange}>
      <DialogContent
        className="max-h-[90vh] overflow-y-auto sm:max-w-[620px]"
        data-draft-publish={rest.draft.id}
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          (e.currentTarget as HTMLElement).querySelector<HTMLButtonElement>("[data-cancel]")?.focus();
        }}
      >
        <Body {...rest} />
      </DialogContent>
    </Dialog>
  );
}
