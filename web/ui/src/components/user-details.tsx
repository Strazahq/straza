import type * as React from "react";
import { Chip } from "@/components/chip";
import { Facts } from "@/components/draft-parts";
import { InEntraAs, entraObjects } from "@/components/entra-agent";
import { KIND_ICON, KindIcon } from "@/components/kind-icon";
import { SponsorName } from "@/components/user-parts";
import type { UserDetail } from "@/lib/api";
import { FROM_ENTRA, IN_ENTRA_AS, NO_SPONSOR_CHIP } from "@/lib/entra-words";
import { NOT_YET, sourceKind, sourceWord, userKind } from "@/lib/user-look";
import { AGENCY_WORD, DETAIL, NOT_SET, YES } from "@/lib/user-words";
import { NONE, absTime, agoWord, dayWord } from "@/lib/words";

const Muted = ({ children }: { children: React.ReactNode }) => <span className="text-muted-foreground">{children}</span>;

// sponsorValue is the Sponsor row. An agent from Entra has the sponsor
// Entra set, marked From Entra, or the chip no sponsor, whose why the strip
// under the head says.
function sponsorValue(user: UserDetail, sponsorID: string | null): React.ReactNode {
  const entra = user.origin === "entra" && userKind(user) === "agent";
  if (!user.sponsor) return entra ? <Chip tone="warn" data-no-sponsor="">{NO_SPONSOR_CHIP}</Chip> : <Muted>{NONE}</Muted>;
  const set = entra && user.entra && !user.entra.sponsor_state;
  return <><SponsorName name={user.sponsor} id={sponsorID} />{set && <Chip tone="plain" icon={KIND_ICON["from-entra"]} data-sponsor-from-entra>{FROM_ENTRA}</Chip>}</>;
}

// UserDetails is the Details tab: the user's own fields, in a fixed order.
// A field the answer does not carry is left out, unless it has a word for
// its absence.
export function UserDetails({ user, sponsorID }: { user: UserDetail; sponsorID: string | null }) {
  const kind = userKind(user);
  const rows: { label: string; value: React.ReactNode }[] = [
    { label: DETAIL.name, value: <span className="font-mono">{user.username}</span> },
    { label: DETAIL.email, value: user.email || <Muted>{NONE}</Muted> },
  ];
  if (user.external_id) rows.push({ label: DETAIL.external, value: <span className="font-mono break-all">{user.external_id}</span> });
  if (user.title) rows.push({ label: DETAIL.title, value: user.title });
  const source = sourceKind(user.origin);
  rows.push({ label: DETAIL.source, value: <>{source && <KindIcon kind={source} className="text-muted-foreground" />}{sourceWord(user.origin)}</> });
  if (entraObjects(user.entra).length) rows.push({ label: IN_ENTRA_AS, value: <InEntraAs user={user} /> });
  rows.push({ label: DETAIL.created, value: <span title={absTime(user.created_at)}>{dayWord(user.created_at)}</span> });
  rows.push({ label: DETAIL.seen, value: user.last_seen ? <span title={absTime(user.last_seen)}>{agoWord(user.last_seen)}</span> : <Muted>{NOT_YET}</Muted> });
  if (kind === "agent") rows.push({ label: DETAIL.agency, value: user.agency_mode ? AGENCY_WORD[user.agency_mode] || user.agency_mode : <Muted>{NOT_SET}</Muted> });
  if (kind !== "person") rows.push({ label: DETAIL.sponsor, value: sponsorValue(user, sponsorID) });
  if (user.swarm_id) rows.push({ label: DETAIL.swarm, value: <span className="font-mono">{user.swarm_id}</span> });
  if (user.ephemeral) rows.push({ label: DETAIL.ephemeral, value: YES });
  return <div data-user-details><Facts rows={rows} /></div>;
}
