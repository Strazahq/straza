import * as React from "react";
import { CheckIcon, CircleHelpIcon, CircleXIcon, InfoIcon, Loader2Icon, type LucideIcon, TriangleAlertIcon } from "lucide-react";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { ApiError, DraftDetail, DraftFinding, DraftVerdict, FindingClass } from "@/lib/api";
import { UNLISTED_CODE, findingGroups, objectOf, remoteApp } from "@/lib/drafts-model";
import { CONTACT, CONTACT_FAILED, CONTACT_HELP, GROUP, NOTHING_REFUSED, STRIP_LABEL, ackWords, answered, nowAfter, stripJump, stripWord } from "@/lib/drafts-words";
import { cn } from "@/lib/utils";

// The verdict of a draft as the review page draws it: the strip that counts
// the classes, and the lines of
// every class in the server's own words. A line renders by the class of
// the list it came in, so a code this console does not know still shows.

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

const STRIP: { cls: "refused" | "risk" | "warning" | "unchecked"; list: keyof DraftVerdict; hue: string }[] = [
  { cls: "refused", list: "refused", hue: "text-danger" },
  { cls: "risk", list: "risks", hue: "text-warn" },
  { cls: "warning", list: "warnings", hue: "text-foreground" },
  { cls: "unchecked", list: "unchecked", hue: "text-unknown" },
];

// VerdictStrip counts the lines a person must read, each count a door to
// the checks below.
export function VerdictStrip({ verdict, onJump }: { verdict: DraftVerdict; onJump: () => void }) {
  return (
    <div role="group" aria-label={STRIP_LABEL} className="flex flex-wrap gap-2" data-verdict-strip>
      {STRIP.map(({ cls, list, hue }) => {
        const n = ((verdict[list] as DraftFinding[] | null) || []).length;
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
            <b className={cn("font-mono text-[17px] leading-none font-semibold", n ? hue : "text-muted-foreground")}>{n}</b>
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
};

export function DraftChecks({ detail, onContact }: Props) {
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

  return (
    <div className="flex flex-col gap-3" data-checks>
      {findingGroups(detail.verdict).map(({ cls, lines }) => {
        if (!lines.length && cls !== "refused") return null;
        const { icon: Icon, hue } = ICON[cls];
        return (
          <section key={cls} aria-label={GROUP[cls]} className="rounded-md border border-border bg-card" data-group={cls}>
            <h3 className="flex items-center gap-2 border-b border-border px-3.5 py-2 text-sm font-semibold text-foreground">
              {GROUP[cls]}
              <span className="font-mono text-[12px] font-normal text-muted-foreground">{lines.length}</span>
            </h3>
            {!lines.length && <p className="m-0 px-3.5 py-2 text-sm text-text-2">{NOTHING_REFUSED}</p>}
            {lines.map((f) => (
              <div key={f.key} className="flex gap-2.5 border-b border-border px-3.5 py-2 last:border-b-0" data-finding={f.code}>
                <Icon className={cn("mt-0.5 size-4 shrink-0", hue)} aria-hidden="true" />
                <div className="flex min-w-0 flex-col gap-1 text-sm leading-relaxed text-text-2">
                  <span className="text-foreground">{f.sentence}</span>
                  {f.fix && <span>{f.fix}</span>}
                  {(f.before || f.after) && <span className="text-[13px]">{nowAfter(f.before || "", f.after || "")}</span>}
                  {cls === "risk" && <span className="text-[12.5px] text-muted-foreground" data-ack-words>{ackWords(f.ack, f.typed)}</span>}
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
            ))}
          </section>
        );
      })}
    </div>
  );
}
