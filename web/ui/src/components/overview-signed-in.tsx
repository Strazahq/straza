import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Door, Panel, PanelNote, ToneBadge } from "@/components/overview-parts";
import type { SessionRow } from "@/lib/api";
import {
  ADMIN_CLIENT,
  NOT_MEASURED_ADMIN,
  NO_GOVERNED,
  CLIENT_VERSION_DIFFERS,
  SESSIONS_UNREAD,
  SIGNED_IN_COLUMN,
  SIGNED_IN_HELP,
  SIGNED_IN_TITLE,
  fleetLine,
  isAdminHarness,
  WIRING_NOT_REPORTED,
  seatLine,
} from "@/lib/config-words";
import { WIRING_TITLE, shortID, wiringTone } from "@/lib/session-words";
import { NONE, absTime, relTimeText } from "@/lib/words";

// Who is signed in: the active sessions, newest first and
// capped at ten, with one line of fleet posture in the header. The
// wiring word and its hue are the ones the Sessions screen uses, so a box
// reads the same on both screens.

const TH = "px-3.5 py-2 text-left text-[13px] font-semibold tracking-[.06em] whitespace-nowrap text-muted-foreground uppercase";
const TD = "px-3.5 py-2 align-middle whitespace-nowrap";

type Props = {
  // rows is the active sessions the screen read, null when the read did
  // not answer or this seat may not make it.
  rows: SessionRow[] | null;
  denied: boolean;
  // Compare with the server build without assuming version ordering.
  strazad: string;
  expanded?: boolean;
};

// Client is the straza build the box checked in with: the version, the
// word for an admin harness that carries none, or the absent word.
function Client({ row, strazad }: { row: SessionRow; strazad: string }) {
  if (!row.client_version) {
    return <span className="text-muted-foreground">{isAdminHarness(row.harness) ? ADMIN_CLIENT : NONE}</span>;
  }
  const different = !!strazad && row.client_version !== strazad;
  return (
    <span className="font-mono">
      {row.client_version}
      {different && <span className="ml-1.5 text-[13px] text-warn" data-version-differs>{CLIENT_VERSION_DIFFERS}</span>}
    </span>
  );
}

// SignedIn is the panel. It counts the governed rows only: this console
// and strazactl are people at a keyboard, not a governed agent.
export function SignedIn({ rows, denied, strazad, expanded }: Props) {
  const list = rows || [];
  const governed = list.filter((r) => !isAdminHarness(r.harness));
  const counts: Record<string, number> = {};
  for (const r of governed) {
    const status = r.wiring_status || WIRING_NOT_REPORTED;
    counts[status] = (counts[status] || 0) + 1;
  }
  const different = governed.filter((r) => r.client_version && strazad && r.client_version !== strazad).length;
  const line = governed.length ? fleetLine(governed.length, counts, different) : NO_GOVERNED;

  return (
    <Panel
      name="signed-in"
      reveal={expanded}
      title={SIGNED_IN_TITLE}
      help={SIGNED_IN_HELP}
      right={rows ? <><span data-fleet-line>{line}</span><Door to="sessions" /></> : undefined}
      flush={!!rows && list.length > 0}
    >
      {!rows ? (
        <PanelNote>{denied ? seatLine(SIGNED_IN_TITLE, "sessions:read") : SESSIONS_UNREAD}</PanelNote>
      ) : list.length === 0 ? (
        <PanelNote>{NO_GOVERNED}</PanelNote>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full table-fixed text-sm">
            <colgroup>
              <col style={{ width: "24%" }} />
              <col style={{ width: "18%" }} />
              <col style={{ width: "20%" }} />
              <col style={{ width: "20%" }} />
              <col style={{ width: "18%" }} />
            </colgroup>
            <thead>
              <tr className="border-b border-border">
                <th className={TH}>{SIGNED_IN_COLUMN.who}</th>
                <th className={TH}>{SIGNED_IN_COLUMN.harness}</th>
                <th className={TH}>{SIGNED_IN_COLUMN.client}</th>
                <th className={TH}>{SIGNED_IN_COLUMN.wiring}</th>
                <th className={TH}>{SIGNED_IN_COLUMN.seen}</th>
              </tr>
            </thead>
            <tbody>
              {list.map((r) => (
                <tr key={r.id} data-session={r.id} className="border-b border-border last:border-b-0">
                  <td className={TD}>
                    <b className="font-semibold">{r.username || shortID(r.user_id)}</b>
                  </td>
                  <td className={TD + " font-mono text-muted-foreground"}>{r.harness}</td>
                  <td className={TD}><Client row={r} strazad={strazad} /></td>
                  <td className={TD}>
                    {r.wiring_status
                      ? <ToneBadge tone={wiringTone(r.wiring_status)} word={r.wiring_status} title={WIRING_TITLE[r.wiring_status]} />
                      : <span className="text-[13px] text-muted-foreground">{isAdminHarness(r.harness) ? NOT_MEASURED_ADMIN : NONE}</span>}
                  </td>
                  <td className={TD}>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <span className="cursor-default text-text-2">{relTimeText(r.last_seen)}</span>
                      </TooltipTrigger>
                      <TooltipContent>{absTime(r.last_seen)}</TooltipContent>
                    </Tooltip>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}
