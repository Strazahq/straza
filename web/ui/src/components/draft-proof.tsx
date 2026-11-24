import type * as React from "react";
import { CircleCheckIcon, ClockIcon, HistoryIcon, Loader2Icon, ShieldCheckIcon, Undo2Icon } from "lucide-react";
import { Chip, Pill } from "@/components/chip";
import { Muted, ObjectTag, PrincipalTag } from "@/components/draft-parts";
import { HelpTip } from "@/components/help-tip";
import { Button } from "@/components/ui/button";
import type { Draft, DraftSummary } from "@/lib/api";
import {
  BY,
  REASON_LABEL,
  REPLACED,
  SIGNED,
  STATE_WORD,
  THEN,
  UNDO,
  UNDO_HELP,
  VERSION,
  draftTitle,
  fromClient,
} from "@/lib/drafts-words";
import { REEVALUATE, liveSince, liveSinceNoVersion } from "@/lib/policy-words";
import { navigate } from "@/lib/router";
import { openDraft } from "@/lib/save-words";
import { absTime, dayWord } from "@/lib/words";

// The strip of a decided draft, under its head: the proof of a publish in
// the ok tone with Undo, a published draft a later one replaced in the plain
// tone with the draft that did, and who discarded a draft or when it
// expired.

const ROW = "flex flex-wrap items-center gap-2";

// ProofStrip is the proof of a publish: who published it, when, from which
// client, the policy version it made, and Undo. Its help says sessions
// follow the new version only for a draft that holds a role or a policy.
// Being live is a trust state, so the ok tone fills the whole strip and no
// edge carries it. children are the lines under the row: the servers a
// publish just changed, and a refused Undo.
export function ProofStrip({ draft: d, undoing, onUndo, children }: { draft: Draft; undoing: boolean; onUndo: () => void; children?: React.ReactNode }) {
  const at = absTime(d.decided_at);
  const client = d.decided_by?.client;
  const reevaluates = d.items.some((it) => it.kind !== "App");
  return (
    <div role="status" className="flex flex-col gap-1 rounded-md border border-ok/40 bg-ok-bg px-4 py-3 text-dense leading-6 text-text-2" data-published>
      <div className={ROW}>
        <CircleCheckIcon className="size-4 shrink-0 text-ok" aria-hidden="true" />
        <Muted>{BY}</Muted>
        {d.decided_by && <PrincipalTag p={d.decided_by} />}
        <Pill icon={ClockIcon}>{at}</Pill>
        {client && <Pill>{fromClient(client)}</Pill>}
        {d.snapshot && <Pill data-version>{VERSION}<span className="font-mono">{d.snapshot.slice(0, 8)}</span></Pill>}
        <Pill icon={ShieldCheckIcon}>{SIGNED}</Pill>
        <HelpTip label={STATE_WORD.published} text={(d.snapshot ? liveSince(at, d.snapshot.slice(0, 8)) : liveSinceNoVersion(at)) + (reevaluates ? " " + REEVALUATE : "")} />
        <span className="ml-auto flex items-center gap-1.5">
          <Button variant="outline" size="sm" onClick={onUndo} disabled={undoing}>{undoing ? <Loader2Icon className="animate-spin" /> : <Undo2Icon />}{UNDO}</Button>
          <HelpTip label={UNDO} text={UNDO_HELP} />
        </span>
      </div>
      {children}
    </div>
  );
}

// ReplacedStrip is a published draft a later publish replaced: who
// published it and when, then the draft that replaced it, with the button
// that opens that draft. It offers no Undo, since an undo of a replaced
// draft would take back the later draft's state.
export function ReplacedStrip({ draft: d, successor }: { draft: Draft; successor: DraftSummary }) {
  return (
    <div role="status" className={ROW + " rounded-md border border-border bg-card px-4 py-3"} data-published data-replaced={successor.id}>
      <Chip tone="plain" icon={HistoryIcon}>{REPLACED}</Chip>
      <Muted>{BY}</Muted>
      {d.decided_by && <PrincipalTag p={d.decided_by} />}
      <Pill icon={ClockIcon}>{absTime(d.decided_at)}</Pill>
      <Muted>{THEN}</Muted>
      <ObjectTag kind="draft" name={draftTitle(successor.id)} />
      <Pill>{dayWord(successor.decided_at)}</Pill>
      <Button variant="outline" size="sm" className="ml-auto" onClick={() => navigate("drafts", [successor.id])}>{openDraft(successor.id)}</Button>
    </div>
  );
}

// DecidedRow is a discarded or an expired draft: who discarded it and when,
// or when it expired, and the reason given.
export function DecidedRow({ draft: d }: { draft: Draft }) {
  const discarded = d.state === "discarded";
  return (
    <div className="flex flex-col gap-1.5 rounded-md border border-border bg-card px-4 py-3" data-decided>
      <div className={ROW}>
        {discarded && <Muted>{BY}</Muted>}
        {discarded && d.decided_by && <PrincipalTag p={d.decided_by} />}
        <Pill icon={ClockIcon}>{absTime(discarded ? d.decided_at : d.decided_at || d.expires_at)}</Pill>
      </div>
      {d.decided_reason && (
        <p className="m-0 flex flex-wrap items-baseline gap-2 text-dense text-text-2" data-reason>
          <Muted>{REASON_LABEL}</Muted>
          <span className="min-w-0 break-words">{d.decided_reason}</span>
        </p>
      )}
    </div>
  );
}
