import * as React from "react";
import { CheckIcon, CircleHelpIcon, CircleXIcon, InfoIcon, Loader2Icon, type LucideIcon, TriangleAlertIcon } from "lucide-react";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { ApiError, DraftDetail, DraftFinding, DraftVerdict, FindingClass } from "@/lib/api";
import { holdersOfRole } from "@/lib/drafts-access";
import { COVERS_WIDER_CODE, type ConfirmBlock, UNLISTED_CODE, askedLine, confirmBlocks, findingGroups, objectOf, remoteApp, secretAsks } from "@/lib/drafts-model";
import { CONTACT, CONTACT_FAILED, CONTACT_HELP, GROUP, NOTHING_TO_CONFIRM, SECRET_CHECKS_LINE, STRIP_LABEL, ackWords, answered, holdsWords, nowAfter, riskTail, stripJump, stripWord } from "@/lib/drafts-words";
import { cn } from "@/lib/utils";

// The verdict of a draft as the review page draws it: the strip that counts
// the classes that hold a line, and the lines of every class in the
// server's own words, an empty class left out and Passed folded. A line
// renders by the class of the list it came in, so a code this console does
// not know still shows.

export type ChipTone = "danger" | "warn" | "unknown" | "accent" | "ok" | "plain";

const TONE: Record<ChipTone, string> = {
  danger: "bg-danger-bg text-danger border-danger/40",
  warn: "bg-warn-bg text-warn border-warn/40",
  unknown: "bg-unknown-bg text-unknown border-unknown/40",
  accent: "bg-accent-bg text-link border-link/40",
  ok: "bg-ok-bg text-ok border-ok/40",
  plain: "",
};

// DraftChip is one word of the drafts vocabulary in its trust hue, with
// violet for what cannot be known, which the users table's badge lacks.
export function DraftChip({ word, tone, title, attr }: { word: string; tone: ChipTone; title?: string; attr?: string }) {
  const data = attr ? { [attr]: word } : {};
  return <Badge variant="outline" title={title} className={cn("rounded-md px-2 text-[13px] font-normal", TONE[tone])} {...data}>{word}</Badge>;
}

// NameChips lists tool and object names as chips that wrap, the role chip's
// look. A name marked added reads in the ok tone with its mark.
export function NameChips({ names, added, mark }: { names: string[]; added?: Set<string>; mark?: (name: string) => string }) {
  return (
    <span className="flex flex-wrap gap-1.5" data-names>
      {names.map((n) => {
        const plus = !!added?.has(n);
        return (
          <span key={n} data-name={n} data-added={plus || undefined} className={cn("inline-block rounded-md border px-1.5 py-px font-mono text-[13px]", plus ? "border-ok text-ok" : "border-border bg-background text-text-2")}>
            {plus && mark ? mark(n) : n}
          </span>
        );
      })}
    </span>
  );
}

const STRIP: { cls: "refused" | "risk" | "warning" | "unchecked"; list: keyof DraftVerdict; hue: string }[] = [
  { cls: "refused", list: "refused", hue: "text-danger" },
  { cls: "risk", list: "risks", hue: "text-warn" },
  { cls: "warning", list: "warnings", hue: "text-foreground" },
  { cls: "unchecked", list: "unchecked", hue: "text-unknown" },
];

// VerdictStrip counts the lines a person must read, each count a door to
// the checks below. A class with no line is left out, and a verdict with
// none says there is nothing to confirm.
export function VerdictStrip({ verdict, onJump }: { verdict: DraftVerdict; onJump: () => void }) {
  const shown = STRIP.map((s) => ({ ...s, n: ((verdict[s.list] as DraftFinding[] | null) || []).length })).filter((s) => s.n > 0);
  return (
    <div role="group" aria-label={STRIP_LABEL} className="flex flex-wrap gap-2" data-verdict-strip>
      {!shown.length && <DraftChip word={NOTHING_TO_CONFIRM} tone="plain" attr="data-strip-none" />}
      {shown.map(({ cls, hue, n }) => {
        const word = stripWord[cls](n);
        return (
          <button
            key={cls}
            type="button"
            aria-label={stripJump(n, word)}
            onClick={onJump}
            data-strip={cls}
            className="inline-flex items-center gap-2 rounded-md border border-border bg-card px-3 py-1.5 text-sm text-text-2 outline-none hover:bg-secondary/60 focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            <b className={cn("font-mono text-[17px] leading-none font-semibold", hue)}>{n}</b>
            {word}
          </button>
        );
      })}
    </div>
  );
}

