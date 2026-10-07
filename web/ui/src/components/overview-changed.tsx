import { Door, Panel, PanelNote } from "@/components/overview-parts";
import type { AuditRow } from "@/lib/api";
import { ceRow, parseCE, rowLabel } from "@/lib/audit-words";
import { CHANGED_HELP, CHANGED_NONE, CHANGED_TITLE, CHANGES_UNREAD, seatLine } from "@/lib/config-words";
import { NONE, absTime, relTimeText } from "@/lib/words";

// What changed: the newest admin, identity and policy
// records from the chain, read as sentences with who did it. Audit holds
// every one with its record, so the panel ends in that door.

type Props = {
  // rows are the newest records the screen merged, already cut to what
  // the panel shows; null when the read did not answer or this seat may
  // not make it.
  rows: AuditRow[] | null;
  denied: boolean;
  limit?: number;
  stale?: string;
};

// actorOf is the person or token that acted on a record. The resolved
// username names the record's subject first, so alice removing joe's role
// would read as joe. A SCIM push names the admin API token it came on, and
// strazad's own background work carries no actor and keeps the username.
function actorOf(raw: AuditRow): string {
  const actor = parseCE(raw.ce)?.data?.actor;
  return typeof actor === "string" ? actor : "";
}

// Changed renders one line per record: who, what, and how long ago.
export function Changed({ rows, denied, limit, stale }: Props) {
  return (
    <Panel name="changed" title={CHANGED_TITLE} help={CHANGED_HELP} right={rows && rows.length ? <Door to="audit" /> : undefined}>
      {stale && <p className="overview-stale">Last read {stale}. Records may be outdated.</p>}
      {!rows ? (
        <PanelNote>{denied ? seatLine(CHANGED_TITLE, "audit:read") : CHANGES_UNREAD}</PanelNote>
      ) : rows.length === 0 ? (
        <PanelNote>{CHANGED_NONE}</PanelNote>
      ) : (
        <div className="flex flex-col">
          {rows.slice(0, limit).map((raw) => {
            const r = ceRow(raw);
            return (
              <div key={r.seq} data-changed={r.seq} className="overview-change-row flex flex-wrap items-baseline gap-2 border-b border-border py-1.5 text-sm first:pt-0 last:border-b-0 last:pb-0">
                <b className="font-semibold text-foreground">{actorOf(raw) || r.username || NONE}</b>
                <span className="min-w-0 flex-1 text-text-2">{rowLabel(r)}</span>
                <span className="text-[13px] whitespace-nowrap text-muted-foreground" title={absTime(r.time)}>{relTimeText(r.time)}</span>
              </div>
            );
          })}
        </div>
      )}
    </Panel>
  );
}
