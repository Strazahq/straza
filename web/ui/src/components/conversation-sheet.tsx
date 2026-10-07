import * as React from "react";
import { Badge } from "@/components/ui/badge";
import { DownloadIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { FetchError } from "@/components/error-state";
import { type ApiError, type Transcript, sessionTranscript } from "@/lib/api";
import { readFailed } from "@/lib/say";
import { shortID } from "@/lib/session-words";
import { BODY_MISSING, BODY_UNAVAILABLE, EMPTY_TURNS, READING_TURNS, TURNS_UNREAD, clockTime, conversationJSONL, conversationText, modeWord, speaker, truncatedLine, turnsLine } from "@/lib/transcript-words";
import { cn, downloadText } from "@/lib/utils";
import { absTime, dayOf } from "@/lib/words";

// Tone names the hue of one word of this area's vocabulary: the capture
// posture, the kind of a turn, the capture mode, a lost body.
export type Tone = "ok" | "accent" | "danger" | "unknown" | "plain";

const TONE: Record<Tone, string> = {
  ok: "border-ok/40 bg-ok-bg text-ok",
  accent: "border-link/40 bg-accent-bg text-link",
  danger: "border-danger/40 bg-danger-bg text-danger",
  unknown: "border-unknown/40 bg-unknown-bg text-unknown",
  plain: "border-border bg-background text-text-2",
};

// ToneBadge renders one word in its tone. title carries the sentence that
// explains the word where a table cell has no room for it.
export function ToneBadge({ tone, title, children }: { tone: Tone; title?: string; children: React.ReactNode }) {
  return (
    <Badge variant="outline" data-tone={tone} title={title} className={cn("rounded-md px-2 text-[13px] font-normal", TONE[tone])}>
      {children}
    </Badge>
  );
}

type Props = {
  sessionID: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onOpenSession?: (id: string) => void;
  onOpenAudit?: (id: string) => void;
};

type State = { kind: "loading" } | { kind: "ready"; transcript: Transcript } | { kind: "error"; message: string };

// ConversationSheet is one session's captured conversation, read as a chat:
// the prompts on the right, the replies on the left, the capture mode named
// once in the header, a band naming the day above its first turn. A turn
// whose body the store no longer holds is said out loud with its hash,
// since that is an integrity finding. Export hands the loaded turns to the
// browser as a file, so it reads nothing the sheet has not already read.
export function ConversationSheet({ sessionID, open, onOpenChange, onOpenSession, onOpenAudit }: Props) {
  const [state, setState] = React.useState<State>({ kind: "loading" });

  React.useEffect(() => {
    if (!open) return;
    let alive = true;
    setState({ kind: "loading" });
    sessionTranscript(sessionID).then(
      (transcript) => { if (alive) setState({ kind: "ready", transcript }); },
      (e) => {
        const err = e as ApiError;
        if (alive && err.status !== 401) setState({ kind: "error", message: readFailed("The recorded turns of this session", err) });
      },
    );
    return () => { alive = false; };
  }, [sessionID, open]);

  const transcript = state.kind === "ready" ? state.transcript : null;
  const turns = transcript?.turns || [];
  const username = transcript?.username;
  const description = state.kind === "loading"
    ? READING_TURNS
    : state.kind === "error"
      ? TURNS_UNREAD
      : turns.length
        ? turnsLine(username, turns.length, turns[0].at, turns[turns.length - 1].at)
        : EMPTY_TURNS;
  const link = "h-auto p-0 text-[13px] text-link";

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-3xl" data-conversation-sheet={sessionID}>
        <SheetHeader className="border-b border-border">
          <SheetTitle className="flex flex-wrap items-center gap-2">
            <span>{"Session "}<span className="font-mono">{shortID(sessionID)}</span></span>
            {turns.length > 0 && <ToneBadge tone={turns[0].mode === "verbatim" ? "accent" : "ok"}>{modeWord(turns[0].mode)}</ToneBadge>}
          </SheetTitle>
          <SheetDescription>{description}</SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 overflow-y-auto p-4">
          {state.kind === "error" && <FetchError subject="Conversation" detail={state.message} />}
          {turns.length > 0 && (
            <div className="flex flex-col gap-2.5" data-chat>
              {turns.map((t, i) => (
                <React.Fragment key={t.at + "|" + i}>
                  {(i === 0 || dayOf(turns[i - 1].at) !== dayOf(t.at)) && (
                    <div data-day={dayOf(t.at)} className="self-center rounded-full border border-border bg-secondary px-2.5 py-0.5 font-mono text-xs text-muted-foreground">{dayOf(t.at)}</div>
                  )}
                  <div
                    data-turn={t.kind}
                    className={cn(
                      "max-w-[82%] rounded-md border border-border px-3 py-2 text-base",
                      // The side and the fill tell the speakers apart: a
                      // prompt is raised, a reply sits on the sheet's own fill.
                      t.kind === "prompt" ? "self-end bg-secondary" : "self-start bg-background",
                    )}
                  >
                    <div className="mb-1 flex items-baseline gap-2.5 text-[13px] text-muted-foreground">
                      <span className="text-xs font-semibold uppercase tracking-[.06em] text-text-2">{speaker(t, username || "")}</span>
                      <span className="font-mono" title={absTime(t.at)}>{clockTime(t.at)}</span>
                    </div>
                    {t.body_missing ? (
                      <>
                        <p className="m-0 italic text-muted-foreground">{BODY_UNAVAILABLE}</p>
                        <p className="m-0 mt-1 text-[13px] text-danger" data-body-missing>
                          {BODY_MISSING + " Hash "}<span className="font-mono">{t.content_hash}</span>
                        </p>
                      </>
                    ) : (
                      <p className="m-0 whitespace-pre-wrap break-words">{t.content}</p>
                    )}
                    {t.truncated && <p className="m-0 mt-1 text-[13px] text-muted-foreground">{truncatedLine(t.content_hash)}</p>}
                  </div>
                </React.Fragment>
              ))}
            </div>
          )}
        </div>

        <SheetFooter className="mt-0 border-t border-border">
          <div className="flex items-center gap-2">
            {(onOpenSession || onOpenAudit) && (
              <span className="text-[13px] text-muted-foreground">
                {onOpenSession && (
                  <>
                    {"Session: "}
                    <Button variant="link" size="sm" className={link} aria-label="Open this session" onClick={() => onOpenSession(sessionID)}>open</Button>
                  </>
                )}
                {onOpenSession && onOpenAudit && " · "}
                {onOpenAudit && (
                  <>
                    {"Audit trail: "}
                    <Button variant="link" size="sm" className={link} aria-label="Open the audit trail of this session" onClick={() => onOpenAudit(sessionID)}>open</Button>
                  </>
                )}
              </span>
            )}
            <div className="ml-auto flex items-center gap-2">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline" disabled={!transcript || turns.length === 0}><DownloadIcon /> Export</Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onSelect={() => transcript && downloadText("straza-conversation-" + sessionID + ".txt", conversationText(transcript), "text/plain")}>Plain text</DropdownMenuItem>
                  <DropdownMenuItem onSelect={() => transcript && downloadText("straza-conversation-" + sessionID + ".jsonl", conversationJSONL(transcript), "application/x-ndjson")}>JSONL</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
              <Button variant="outline" onClick={() => onOpenChange(false)}>Close</Button>
            </div>
          </div>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