const ICON: Record<FindingClass, { icon: LucideIcon; hue: string }> = {
  refused: { icon: CircleXIcon, hue: "text-danger" },
  risk: { icon: TriangleAlertIcon, hue: "text-warn" },
  warning: { icon: TriangleAlertIcon, hue: "text-warn" },
  unchecked: { icon: CircleHelpIcon, hue: "text-unknown" },
  passed: { icon: CheckIcon, hue: "text-ok" },
  info: { icon: InfoIcon, hue: "text-muted-foreground" },
};

type Props = {
  detail: DraftDetail;
  // onContact asks the page to contact one proposed server and read the
  // draft again; it throws the server's refusal. Absent, no line offers it.
  onContact?: (object: string) => Promise<void>;
  // omit are the keys of lines the page already says elsewhere.
  omit?: string[];
};

export function DraftChecks({ detail, onContact, omit }: Props) {
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

  // Each risk says how the publish dialog asks for it, by the confirm block
  // that holds it, so a grouped line names the count to type.
  const blockOf = new Map<string, ConfirmBlock>();
  for (const b of confirmBlocks(detail.verdict.risks || [])) for (const f of b.lines) blockOf.set(f.key, b);
  const left = new Set(omit || []);
  // holds is the client's holders sentence after a covers widening, whose
  // own sentence never counts people.
  const holds = (f: DraftFinding) => {
    const n = f.code === COVERS_WIDER_CODE && f.object ? holdersOfRole(detail.verdict, f.object.replace(/^Role\//, "")) : null;
    return n ? " " + holdsWords(n) : "";
  };
  // fixOf is a line's fix. On an open draft the publish dialog asks for a
  // server's secret and listing credential, so their warnings say that in
  // place of the fix that names strazactl, or of none.
  const asks = open ? secretAsks(detail.draft, detail.verdict) : [];
  const fixOf = (f: DraftFinding) => (askedLine(asks, f) ? SECRET_CHECKS_LINE : f.fix);

  return (
    <div className="flex flex-col gap-3" data-checks>
      {findingGroups(detail.verdict).map(({ cls, lines: all }) => {
        const lines = all.filter((f) => !left.has(f.key));
        if (!lines.length) return null;
        const { icon: Icon, hue } = ICON[cls];
        const head = (
          <>
            {GROUP[cls]}
            <span className="font-mono text-[12px] font-normal text-muted-foreground">{lines.length}</span>
            {cls === "risk" && <span className="text-[13px] font-normal text-muted-foreground">{"· " + riskTail(lines.length)}</span>}
          </>
        );
        const body = lines.map((f) => {
          const block = blockOf.get(f.key);
          const fix = fixOf(f);
          return (
            <div key={f.key} className="flex gap-2.5 border-b border-border px-3.5 py-2 last:border-b-0" data-finding={f.code}>
              <Icon className={cn("mt-0.5 size-4 shrink-0", hue)} aria-hidden="true" />
              <div className="flex min-w-0 max-w-[75ch] flex-col gap-1 text-[15px] leading-relaxed text-text-2">
                <span className="text-foreground">{f.sentence + holds(f)}</span>
                {fix && <span>{fix}</span>}
                {(f.before || f.after) && <span className="text-sm">{nowAfter(f.before || "", f.after || "")}</span>}
                {cls === "risk" && block && <span className="text-[13px] text-muted-foreground" data-ack-words>{ackWords(block)}</span>}
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
        const H = "flex items-center gap-2 border-b border-border px-3.5 py-2 text-sm font-semibold text-foreground";
        if (cls === "passed") {
          return (
            <details key={cls} aria-label={GROUP[cls]} className="group rounded-md border border-border bg-card" data-group={cls}>
              <summary className={cn(H, "cursor-pointer border-b-0 group-open:border-b")}>{head}</summary>
              {body}
            </details>
          );
        }
        return (
          <section key={cls} aria-label={GROUP[cls]} className="rounded-md border border-border bg-card" data-group={cls}>
            <h3 className={H}>{head}</h3>
            {body}
          </section>
        );
      })}
    </div>
  );
}
