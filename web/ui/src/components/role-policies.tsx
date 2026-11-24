import { Button } from "@/components/ui/button";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { ListRow, Section } from "@/components/sheet-parts";
import { Chip } from "@/components/chip";
import { KindTile } from "@/components/kind-icon";
import type { PolicySetRow, RoleRow } from "@/lib/api";
import { navigate } from "@/lib/router";
import { DECIDER_HELP, DECIDER_LINE, DRAFT, LIVE, OPEN_POLICY, POLICIES_HELP, SUBJECT_POLICIES, TAB, kindOf, noDecider, noPolicy, postureWords } from "@/lib/role-words";

// The Policies tab: the sets that name the role in
// their match, or, for an approver role, the live sets it decides for, each
// row led by the policy's tile. Open leads to the policy's page.

// Name is a policy's tile and its name, the lead of its row.
function Name({ name }: { name: string }) {
  return (
    <span className="flex min-w-0 items-center gap-2">
      <KindTile kind="policy-set" />
      <span className="truncate font-mono text-foreground">{name}</span>
    </span>
  );
}

type Props = {
  role: RoleRow;
  // sets are the policy sets naming the role, read by the page; empty for
  // an approver role, which reads its pools off decider_in.
  sets: PolicySetRow[];
  // problem words the failed read, and leaves the last rows on screen.
  problem: string | null;
  lastRead: Date | null;
};

export function RolePolicies({ role, sets, problem, lastRead }: Props) {
  const approver = kindOf(role) === "approver";
  const deciders = role.decider_in || [];
  const openButton = (name: string) => (
    <Button variant="ghost" size="sm" onClick={() => navigate("policies", [name])}>{OPEN_POLICY}</Button>
  );

  if (approver) {
    return (
      <Section title={TAB.policies} action={<HelpTip label={TAB.policies} text={DECIDER_HELP} />}>
        {deciders.length === 0 && <p className="text-dense text-muted-foreground">{noDecider(role.name)}</p>}
        {deciders.map((name) => (
          <ListRow key={name} actions={openButton(name)}>
            <Name name={name} />
            <Chip tone="ok" data-policy-status={LIVE}>{LIVE}</Chip>
            <span className="text-caption text-muted-foreground">{DECIDER_LINE}</span>
          </ListRow>
        ))}
      </Section>
    );
  }

  return (
    <Section title={TAB.policies} action={<HelpTip label={TAB.policies} text={POLICIES_HELP} />}>
      {problem && <FetchError subject={SUBJECT_POLICIES} detail={problem} lastRead={lastRead} />}
      {sets.length === 0 && !problem && <p className="text-dense text-muted-foreground">{noPolicy(role.name)}</p>}
      {sets.map((s) => (
        <ListRow key={s.name} actions={openButton(s.name)}>
          <Name name={s.name} />
          <Chip tone={s.status === "active" ? "ok" : "plain"} data-policy-status={s.status === "active" ? LIVE : DRAFT}>{s.status === "active" ? LIVE : DRAFT}</Chip>
          <span className="min-w-0 truncate text-caption text-muted-foreground">{postureWords(s.summary?.rules, s.summary?.postures)}</span>
        </ListRow>
      ))}
    </Section>
  );
}
