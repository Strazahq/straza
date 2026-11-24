import type * as React from "react";
import { MessagesSquareIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FetchError } from "@/components/error-state";
import { SessionCard } from "@/components/session-card";
import type { Transcript, TurnRow } from "@/lib/api";
import { navigate } from "@/lib/router";
import { needsArea } from "@/lib/say";
import { NOT_RECORDED, NOT_RECORDED_LINE, OPEN_TRANSCRIPT, READING_RECORDING, RECORDED, RECORDING, SUBJECT_RECORDING, turnsSoFar } from "@/lib/session-words";
import type { Read } from "@/lib/user-read";

// recordedMode is the mode the turns carry: verbatim when every turn is
// word for word, redact when none is, and mixed when they differ. A turn
// whose mode is not verbatim reads as masked, the way the Transcripts
// screen reads it.
export function recordedMode(turns: Pick<TurnRow, "mode">[]): "verbatim" | "redact" | "mixed" {
  const verbatim = turns.filter((t) => t.mode === "verbatim").length;
  return verbatim === turns.length ? "verbatim" : verbatim === 0 ? "redact" : "mixed";
}

type Props = {
  id: string;
  // allowed says the reader's role includes the transcripts area.
  allowed: boolean;
  read: Read<Transcript>;
};

// SessionRecording is the Recording card: how the session is recorded and
// how many turns are kept, with a door to the transcript, or that no
// policy records it.
export function SessionRecording({ id, allowed, read }: Props) {
  let body: React.ReactNode = null;
  if (!allowed) body = <p className="m-0 text-dense text-text-2" data-needs-area>{needsArea("The recording is", "transcripts")}</p>;
  else if (read.kind === "loading") body = <p className="m-0 text-dense text-muted-foreground" role="status">{READING_RECORDING}</p>;
  else if (read.kind === "failed") body = <FetchError subject={SUBJECT_RECORDING} detail={read.message} />;
  else if (read.kind === "ready") {
    const turns = read.data.turns || [];
    body = turns.length === 0 ? (
      <div className="min-w-0" data-recording="none">
        <p className="m-0 text-dense font-semibold text-foreground">{NOT_RECORDED}</p>
        <p className="m-0 text-dense text-text-2">{NOT_RECORDED_LINE}</p>
      </div>
    ) : (
      <>
        <div className="flex min-w-0 items-start gap-2.5" data-recording={recordedMode(turns)}>
          <MessagesSquareIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <div className="min-w-0">
            <p className="m-0 text-dense font-semibold text-foreground">{RECORDED[recordedMode(turns)]}</p>
            <p className="m-0 text-dense text-text-2">{turnsSoFar(turns.length)}</p>
          </div>
        </div>
        <Button variant="outline" size="sm" className="self-start" onClick={() => navigate("transcripts", [id])}>{OPEN_TRANSCRIPT}</Button>
      </>
    );
  }
  return <SessionCard title={RECORDING} data-recording-card>{body}</SessionCard>;
}
