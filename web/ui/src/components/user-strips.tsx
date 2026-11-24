import { ArrowRightIcon, CircleCheckIcon, CircleSlashIcon, Loader2Icon, LockIcon, UserRoundIcon, XIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Pill } from "@/components/chip";
import { EntraTag } from "@/components/entra-account";
import { NoSponsor, ObjectChip, entraObjects } from "@/components/entra-agent";
import { RefusedError } from "@/components/error-state";
import type { UserDetail } from "@/lib/api";
import { byDisable, entraAccount, userState } from "@/lib/user-look";
import { DETAIL, DISABLED_STRIP, DISMISS, ENABLE, LEFT_BY_DISABLE, LEFT_BY_ENTRA, LEFT_BY_IDM, LOCK_STRIP, UNLOCK, assignedBanner, lockBy, openSessions, rolesKept } from "@/lib/user-words";
import { absTime, agoWord } from "@/lib/words";

const STRIP = "flex flex-wrap items-center gap-2 rounded-md border px-4 py-2.5 text-dense leading-6";

// LEFT_BY is the reason a lock strip gives for a row that a disable wrote
// and that outlived it, by the row's origin.
const LEFT_BY: Record<string, string> = { admin: LEFT_BY_DISABLE, scim: LEFT_BY_IDM, entra: LEFT_BY_ENTRA };

type Props = {
  user: UserDetail;
  // entraOwned says Entra owns the user, which Straza then never enables.
  entraOwned: boolean;
  // sponsorNames are the Straza names of the people Entra lists as an
  // agent's sponsors, by object ID, as far as the page found them.
  sponsorNames?: Record<string, string>;
  // assigned is the role a write just assigned, whose banner stays until
  // it is dismissed or the page is left.
  assigned: string | null;
  // sessions says the reader's role includes the sessions area.
  sessions: boolean;
  enabling: boolean;
  // refused is the server's sentence for an Enable it refused, the one
  // write of the page with no dialog to hold it.
  refused: { subject: string; text: string } | null;
  onUnlock: () => void;
  onEnable: () => void;
  onOpenSessions: () => void;
  onDismiss: () => void;
};

// disabledBy says who disabled the user, read off the rows a disable
// wrote: the identity manager, else an administrator, else nobody named.
function disabledBy(user: UserDetail): string | null {
  const rows = user.locks || [];
  if (rows.some((l) => l.origin === "scim")) return lockBy("scim");
  if (rows.some((l) => l.origin === "admin-disable" || l.origin === "admin")) return lockBy("admin");
  return null;
}

// UserStrips are the strips between the head and the glance tiles, each in
// its state: one per lock, the disabled user, why an agent from Entra has
// no sponsor, the role just assigned, and a refused Enable. A user
// disabled in Entra, or one Entra owns, has the tag Entra in place of who
// disabled it, with the account's link, when Entra disabled it and the
// roles it keeps. An agent's strip also names which of its objects Entra
// disabled, and its tag opens the agent identity, the one object whose ID
// the answer carries.
export function UserStrips({ user, entraOwned, sponsorNames = {}, assigned, sessions, enabling, refused, onUnlock, onEnable, onOpenSessions, onDismiss }: Props) {
  const state = userState(user);
  const fromEntra = state.disabled ? (user.locks || []).find((l) => l.origin === "entra") : undefined;
  const entra = state.disabled && (entraOwned || !!fromEntra);
  const who = state.disabled && !entra ? disabledBy(user) : null;
  const roles = (user.effective_roles || []).length;
  const active = user.counts?.active_sessions || 0;
  const off = entraObjects(user.entra, "disabled");
  const tagURL = off.length && !off.includes("agentIdentity") ? "" : entraAccount(user);
  return (
    <>
      {state.locks.map((l, i) => (
        <div key={l.origin + l.created_at + i} role="group" aria-label={LOCK_STRIP} className={STRIP + " border-danger bg-danger-bg"} data-lock-strip={l.origin}>
          <LockIcon className="size-4 shrink-0 text-danger" aria-hidden="true" />
          <Pill className="bg-card">{lockBy(l.origin)}</Pill>
          <Pill className="bg-card" title={absTime(l.created_at)}>{agoWord(l.created_at)}</Pill>
          <span className="min-w-0 break-words text-foreground" data-lock-reason>{byDisable(l) ? LEFT_BY[l.origin] : l.reason}</span>
          {i === 0 && <Button variant="outline" size="sm" className="ml-auto" onClick={onUnlock}>{UNLOCK}</Button>}
        </div>
      ))}
      {state.disabled && (
        <div role="group" aria-label={DISABLED_STRIP} className={STRIP + " border-border bg-card"} data-disabled-strip={entra ? "entra" : ""}>
          <CircleSlashIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          {who && <Pill>{who}</Pill>}
          {entra && <EntraTag url={tagURL} who={user.username} />}
          {entra && off.map((k) => <ObjectChip key={k} kind={k} />)}
          {fromEntra && <Pill title={absTime(fromEntra.created_at)}>{agoWord(fromEntra.created_at)}</Pill>}
          {entra && roles > 0 && <Pill data-roles-kept={roles}>{rolesKept(roles)}</Pill>}
          {!entraOwned && <Button variant="outline" size="sm" className="ml-auto" disabled={enabling} onClick={onEnable}>{enabling && <Loader2Icon className="animate-spin" />}{ENABLE}</Button>}
        </div>
      )}
      {user.entra?.sponsor_state && (
        <div role="group" aria-label={DETAIL.sponsor} className={STRIP + " border-border bg-card"} data-sponsor-strip={user.entra.sponsor_state}>
          <UserRoundIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <NoSponsor entra={user.entra} names={sponsorNames} />
        </div>
      )}
      {assigned && (
        <div role="status" className={STRIP + " border-ok bg-ok-bg"} data-assigned={assigned}>
          <CircleCheckIcon className="size-4 shrink-0 text-ok" aria-hidden="true" />
          <span className="min-w-0 flex-1 text-foreground">{assignedBanner(assigned, user.username)}</span>
          {sessions && active > 0 && <Button variant="outline" size="sm" onClick={onOpenSessions}><ArrowRightIcon />{openSessions(user.username, active)}</Button>}
          <Button variant="ghost" size="icon-xs" aria-label={DISMISS} onClick={onDismiss}><XIcon /></Button>
        </div>
      )}
      {refused && <RefusedError subject={refused.subject} message={refused.text} />}
    </>
  );
}
