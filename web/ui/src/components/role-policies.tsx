import { Button } from "@/components/ui/button";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { ListRow, Section } from "@/components/sheet-parts";
import { WordBadge } from "@/components/users-table";
import type { PolicySetRow, RoleRow } from "@/lib/api";
import { navigate } from "@/lib/router";
import { DECIDER_HELP, DECIDER_LINE, DRAFT, LIVE, OPEN_POLICY, POLICIES_HELP, SUBJECT_POLICIES, TAB, kindOf, noDecider, noPolicy, policiesLine, postureWords } from "@/lib/role-words";

// The Policies tab: the sets that name the role in
// their match, or, for an approver role, the live sets it decides for. Open
// leads to the policy's page.

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
        {deciders.length === 0 && <p className="text-sm text-muted-foreground">{noDecider(role.name)}</p>}
        {deciders.map((name) => (
          <ListRow key={name} actions={openButton(name)}>
            <span className="font-mono text-foreground">{name}</span>
            <WordBadge word={LIVE} tone="ok" attr="data-policy-status" />
            <span className="text-[13px] text-muted-foreground">{DECIDER_LINE}</span>
          </ListRow>
        ))}
      </Section>
    );
  }

  return (
    <Section title={TAB.policies}>
      {problem && <FetchError subject={SUBJECT_POLICIES} detail={problem} lastRead={lastRead} />}
      {sets.length === 0 && !problem && <p className="text-sm text-muted-foreground">{noPolicy(role.name)}</p>}
      {sets.map((s) => (
        <ListRow key={s.name} actions={openButton(s.name)}>
          <span className="font-mono text-foreground">{s.name}</span>
          <WordBadge word={s.status === "active" ? LIVE : DRAFT} tone={s.status === "active" ? "ok" : "plain"} attr="data-policy-status" />
          <span className="min-w-0 truncate text-[13px] text-muted-foreground">{postureWords(s.summary?.rules, s.summary?.postures)}</span>
        </ListRow>
      ))}
      <p className="flex items-center gap-1 text-[13px] text-muted-foreground">
        {policiesLine(role.name)}
        <HelpTip label={TAB.policies} text={POLICIES_HELP} />
      </p>
    </Section>
  );
}
