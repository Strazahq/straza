import * as React from "react";
import { PlayIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { KindTile } from "@/components/kind-icon";
import { EntraTag, OpenInEntra } from "@/components/entra-account";
import { PageHead } from "@/components/page-head";
import { KindChip, MUTED, SponsorName, StateChips } from "@/components/user-parts";
import { type Stop, StopMenu } from "@/components/user-stop-menu";
import type { UserDetail } from "@/lib/api";
import { TEST_A_CALL } from "@/lib/policy-words";
import { routeByKey } from "@/lib/routes";
import { entraAccount, refusal, tileStatus, userKind, userState } from "@/lib/user-look";
import { NOT_SEEN_YET, NO_SPONSOR, SPONSORED_BY, agencyLine, lastSeenLine, sponsorsLine, whatAgent, whereFrom } from "@/lib/user-words";
import { agoLong } from "@/lib/words";

const route = routeByKey("users");

// Sentence is the line under the title, in this order, each part a
// sentence of its own: where the user comes from, the sponsorship, the
// last check-in. A user from Entra says where it comes from as the tag
// Entra and a link to its account there instead.
export function Sentence({ user, sponsorID }: { user: UserDetail; sponsorID: string | null }) {
  const kind = userKind(user);
  // The sponsor and its full stop are one unit too, so the stop never starts
  // a line of its own.
  const sponsor = user.sponsor ? <>{SPONSORED_BY} <span className="inline-block"><SponsorName name={user.sponsor} id={sponsorID} />.</span></> : null;
  const entra = user.origin === "entra";
  const url = entraAccount(user);
  const parts: React.ReactNode[] = entra ? [<EntraTag url="" who={user.username} />, url ? <OpenInEntra url={url} who={user.username} /> : null] : [];
  if (kind === "agent") parts.push(entra ? agencyLine(user.agency_mode) : whatAgent(user.agency_mode, user.origin), sponsor || NO_SPONSOR);
  else if (kind === "service") parts.push(entra ? null : whereFrom(user.origin), sponsor);
  else parts.push(entra ? null : whereFrom(user.origin), user.sponsored_count > 0 ? sponsorsLine(user.sponsored_count) : null);
  parts.push(user.last_seen ? lastSeenLine(agoLong(user.last_seen)) : NOT_SEEN_YET);
  const shown = parts.filter((p) => p !== null);
  // Each part is an inline block, so the line breaks between sentences and a
  // part wraps inside itself only when it is wider than the line.
  return <span data-user-sentence>{shown.map((p, i) => <React.Fragment key={i}>{i > 0 && " "}<span className="inline-block">{p}</span></React.Fragment>)}</span>;
}

type Props = {
  user: UserDetail;
  sponsorID: string | null;
  // canTest and sessions say the reader's role includes the policy and the
  // sessions areas.
  canTest: boolean;
  sessions: boolean;
  // entraOwned says Entra owns the user, so Stop access sends a disable to
  // Entra.
  entraOwned: boolean;
  onTest: () => void;
  onStop: (stop: Stop) => void;
};

// UserHead is the head of a user's page: the crumbs, the kind tile, the
// name with the kind and the state beside it, the sentence, Test a call
// and Stop access.
export function UserHead({ user, sponsorID, canTest, sessions, entraOwned, onTest, onStop }: Props) {
  const title = user.display || user.username;
  const state = userState(user);
  return (
    <PageHead
      label={user.username}
      crumbs={[{ label: route.label, to: "users" }]}
      tile={<KindTile kind={userKind(user)} size="lg" status={tileStatus(user)} />}
      title={title}
      titleExtra={
        <>
          {title !== user.username && <span className={MUTED + " min-w-0 font-mono [overflow-wrap:anywhere]"} data-sign-in>{user.username}</span>}
          <KindChip user={user} />
          <StateChips user={user} />
        </>
      }
      description={<Sentence user={user} sponsorID={sponsorID} />}
      actions={
        <>
          {canTest && !refusal(user) && <Button variant="outline" onClick={onTest}><PlayIcon />{TEST_A_CALL}</Button>}
          <StopMenu name={user.username} sessions={sessions} active={user.counts?.active_sessions || 0} locked={state.locks.length > 0} disabled={state.disabled} entra={entraOwned ? { url: entraAccount(user) } : null} onStop={onStop} />
        </>
      }
    />
  );
}
