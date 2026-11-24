import * as React from "react";
import { CheckIcon, ChevronDownIcon, CircleHelpIcon, CircleXIcon, ClockIcon, InfoIcon, Loader2Icon, type LucideIcon, TriangleAlertIcon } from "lucide-react";
import { Chip, Pill, Tag, type Tone } from "@/components/chip";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { Button } from "@/components/ui/button";
import { Ack, WidensBody } from "@/components/draft-widens";
import type { ApiError, DraftDetail, DraftFinding, FindingClass } from "@/lib/api";
import { type ConfirmBlock, UNLISTED_CODE, agentAccepts, askedLine, confirmBlocks, findingGroups, objectOf, remoteApp, secretAsks } from "@/lib/drafts-model";
import { foldedWarnings, printed, widensOf } from "@/lib/drafts-widens";
import {
  CHECKS_TITLE,
  CONTACT,
  CONTACT_FAILED,
  CONTACT_HELP,
  GROUP,
  HIDE,
  NOTHING_TO_ACK,
  NOT_CHECKED_AGAIN,
  NO_STORED_CHECK,
  RISK_TAIL,
  SECRET_CHECKS_LINE,
  SHOW,
  STORED_WHEN,
  answered,
  notesCount,
  nowAfter,
  passedCount,
  refusedCount,
  revisionPill,
  toConfirm,
  uncheckedCount,
  warningCount,
  widenCount,
} from "@/lib/drafts-words";
import { cn } from "@/lib/utils";
import { absTime, agoWord } from "@/lib/words";

// The checks of a draft as its page draws them: a heading row of counts,
// what widens access as rows where the verdict carries them, and the lines
// of every class in the server's own words, an empty class left out. The
// lines that stand between the reader and Publish are always open, and the
// passed lines and the notes open on demand. A line renders by the class
// of the list it came in, so a code this console does not know still
// shows.

// NameChips lists tool and object names as tags that wrap, the role chip's
// look. A name marked added reads in the ok tone with its mark.
export function NameChips({ names, added, mark }: { names: string[]; added?: Set<string>; mark?: (name: string) => string }) {
  return (
    <span className="flex flex-wrap gap-1.5" data-names>
      {names.map((n) => {
        const plus = !!added?.has(n);
        return (
          <Tag key={n} data-name={n} data-added={plus || undefined} className={plus ? "border-ok text-ok" : undefined}>
            {plus && mark ? mark(n) : n}
          </Tag>
        );
      })}
    </span>
  );
}

// CLASS_LOOK is the icon, the hue of a line's icon and the tone of a count
// chip of each class of line.
export const CLASS_LOOK: Record<FindingClass, { icon: LucideIcon; hue: string; tone: Tone }> = {
  refused: { icon: CircleXIcon, hue: "text-danger", tone: "danger" },
  risk: { icon: TriangleAlertIcon, hue: "text-warn", tone: "warn" },
  warning: { icon: TriangleAlertIcon, hue: "text-warn", tone: "warn" },
  unchecked: { icon: CircleHelpIcon, hue: "text-unknown", tone: "unknown" },
  passed: { icon: CheckIcon, hue: "text-ok", tone: "ok" },
  info: { icon: InfoIcon, hue: "text-muted-foreground", tone: "plain" },
};

// FOLDED are the classes that open on demand.
const FOLDED: FindingClass[] = ["passed", "info"];

const Count = ({ cls, children }: { cls: FindingClass; children: string }) => <Chip tone={CLASS_LOOK[cls].tone} icon={CLASS_LOOK[cls].icon} data-checks-count={cls}>{children}</Chip>;

type HeadProps = {
  detail: DraftDetail;
  // omit are the keys of notes the page already says elsewhere.
  omit: string[];
  more: boolean;
  onMore: () => void;
};

