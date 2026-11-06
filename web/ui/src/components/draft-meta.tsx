import type * as React from "react";
import { ChevronDownIcon } from "lucide-react";
import { HelpTip } from "@/components/help-tip";
import { Button } from "@/components/ui/button";
import type { DraftDetail } from "@/lib/api";
import { navigate } from "@/lib/router";
import { DETAILS, FACT, META_DOT, MAY_PUBLISH_ALL, cameInLine, checkedLine, doorWords, expiresLine, expiresWord, kindOfAuthor, proposerWords, revisionLine, revisionWord, storedNoCheck, undoesLine } from "@/lib/drafts-words";
import { cn } from "@/lib/utils";
import { absTime, dayOf, relTimeText } from "@/lib/words";

// The facts of a draft: one line under its title, who drafted it and what
// kind of author that is, the door, the revision, when, and the expiry of an
// open draft, whose title says that a revision moves it, then a Details
// toggle that opens the facts in full under the head.

const Dot = () => <span aria-hidden="true" className="opacity-60">{META_DOT}</span>;

// DraftMeta is the line under the title. me is the reader's username.
export function DraftMeta({ detail, me, details, onDetails }: { detail: DraftDetail; me: string; details: boolean; onDetails: () => void }) {
  const d = detail.draft;
  const proposer = d.authors[0];
  const who = proposer ? proposerWords(proposer, me, d.source) : null;
  return (
    <span className="flex flex-wrap items-center gap-x-2 gap-y-1" data-meta>
      {who && <b className="font-semibold text-foreground" data-fact={FACT.by}>{who.name}</b>}
      {who?.line && <span>{kindOfAuthor(who.line)}</span>}
      {who && <Dot />}
      <span>{doorWords(d.door)}</span>
      <Dot />
      <span>{revisionWord(d.revision)}</span>
      <Dot />
      <span title={absTime(d.updated_at)}>{relTimeText(d.updated_at)}</span>
      {d.state === "open" && d.expires_at && <><Dot /><span title={expiresLine(absTime(d.expires_at))} data-fact={FACT.expires}>{expiresWord(dayOf(d.expires_at))}</span></>}
      <Button variant="link" size="sm" className="h-auto gap-0.5 p-0 text-link" aria-expanded={details} onClick={onDetails} data-details-toggle>
        {DETAILS}<ChevronDownIcon className={cn("transition-transform", details && "rotate-180")} aria-hidden="true" />
      </Button>
    </span>
  );
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <tr className="border-b border-border last:border-b-0" data-fact={label}>
      <th scope="row" className="w-[170px] py-2 pr-3 text-left align-top font-semibold whitespace-nowrap text-foreground">{label}</th>
      <td className="py-2 align-top text-text-2">{children}</td>
    </tr>
  );
}

type FactsProps = {
  detail: DraftDetail;
  // checked is the Last checked line: the page's own while it reads the
  // draft again after a publish, else empty for the stored one.
  checked: string;
  // needs is the help of Who may publish: the grant each object needs.
  needs: string;
};

// DraftFacts are the facts Details opens: where the draft came from with
// its source file, every revision, when it was last checked against which
// live policy version, who may publish an open draft, and the draft an undo
// takes back.
export function DraftFacts({ detail, checked, needs }: FactsProps) {
  const d = detail.draft;
  const v = detail.verdict;
  const open = d.state === "open";
  return (
    <table className="w-full max-w-[960px] border-collapse rounded-md text-sm" data-facts>
      <tbody>
        <Fact label={FACT.door}>{cameInLine(d.door, d.source, d.authors[0]?.client)}</Fact>
        <Fact label={FACT.revisions}>
          <ul className="m-0 flex list-none flex-col gap-0.5 p-0">{(detail.revisions || []).map((r) => <li key={r.revision}>{revisionLine(r.revision, r.author.username, r.door, absTime(r.created_at), !!r.mechanical)}</li>)}</ul>
        </Fact>
        <Fact label={FACT.checked}>{checked || (open || detail.checks ? checkedLine(absTime(v.checked_at), (v.snapshot || "").slice(0, 8)) : storedNoCheck(d.revision))}</Fact>
        {open && (
          <Fact label={FACT.needs}>
            <span className="inline-flex items-center gap-1.5">
              {detail.may_publish ? MAY_PUBLISH_ALL : detail.publish_refusal}
              <HelpTip label={FACT.needs} text={needs} />
            </span>
          </Fact>
        )}
        {d.reverts && <Fact label={FACT.undoes}><Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => navigate("drafts", [d.reverts as string])}>{undoesLine(d.reverts)}</Button></Fact>}
      </tbody>
    </table>
  );
}
