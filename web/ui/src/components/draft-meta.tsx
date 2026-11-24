import type * as React from "react";
import { CheckIcon, ChevronDownIcon, ClockIcon } from "lucide-react";
import { Pill, Tag } from "@/components/chip";
import { AuthorPill, Facts, PrincipalTag } from "@/components/draft-parts";
import { KIND_ICON } from "@/components/kind-icon";
import { Button } from "@/components/ui/button";
import type { DraftDetail } from "@/lib/api";
import { DETAILS, FACT2, MAY_NOT_YOU, MAY_YOU, WAITS_WHOLE, agoPill, cameInLine, checkedLine, draftTitle, expiresLine, revisionLine } from "@/lib/drafts-words";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { cn } from "@/lib/utils";
import { absTime, agoWord, dayWord } from "@/lib/words";

// The head of a draft's page under its title: one row of pills, the draft,
// who drafted it, and how long ago it was written or decided, then a
// Details toggle that opens the rest of its facts under the head.

// DraftMeta is the row of pills under the title. me is the reader's
// username.
export function DraftMeta({ detail, me, details, onDetails }: { detail: DraftDetail; me: string; details: boolean; onDetails: () => void }) {
  const d = detail.draft;
  const proposer = d.authors[0];
  // A decided draft says when it was decided. An expired draft that the
  // server never stamped as decided expired at its expiry.
  const at = d.state === "open" ? d.updated_at : d.decided_at || (d.state === "expired" ? d.expires_at : undefined) || d.updated_at;
  const ago = d.state === "open" ? agoWord(at) : agoPill(d.state, agoWord(at));
  return (
    <span className="mt-1.5 flex flex-wrap items-center gap-2" data-meta>
      <Tag icon={KIND_ICON.draft} data-meta-draft>{draftTitle(d.id)}</Tag>
      {proposer && <PrincipalTag p={proposer} source={d.source} />}
      {proposer && <AuthorPill p={proposer} me={me} door={d.door} />}
      <Pill icon={ClockIcon} title={absTime(at)} data-meta-ago>{ago}</Pill>
      <Button variant="link" size="sm" className="h-auto gap-0.5 p-0 text-link" aria-expanded={details} onClick={onDetails} data-details-toggle>
        {DETAILS}<ChevronDownIcon className={cn("transition-transform", details && "rotate-180")} aria-hidden="true" />
      </Button>
    </span>
  );
}

// DraftLink is a tag that opens another draft's page.
function DraftLink({ id }: { id: string }) {
  return (
    <a
      href={pathFor("drafts", [id])}
      className="rounded-md outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50"
      onClick={(e) => {
        if (!isPlainClick(e)) return;
        e.preventDefault();
        navigate("drafts", [id]);
      }}
    >
      <Tag icon={KIND_ICON.draft}>{draftTitle(id)}</Tag>
    </a>
  );
}

// DraftFacts are the facts Details opens: where the draft came from, its
// revision, when it was last checked, who may publish an open draft and
// when it expires, and the draft an undo takes back. needs is the grants
// each object needs to publish, in sentences, and where no object needs
// one the server's own reason stands in its place.
export function DraftFacts({ detail, needs }: { detail: DraftDetail; needs: string }) {
  const d = detail.draft;
  const v = detail.verdict;
  const open = d.state === "open";
  const newest = [...(detail.revisions || [])].sort((a, b) => b.revision - a.revision)[0];
  const checkedAt = open ? v.checked_at : detail.checks?.checked_at;
  const rows: { label: string; value: React.ReactNode }[] = [
    // A file's path can be wider than a phone, so this pill wraps.
    { label: FACT2.door, value: <Pill className="max-w-full whitespace-normal break-all">{cameInLine(d.door, d.source, d.authors[0]?.client)}</Pill> },
    {
      label: FACT2.revision,
      value: <Pill title={newest ? revisionLine(newest.revision, newest.author.username, newest.door, absTime(newest.created_at), !!newest.mechanical) : undefined}>{d.revision}</Pill>,
    },
  ];
  if (checkedAt) rows.push({ label: FACT2.checked, value: <Pill icon={ClockIcon} title={checkedLine(absTime(checkedAt), (v.snapshot || "").slice(0, 8))}>{agoWord(checkedAt)}</Pill> });
  if (open) {
    rows.push({
      label: FACT2.may,
      value: detail.may_publish
        ? <Pill icon={CheckIcon}>{MAY_YOU}</Pill>
        : <><Pill>{MAY_NOT_YOU}</Pill><span className="min-w-0 basis-full text-dense text-text-2 sm:basis-auto">{[needs || detail.publish_refusal, WAITS_WHOLE].filter(Boolean).join(" ")}</span></>,
    });
  }
  if (open && d.expires_at) rows.push({ label: FACT2.expires, value: <Pill title={expiresLine(absTime(d.expires_at))}>{dayWord(d.expires_at)}</Pill> });
  if (d.reverts) rows.push({ label: FACT2.undoes, value: <DraftLink id={d.reverts} /> });
  return (
    <div className="max-w-[960px]" data-facts>
      <Facts rows={rows} />
    </div>
  );
}
