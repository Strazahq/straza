import { Door, Panel, PanelNote } from "@/components/overview-parts";
import type { AuditRow } from "@/lib/api";
import { ACTOR_BY_ORIGIN, ceRow, parseCE, rowLabel } from "@/lib/audit-words";
import { CHANGED_HELP, CHANGED_NONE, CHANGED_TITLE, CHANGES_UNREAD, STALE_WHAT, seatLine, staleLine } from "@/lib/config-words";
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
// strazad's own background work carries no actor and keeps the username,
// unless its origin names who it acted for, as a change read in Entra does.
function actorOf(raw: AuditRow): string {
  const d = parseCE(raw.ce)?.data;
  if (typeof d?.actor === "string") return d.actor;
  return ACTOR_BY_ORIGIN[String(d?.origin || "")] || "";
}

// Changed renders one line per record: who, what, and how long ago.
export function Changed({ rows, denied, limit, stale }: Props) {
  return (
    <Panel name="changed" title={CHANGED_TITLE} help={CHANGED_HELP} right={rows && rows.length ? <Door to="audit" /> : undefined}>
      {stale && <p className="overview-stale">{staleLine(STALE_WHAT.changes, stale)}</p>}
      {!rows ? (
        <PanelNote>{denied ? seatLine(CHANGED_TITLE, "audit") : CHANGES_UNREAD}</PanelNote>
      ) : rows.length === 0 ? (
        <PanelNote>{CHANGED_NONE}</PanelNote>
      ) : (
        <div className="flex flex-col">
          {rows.slice(0, limit).map((raw) => {
            const r = ceRow(raw);
            return (
              <div key={r.seq} data-changed={r.seq} className="overview-change-row flex flex-wrap items-baseline gap-2 border-b border-border py-1.5 text-dense first:pt-0 last:border-b-0 last:pb-0">
                <b className="font-semibold text-foreground">{actorOf(raw) || r.username || NONE}</b>
                <span className="min-w-0 flex-1 text-text-2">{rowLabel(r)}</span>
                <span className="text-caption whitespace-nowrap text-muted-foreground" title={absTime(r.time)}>{relTimeText(r.time)}</span>
              </div>
            );
          })}
        </div>
      )}
    </Panel>
  );
}
