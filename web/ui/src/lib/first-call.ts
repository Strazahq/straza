// The first-call watcher: after a grant lands, the server's
// row on the Access tab watches the audit log for the first call through
// that grant and names it when it comes. It is the witness that the grant
// is real, so it reads the ledger rather than the console's own state.
import * as React from "react";
import { type ApiError, type AuditRow, listAudit } from "./api";

// FirstCallRecord is the audit record the line names: who called what,
// when, and what policy did.
export type FirstCallRecord = { seq: number; time: string; username: string; tool: string; effect: string; setName?: string; ruleId?: string };

export type FirstCall = { state: "off" | "watching" | "hit" | "stopped"; rec?: FirstCallRecord };

const POLL_MS = 5000;
const STOP_MS = 5 * 60 * 1000;

// firstOf reads the newest matching record out of one page of the ledger.
// The list's q is a substring match, so rows are filtered again on the
// app itself, and a record older than the grant is skipped: a server whose
// name was used before must not show the old server's call.
function firstOf(rows: AuditRow[], app: string, since: number): FirstCallRecord | null {
  for (const row of rows) {
    let ce: { time?: string; data?: Record<string, unknown> } | null = null;
    try {
      ce = JSON.parse(row.ce) as { time?: string; data?: Record<string, unknown> };
    } catch {
      ce = null;
    }
    const d = (ce && ce.data) || {};
    if (d.app !== app) continue;
    const at = Date.parse(String(ce?.time || ""));
    if (since && (Number.isNaN(at) || at < since)) continue;
    return {
      seq: row.seq,
      time: String(ce?.time || ""),
      username: row.username || String(d.user || ""),
      tool: String(d.toolName || ""),
      effect: String(d.effect || ""),
      setName: d.setName ? String(d.setName) : undefined,
      ruleId: d.ruleId ? String(d.ruleId) : undefined,
    };
  }
  return null;
}

// useFirstCall polls the ledger every five seconds for up to five minutes.
// A 403 turns the watcher off rather than retrying: this session's grants
// do not read the audit area, and that does not change while it waits.
export function useFirstCall(app: string, enabled: boolean, since: number): FirstCall {
  const [call, setCall] = React.useState<FirstCall>({ state: "off" });

  React.useEffect(() => {
    if (!enabled || !app) {
      setCall({ state: "off" });
      return undefined;
    }
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const started = Date.now();
    setCall({ state: "watching" });
    const probe = async () => {
      if (!alive) return;
      try {
        const rows = await listAudit("type=straza.audit.mcp&q=" + encodeURIComponent(app) + "&order=desc&limit=25");
        if (!alive) return;
        const rec = firstOf(rows || [], app, since);
        if (rec) {
          setCall({ state: "hit", rec });
          return;
        }
      } catch (e) {
        if (!alive) return;
        if ((e as ApiError).status === 403) {
          setCall({ state: "off" });
          return;
        }
      }
      if (Date.now() - started >= STOP_MS) {
        setCall({ state: "stopped" });
        return;
      }
      timer = setTimeout(() => void probe(), POLL_MS);
    };
    void probe();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [app, enabled, since]);

  return call;
}