// ChecksHead is the row beside the Checks heading. An open draft counts
// what the reader confirms, the band's count, then the lines of each other
// class its check holds, a warning folded into a row left out, says how
// long ago it was checked, and opens its passed lines and notes on Show. A
// decided draft counts the lines of the check the server stored for its
// revision, which is never run again.
export function ChecksHead({ detail, omit, more, onMore }: HeadProps) {
  const d = detail.draft;
  const v = detail.verdict;
  if (d.state !== "open") {
    const state = d.state;
    const stored = detail.checks;
    const help = <HelpTip label={CHECKS_TITLE} text={NOT_CHECKED_AGAIN[state]} />;
    if (!stored) return <><Pill data-checks-stored="none">{NO_STORED_CHECK}</Pill>{help}</>;
    const counts = [
      stored.refused ? <Count key="refused" cls="refused">{refusedCount(stored.refused)}</Count> : null,
      stored.risks ? <Count key="risk" cls="risk">{widenCount(stored.risks)}</Count> : null,
      stored.warnings ? <Count key="warning" cls="warning">{warningCount(stored.warnings)}</Count> : null,
      stored.unchecked ? <Count key="unchecked" cls="unchecked">{uncheckedCount(stored.unchecked)}</Count> : null,
    ].filter(Boolean);
    return (
      <>
        {counts.length ? counts : <Pill>{NOTHING_TO_ACK}</Pill>}
        <Pill>{revisionPill(stored.revision)}</Pill>
        <Pill icon={ClockIcon}>{absTime(stored.checked_at)}</Pill>
        <Pill data-checks-stored={state}>{STORED_WHEN[state]}</Pill>
        {help}
      </>
    );
  }
  const left = new Set([...omit, ...foldedWarnings(v).keys()]);
  const n = (list: DraftFinding[] | null | undefined) => (list || []).filter((f) => !left.has(f.key)).length;
  const notes = n(v.info);
  const passed = n(v.passed);
  const blocks = confirmBlocks((v.risks || []).filter((f) => !left.has(f.key))).length;
  return (
    <>
      {n(v.refused) > 0 && <Count cls="refused">{refusedCount(n(v.refused))}</Count>}
      {blocks > 0 && <Count cls="risk">{toConfirm(blocks)}</Count>}
      {n(v.warnings) > 0 && <Count cls="warning">{warningCount(n(v.warnings))}</Count>}
      {n(v.unchecked) > 0 && <Count cls="unchecked">{uncheckedCount(n(v.unchecked))}</Count>}
      {notes > 0 && <Count cls="info">{notesCount(notes)}</Count>}
      {passed > 0 && <Count cls="passed">{passedCount(passed)}</Count>}
      <Pill icon={ClockIcon} title={absTime(v.checked_at)}>{agoWord(v.checked_at)}</Pill>
      {(passed > 0 || notes > 0) && (
        <Button variant="ghost" size="xs" aria-expanded={more} onClick={onMore} data-checks-more>
          {more ? HIDE : SHOW}<ChevronDownIcon className={cn("transition-transform", more && "rotate-180")} aria-hidden="true" />
        </Button>
      )}
    </>
  );
}

type Props = {
  detail: DraftDetail;
  // onContact asks the page to contact one proposed server and read the
  // draft again; it throws the server's refusal. Absent, no line offers it.
  onContact?: (object: string) => Promise<void>;
  // omit are the keys of lines the page already says elsewhere.
  omit?: string[];
  // more opens the passed lines and the notes.
  more?: boolean;
};

