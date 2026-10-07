import type * as React from "react";
import { PowerIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Facts, ListRow, Section } from "@/components/sheet-parts";
import type { SessionRow } from "@/lib/api";
import {
  ATT_PHRASE,
  ATT_TITLE,
  RECORD_AUDIT,
  RECORD_TRANSCRIPT,
  WIRING_TITLE,
  attTone,
  harnessWords,
  revokeLabel,
  shortHash,
  shortID,
  statusTone,
  wiringPhrase,
  wiringTone,
} from "@/lib/session-words";
import { snapshot } from "@/lib/session";
import { NONE, absTime, relTimeText } from "@/lib/words";
import { cn } from "@/lib/utils";

// The detail of one session: facts and lists, no
// prose. The sentence that explains revoking lives in the confirm dialog
// the screen owns, which is why Revoke is a callback here.

// TONE paints one word of the session vocabulary in the reserved trust
// hues, the way StatusBadge does for the server statuses.
const TONE: Record<string, string> = {
  ok: "bg-ok-bg text-ok border-ok/40",
  warn: "bg-warn-bg text-warn border-warn/40",
  danger: "bg-danger-bg text-danger border-danger/40",
  unknown: "bg-unknown-bg text-unknown border-unknown/40",
  plain: "border-border bg-background text-text-2",
};

// SessionBadge renders one attestation, wiring or status word in its tone,
// with the sentence that explains the word on its tooltip.
export function SessionBadge({ tone, word, title, className }: { tone: string; word: string; title?: string; className?: string }) {
  return (
    <Badge variant="outline" title={title} data-tone={tone} className={cn("rounded-md px-2 font-mono text-[13px] font-normal", TONE[tone] || TONE.plain, className)}>
      {word}
    </Badge>
  );
}

// ThisConsole marks the session this browser is signed in with, in the
// accent, which carries no meaning beyond "you are here".
export function ThisConsole() {
  return (
    <Badge variant="outline" data-this-console className="rounded-md border-primary/40 bg-accent-bg px-2 text-[13px] font-normal text-primary">
      this console
    </Badge>
  );
}

export type SessionSheetProps = {
  session: SessionRow;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onRevoke opens the confirm dialog the screen owns, so one revoke and a
  // bulk revoke ask the same question.
  onRevoke: (session: SessionRow) => void;
  onOpenUser?: (userID: string) => void;
  onOpenTranscript?: (sessionID: string) => void;
  onOpenAudit?: (sessionID: string) => void;
};

// SessionSheet is the right-hand detail of one session row.
export function SessionSheet({ session, open, onOpenChange, onRevoke, onOpenUser, onOpenTranscript, onOpenAudit }: SessionSheetProps) {
  const who = session.username || shortID(session.user_id);
  const own = snapshot()?.sessionID === session.id;
  const wiring = session.wiring_status;
  const rows: [string, React.ReactNode][] = [
    ["Attestation", (
      <span className="flex flex-wrap items-center gap-2">
        <SessionBadge tone={attTone(session.attestation)} word={session.attestation} title={ATT_TITLE[session.attestation]} />
        <span className="text-muted-foreground">{ATT_PHRASE[session.attestation] || ""}</span>
      </span>
    )],
    ["Wiring", wiring ? (
      <span className="flex flex-wrap items-center gap-2">
        <SessionBadge tone={wiringTone(wiring)} word={wiring} title={WIRING_TITLE[wiring]} />
        <span className="text-muted-foreground">{wiringPhrase(wiring)}</span>
        {session.wiring_hash && <span className="font-mono text-muted-foreground">{shortHash(session.wiring_hash)}</span>}
      </span>
    ) : <span className="text-muted-foreground">{NONE}</span>],
    ["Session id", <span className="font-mono break-all">{session.id}</span>],
    ["User", onOpenUser
      ? <Button variant="link" className="h-auto p-0 font-mono text-link" onClick={() => onOpenUser(session.user_id)}>{who}</Button>
      : <span className="font-mono">{who}</span>],
    ["Harness", <span className="font-mono">{session.harness}</span>],
    ["Client build", session.client_version
      ? <span className="font-mono">{session.client_version}</span>
      : <span className="text-muted-foreground">{NONE}</span>],
    ["Started", absTime(session.started_at)],
    ["Last seen", absTime(session.last_seen)],
  ];

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full gap-0 p-0 sm:max-w-[560px]" data-session-sheet={session.id}>
        <SheetHeader className="border-b border-border pr-12">
          <SheetTitle className="flex flex-wrap items-center gap-2 text-lg leading-snug">
            <span>Session</span>
            <span className="font-mono">{shortID(session.id)}</span>
            <SessionBadge tone={statusTone(session.status)} word={session.status} />
            {own && <ThisConsole />}
          </SheetTitle>
          <SheetDescription>
            {who + " on " + harnessWords(session) + ". Started " + relTimeText(session.started_at) + ", seen " + relTimeText(session.last_seen) + "."}
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4">
          <Facts rows={rows} />
          <Section title="Record">
            <div className="flex flex-col gap-2">
              <ListRow actions={onOpenTranscript && <Button variant="outline" size="sm" onClick={() => onOpenTranscript(session.id)}>Open transcript</Button>}>
                <span>{RECORD_TRANSCRIPT}</span>
              </ListRow>
              <ListRow actions={onOpenAudit && <Button variant="outline" size="sm" onClick={() => onOpenAudit(session.id)}>Open audit trail</Button>}>
                <span>{RECORD_AUDIT}</span>
              </ListRow>
            </div>
          </Section>
        </div>

        <SheetFooter className="mt-0 border-t border-border">
          <div className="flex items-center gap-2">
            {session.status === "active" && (
              <Button variant="outline" className="border-danger/40 text-danger hover:bg-danger-bg hover:text-danger" onClick={() => onRevoke(session)}>
                <PowerIcon /> {revokeLabel(1)}
              </Button>
            )}
            <Button variant="outline" className="ml-auto" onClick={() => onOpenChange(false)}>Close</Button>
          </div>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
