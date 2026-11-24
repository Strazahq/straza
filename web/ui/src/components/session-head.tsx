import { PowerIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Chip } from "@/components/chip";
import { KindTile } from "@/components/kind-icon";
import { PageHead } from "@/components/page-head";
import { SessionStatus } from "@/components/session-status";
import { PageLink } from "@/components/user-parts";
import type { SessionRow } from "@/lib/api";
import { routeByKey } from "@/lib/routes";
import { IN_FOLDER, ON_MACHINE, REVOKE_SESSION, SPONSORED_BY, THIS_CONSOLE, cutID, kindLine, seenLine, sessionHarness, sessionTitle, started, userKindOf } from "@/lib/session-words";
import { agoLong } from "@/lib/words";

const route = routeByKey("sessions");

type Props = {
  row: SessionRow;
  // own says the session is the one this console is signed in with.
  own: boolean;
  // sponsorID is the sponsor's user id once the users read found it, null
  // until then and without the identity area.
  sponsorID: string | null;
  onRevoke: () => void;
};

// SessionHead is the head of a session's page: the crumbs, the session
// tile, who on which harness with the status beside it, when it started on
// which machine in which folder and when it was last seen, the user and the
// sponsor as links, and Revoke session for an active session. A session
// whose user has no row names the user by the first eight characters of its
// id in the title alone.
export function SessionHead({ row, own, sponsorID, onRevoke }: Props) {
  const who = row.username || cutID(row.user_id);
  const kind = userKindOf(row.user_kind);
  const said = [kindLine(row.user_kind || ""), row.sponsor ? SPONSORED_BY : ""].filter(Boolean).join(", ");
  return (
    <PageHead
      label={cutID(row.id)}
      crumbs={[{ label: route.label, to: "sessions" }]}
      tile={<KindTile kind="session" size="lg" />}
      title={sessionTitle(who, sessionHarness(row.harness))}
      titleExtra={
        <>
          <SessionStatus status={row.status} />
          {own && <Chip tone="plain" data-this-console>{THIS_CONSOLE}</Chip>}
        </>
      }
      description={
        <>
          <span className="w-full" data-session-times>
            {started(agoLong(row.started_at))}
            {row.host && <>{" " + ON_MACHINE + " "}<span className="font-mono [overflow-wrap:anywhere]" data-session-host>{row.host}</span></>}
            {row.folder && <>{" " + IN_FOLDER + " "}<span className="font-mono [overflow-wrap:anywhere]" data-session-folder>{row.folder}</span></>}
            {". " + seenLine(agoLong(row.last_seen))}
          </span>
          {row.username && (
            <span className="flex w-full min-w-0 flex-wrap items-center gap-1.5" data-session-user>
              {kind && <KindTile kind={kind} size="sm" />}
              <PageLink to="users" rest={[row.user_id]} data-user-link={row.user_id}>{row.username}</PageLink>
              {said && <span className="text-muted-foreground">{said}</span>}
              {row.sponsor && (
                <span className="inline-flex min-w-0 items-center gap-1.5" data-sponsor={row.sponsor}>
                  <KindTile kind="person" size="sm" />
                  {sponsorID ? <PageLink to="users" rest={[sponsorID]} data-sponsor-link={sponsorID}>{row.sponsor}</PageLink> : <span>{row.sponsor}</span>}
                </span>
              )}
            </span>
          )}
        </>
      }
      actions={row.status === "active" ? (
        <Button variant="outline" className="border-danger/40 text-danger hover:bg-danger-bg hover:text-danger" onClick={onRevoke} data-revoke-session>
          <PowerIcon /> {REVOKE_SESSION}
        </Button>
      ) : undefined}
    />
  );
}
