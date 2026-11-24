import * as React from "react";
import { Chip } from "@/components/chip";
import { Muted, PrincipalTag } from "@/components/draft-parts";
import { HelpTip } from "@/components/help-tip";
import { Button } from "@/components/ui/button";
import type { DraftPrincipal } from "@/lib/api";
import { UNVERIFIED } from "@/lib/approval-words";
import { OWN_WORDS, SAYS_AGENT_HELP, SHOW_ALL, SHOW_LESS, saysPersonHelp, saysTitle } from "@/lib/drafts-words";
import { cn } from "@/lib/utils";

// The note a draft came with: the author's own words, checked by nobody,
// in a dashed box quieter than the rows the server computes. It shows two
// lines until the reader opens it.

// LONG_NOTE is the length past which a note may not fit its two lines.
const LONG_NOTE = 160;

export function DraftNote({ note, author, source }: { note: string; author: DraftPrincipal; source?: string }) {
  const [all, setAll] = React.useState(false);
  const title = saysTitle(author.agent, author.username);
  const long = note.length > LONG_NOTE || note.includes("\n");
  return (
    <div className="flex max-w-[960px] flex-col gap-1.5 rounded-md border border-dashed border-border px-3.5 py-2.5" data-says>
      <span className="flex flex-wrap items-center gap-2">
        <PrincipalTag p={author} source={source} />
        <Muted>{author.agent ? OWN_WORDS.agent : OWN_WORDS.person}</Muted>
        <Chip tone="plain" data-unverified={UNVERIFIED}>{UNVERIFIED}</Chip>
        <HelpTip label={title} text={author.agent ? SAYS_AGENT_HELP : saysPersonHelp(author.username)} />
        {long && (
          <Button variant="ghost" size="xs" className="ml-auto text-link" aria-expanded={all} onClick={() => setAll((a) => !a)} data-note-toggle>
            {all ? SHOW_LESS : SHOW_ALL}
          </Button>
        )}
      </span>
      <p className={cn("m-0 max-w-[96ch] text-dense break-words whitespace-pre-wrap text-text-2", !all && "line-clamp-2")} data-note>{note}</p>
    </div>
  );
}
