import { Button } from "@/components/ui/button";
import { Tag } from "@/components/chip";
import { KindTile } from "@/components/kind-icon";
import { RoleChip } from "@/components/status-badge";
import { Heading, MUTED, StateChips, Waiting } from "@/components/user-parts";
import type { Page, UserRow } from "@/lib/api";
import { put } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { NOT_YET, orderedRoles, tileStatus, userKind } from "@/lib/user-look";
import { type Read, dataOf } from "@/lib/user-read";
import { READING_AGENTS, SUBJECT_AGENTS, agentsOf, allAgentsOf, openUser } from "@/lib/user-words";
import { agoWord } from "@/lib/words";

// UserAgents is the Agents tab of a person who sponsors agents: one row per
// agent, each opening that agent's page. The read is cut at 50, and a link
// under the rows opens the list filtered to the person's agents.
export function UserAgents({ name, agents }: { name: string; agents: Read<Page<UserRow>> }) {
  const page = dataOf(agents);
  return (
    <section className="flex flex-col gap-3" data-user-agents>
      <Heading>{agentsOf(name)}</Heading>
      <Waiting read={agents} word={READING_AGENTS} subject={SUBJECT_AGENTS} />
      {(page?.items || []).map((a) => {
        const roles = orderedRoles(a.effective_roles);
        return (
          <button
            key={a.id}
            type="button"
            aria-label={openUser(a.username)}
            onClick={() => navigate("users", [a.id])}
            className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-border bg-card px-3 py-2 text-left text-dense outline-none hover:border-link focus-visible:ring-2 focus-visible:ring-ring/50"
            data-agent-row={a.username}
          >
            <KindTile kind={userKind(a)} status={tileStatus(a)} />
            <span className="min-w-0 truncate font-semibold text-foreground">{a.display || a.username}</span>
            {a.display && <span className={MUTED + " min-w-0 truncate font-mono"}>{a.username}</span>}
            {roles.length > 0 && <RoleChip name={roles[0]} />}
            {roles.length > 1 && <Tag title={roles.slice(1).join(", ")}>{"+" + (roles.length - 1)}</Tag>}
            <span className={MUTED}>{a.last_seen ? agoWord(a.last_seen) : NOT_YET}</span>
            <span className="ml-auto flex items-center gap-1.5"><StateChips user={a} /></span>
          </button>
        );
      })}
      {page && page.next_cursor && (
        <div>
          <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => navigate("users", [], false, () => put("users", { sponsor: name }))}>{allAgentsOf(name)}</Button>
        </div>
      )}
    </section>
  );
}