export function DraftChecks({ detail, onContact, omit, more }: Props) {
  const [contacting, setContacting] = React.useState<string | null>(null);
  const [failed, setFailed] = React.useState<Record<string, string>>({});
  const open = detail.draft.state === "open";

  // contactable says whether a line is the unread tools of a remote server
  // the draft puts, the one case Contact reaches.
  const contactable = (f: DraftFinding) => {
    if (!open || !onContact || f.code !== UNLISTED_CODE || !f.object) return false;
    const it = detail.draft.items.find((i) => objectOf(i) === f.object);
    return !!it && it.kind === "App" && it.op === "put" && remoteApp(it.doc);
  };

  const contact = async (object: string) => {
    if (!onContact || contacting) return;
    setContacting(object);
    setFailed((m) => ({ ...m, [object]: "" }));
    try {
      await onContact(object);
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setFailed((m) => ({ ...m, [object]: answered(err) }));
    } finally {
      setContacting(null);
    }
  };

  // An agent's change a person accepts says how on its own line, the first
  // of its block, and every other block says it once under the section.
  const blocks = confirmBlocks(detail.verdict.risks || []);
  const agentAck = new Map<string, ConfirmBlock>();
  for (const b of blocks) {
    const first = b.lines.find(agentAccepts);
    if (first) agentAck.set(first.key, b);
  }
  const left = new Set([...(omit || []), ...foldedWarnings(detail.verdict).keys()]);
  // fixOf is a line's fix. On an open draft the publish dialog asks for a
  // server's secret and listing credential, so their warnings say that in
  // place of the fix that names strazactl, or of none.
  const asks = open ? secretAsks(detail.draft, detail.verdict) : [];
  const fixOf = (f: DraftFinding) => (askedLine(asks, f) ? SECRET_CHECKS_LINE : f.fix);

  return (
    <div className="flex flex-col gap-3" data-checks>
      {findingGroups(detail.verdict).map(({ cls, lines: all }) => {
        if (FOLDED.includes(cls) && !more) return null;
        const lines = all.filter((f) => !left.has(f.key));
        if (!lines.length) return null;
        const { icon: Icon, hue } = CLASS_LOOK[cls];
        const widens = cls === "risk" ? widensOf(detail, lines) : null;
        const body = (widens ? widens.lines : lines).map((f) => {
          const agent = agentAck.get(f.key);
          const fix = fixOf(f);
          // A fingerprint shows under Details alone, so its Now and After
          // stay off the line.
          const words = printed(f) ? "" : nowAfter(f.before || "", f.after || "");
          return (
            <div key={f.key} className="flex gap-2.5 border-b border-border px-3.5 py-2 last:border-b-0" data-finding={f.code}>
              <Icon className={cn("mt-0.5 size-4 shrink-0", hue)} aria-hidden="true" />
              <div className="flex min-w-0 max-w-[75ch] flex-col gap-1 text-body leading-relaxed text-text-2">
                <span className="text-foreground">{f.sentence}</span>
                {fix && <span>{fix}</span>}
                {words && <span className="min-w-0 text-dense [overflow-wrap:anywhere]">{words}</span>}
                {agent && <span className="text-caption text-muted-foreground"><Ack b={agent} /></span>}
                {contactable(f) && (
                  <span className="flex items-center gap-1.5">
                    <Button variant="outline" size="sm" onClick={() => void contact(f.object as string)} disabled={!!contacting}>
                      {contacting === f.object && <Loader2Icon className="animate-spin" />}{CONTACT}
                    </Button>
                    <HelpTip label={CONTACT} text={CONTACT_HELP} />
                  </span>
                )}
                {contactable(f) && failed[f.object as string] && <RefusedError subject={CONTACT} heading={CONTACT_FAILED} message={failed[f.object as string]} />}
              </div>
            </div>
          );
        });
        return (
          <section key={cls} aria-label={GROUP[cls]} className="rounded-md border border-border bg-card" data-group={cls}>
            <h3 className="flex flex-wrap items-center gap-x-2 border-b border-border px-3.5 py-2 text-dense font-semibold text-foreground">
              {GROUP[cls]}
              {widens ? <span className="text-caption font-normal text-muted-foreground">{"· " + RISK_TAIL}</span> : <span className="font-mono text-caption font-normal text-muted-foreground">{lines.length}</span>}
            </h3>
            {widens ? <WidensBody detail={detail} w={widens} blocks={blocks}>{body}</WidensBody> : body}
          </section>
        );
      })}
    </div>
  );
}
