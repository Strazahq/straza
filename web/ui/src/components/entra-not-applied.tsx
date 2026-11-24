import * as React from "react";
import { CheckIcon, ChevronRightIcon, CopyIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Chip, Pill, type Tone } from "@/components/chip";
import { ENTRA_TYPE_ICON, OpenInEntra, RemoveInEntra } from "@/components/entra-account";
import type { RoleEntraGroup } from "@/lib/api";
import { copyText } from "@/lib/clipboard";
import { COPY_ID, EVERY_MEMBER_APPLIED, NOT_APPLIED_TITLE, byReason, copiedID, entraAccountURL, kindWord, reasonFix, reasonWord } from "@/lib/entra-words";

// REASON_TONE marks the reasons a person has to act on. Every other reason,
// a new one included, is plain.
const REASON_TONE: Record<string, Tone> = { unread: "warn", "name-taken": "warn" };

// OPENS_ACCOUNT are the reasons whose fix is on the member's account, so
// each such member links to it in Entra. Straza cannot name the user that
// holds a taken sign-in name, since the group's row carries only the
// member's ID and type. REMOVES_IN_GROUP are the reasons whose fix is to
// remove the member from the group, so each such member links to it.
const OPENS_ACCOUNT = ["name-taken"];
const REMOVES_IN_GROUP = ["not-agent"];

type Props = {
  group: RoleEntraGroup;
  // name is the group's name in Entra, for the hover of its link.
  name: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // reveal scrolls the fold into view once, when the block's not applied
  // pill opened it, and onRevealed hands the request back.
  reveal: boolean;
  onRevealed: () => void;
};

// EntraNotApplied is the fold under a role's holders: the members of its
// group in Entra that Straza did not give the role, grouped by reason, each
// reason with what to do, each member with its type and its Entra object
// ID to copy. Nothing shows while the group is being created.
export function EntraNotApplied({ group, name, open, onOpenChange, reveal, onRevealed }: Props) {
  const box = React.useRef<HTMLDetailsElement>(null);
  React.useEffect(() => {
    if (!reveal) return;
    box.current?.scrollIntoView({ block: "center" });
    onRevealed();
  }, [reveal, onRevealed]);

  if (group.state === "creating") return null;
  const members = group.not_applied || [];
  if (!members.length) return <div><Pill icon={CheckIcon} data-entra-all-applied>{EVERY_MEMBER_APPLIED}</Pill></div>;
  return (
    <details
      ref={box}
      open={open}
      onToggle={(e) => { if (e.currentTarget.open !== open) onOpenChange(e.currentTarget.open); }}
      className="group/na min-w-0 rounded-md border border-border bg-card"
      data-entra-not-applied-list
    >
      <summary className="flex cursor-pointer list-none flex-wrap items-center gap-2 px-3 py-2 text-dense [&::-webkit-details-marker]:hidden">
        <ChevronRightIcon className="size-3.5 shrink-0 text-muted-foreground transition-transform group-open/na:rotate-90" aria-hidden="true" />
        <span className="font-semibold text-foreground">{NOT_APPLIED_TITLE}</span>
        <Pill>{String(members.length)}</Pill>
      </summary>
      {byReason(members).map(([reason, ms]) => {
        const fix = reasonFix(reason, ms.map((m) => m.kind));
        return (
          <div key={reason} className="grid gap-2 border-t border-border px-3 py-2.5" data-entra-reason={reason}>
            <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-caption text-text-2">
              <Chip tone={REASON_TONE[reason] || "plain"} title={fix || undefined}>{reasonWord(reason)}</Chip>
              {fix && <span className="min-w-0 flex-1 basis-[240px]">{fix}</span>}
            </div>
            {ms.map((m) => {
              const url = OPENS_ACCOUNT.includes(reason) ? entraAccountURL(m.kind, m.id) : "";
              return (
                <div key={m.id} className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1 pl-1" data-entra-member={m.id}>
                  <Chip tone="plain" icon={ENTRA_TYPE_ICON[m.kind]} data-entra-kind={m.kind}>{kindWord(m.kind)}</Chip>
                  <span className="flex min-w-0 items-center gap-0.5">
                    <span className="min-w-0 font-mono text-caption [overflow-wrap:anywhere] text-muted-foreground">{m.id}</span>
                    <Button variant="ghost" size="icon-xs" aria-label={COPY_ID} title={COPY_ID} onClick={() => copyText(m.id, copiedID(m.id))}><CopyIcon /></Button>
                  </span>
                  {url && <span className="basis-full"><OpenInEntra url={url} who={m.id} /></span>}
                  {REMOVES_IN_GROUP.includes(reason) && group.group_id && <span className="basis-full"><RemoveInEntra groupID={group.group_id} group={name} /></span>}
                </div>
              );
            })}
          </div>
        );
      })}
    </details>
  );
}
