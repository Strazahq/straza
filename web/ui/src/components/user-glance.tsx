import { GlanceTile } from "@/components/glance-tile";
import type { ApprovalRow, UserDetail } from "@/lib/api";
import { type AccessRow, reachedCount } from "@/lib/user-access";
import { userKind } from "@/lib/user-look";
import { GLANCE } from "@/lib/user-words";

type Tile = { key: string; n: number; words: string; onOpen: () => void };

type Props = {
  user: UserDetail;
  // access is the Access table, null while its read is out, failed or not
  // allowed. requests are the pending requests of this user, null the same
  // way.
  access: AccessRow[] | null;
  requests: ApprovalRow[] | null;
  onRoles: () => void;
  onTab: (tab: "access" | "sessions" | "agents") => void;
  onRequests: (requests: ApprovalRow[]) => void;
};

// UserGlance is the row of numbers under the head, each a door to the part
// of the page or the screen that answers it. A tile whose number is zero is
// not drawn, and neither is one whose read is out, failed or not allowed.
export function UserGlance({ user, access, requests, onRoles, onTab, onRequests }: Props) {
  const tiles: Tile[] = [];
  const roles = (user.effective_roles || []).length;
  tiles.push({ key: "roles", n: roles, words: GLANCE.roles(roles), onOpen: onRoles });
  if (access) tiles.push({ key: "servers", n: access.length, words: GLANCE.servers(access.length, reachedCount(access)), onOpen: () => onTab("access") });
  const active = user.counts?.active_sessions || 0;
  tiles.push({ key: "sessions", n: active, words: GLANCE.sessions(active), onOpen: () => onTab("sessions") });
  if (requests) tiles.push({ key: "requests", n: requests.length, words: GLANCE.requests(requests.length), onOpen: () => onRequests(requests) });
  if (userKind(user) === "person") tiles.push({ key: "agents", n: user.sponsored_count || 0, words: GLANCE.agents(user.sponsored_count || 0), onOpen: () => onTab("agents") });
  const shown = tiles.filter((t) => t.n > 0);
  if (shown.length === 0) return null;
  return (
    <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap" data-glance-row>
      {shown.map((t) => <GlanceTile key={t.key} name={t.key} n={t.n} words={t.words} onOpen={t.onOpen} />)}
    </div>
  );
}
