import * as React from "react";
import { ArrowRightIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { type ApiError, listAudit } from "@/lib/api";
import { parseCE, whyOf } from "@/lib/audit-words";
import { AUDIT_THIS_POLICY, REEVALUATE, SUBJECT_DECISIONS, WATCHING, WATCH_STOPPED, decisionCall, firstDecision, liveSince, liveSinceNoVersion } from "@/lib/policy-words";
import { navigate } from "@/lib/router";
import { readFailed } from "@/lib/say";
import { absTime } from "@/lib/words";

// The proof banner of a policy that was just published: since when it
// runs, which version, and the first decision it
// makes. The watcher polls the audit chain for two minutes and then says
// that nothing matched, so the banner never claims more than it read.

// EVERY_MS is how often the watcher asks, and WATCH_MS how long it waits
// before it stops asking.
const EVERY_MS = 5000;
const WATCH_MS = 120000;

// VERSION_CHARS is how much of the snapshot id the banner names, the same
// prefix the wire line of a record shows.
const VERSION_CHARS = 8;

type Watch = { kind: "watching" } | { kind: "hit"; text: string } | { kind: "stopped" } | { kind: "failed"; detail: string };

// clockOf is the wall clock of a stamp in the zone the console reads,
// hours and minutes with the zone named.
function clockOf(iso: string): string {
  const stamp = absTime(iso);
  return stamp.length > 20 && stamp[13] === ":" ? stamp.slice(11, 16) + " " + stamp.slice(20) : stamp;
}

// outcomeWords finishes the first-decision line with the record sheet's own
// reading of the outcome.
const outcomeWords = (outcome: string) => outcome.replace(/\.$/, "").toLowerCase();

export function PolicyProof({ name, snapshot, at }: { name: string; snapshot?: string; at: string }) {
  const [watch, setWatch] = React.useState<Watch>({ kind: "watching" });

  React.useEffect(() => {
    let alive = true;
    let timer = 0 as unknown as ReturnType<typeof setInterval>;
    let deadline = 0 as unknown as ReturnType<typeof setTimeout>;
    const end = () => { clearInterval(timer); clearTimeout(deadline); };

    const ask = async () => {
      try {
        const rows = await listAudit("q=" + encodeURIComponent('"setName":"' + name + '"') + "&limit=1");
        if (!alive) return;
        const row = (rows || [])[0];
        const ce = row ? parseCE(row.ce) : null;
        const why = whyOf(ce);
        if (!row || !ce || !why || !ce.time || Date.parse(ce.time) <= Date.parse(at)) return;
        const data = (ce.data || {}) as Record<string, unknown>;
        const who = row.username || String(data.user || "");
        end();
        setWatch({ kind: "hit", text: firstDecision(clockOf(ce.time), who, decisionCall(data) || String(data.tool || ""), outcomeWords(why.outcome)) });
      } catch (e) {
        if (!alive) return;
        end();
        const err = e as ApiError;
        if (err.status !== 401) setWatch({ kind: "failed", detail: readFailed(SUBJECT_DECISIONS, err) });
      }
    };

    timer = setInterval(() => void ask(), EVERY_MS);
    deadline = setTimeout(() => { end(); if (alive) setWatch({ kind: "stopped" }); }, WATCH_MS);
    return () => { alive = false; end(); };
  }, [name, at]);

  const when = clockOf(at);
  return (
    <div role="status" className="flex flex-col gap-1 rounded-md border border-ok/40 bg-ok-bg px-3 py-2 text-sm leading-relaxed text-text-2" data-proof={name}>
      <span>{snapshot ? liveSince(when, snapshot.slice(0, VERSION_CHARS)) : liveSinceNoVersion(when)} {REEVALUATE}</span>
      <span className="flex flex-wrap items-center gap-2" data-watch={watch.kind}>
        {watch.kind === "watching" && WATCHING}
        {watch.kind === "stopped" && WATCH_STOPPED}
        {watch.kind === "failed" && watch.detail}
        {watch.kind === "hit" && (
          <>
            {watch.text}
            <Button variant="link" size="sm" className="h-auto gap-1 p-0 text-link" onClick={() => navigate("audit")}>
              {AUDIT_THIS_POLICY} <ArrowRightIcon />
            </Button>
          </>
        )}
      </span>
    </div>
  );
}
