// The servers on a person's machine: an MCP server a policy rule names that
// Straza has not registered, which a person's own harness runs under that
// name. The policy page and the draft page read it with these functions, so
// both tell such a server apart the same way.
import type { DraftItem } from "./api";
import type { RuleView } from "./policy-model";

// machineServers answers the servers an MCP rule names that are not straza
// and not in registered, in the rule's order.
export function machineServers(rule: RuleView, registered: string[]): string[] {
  if (rule.lane !== "mcp" || !rule.app) return [];
  return rule.app.split(", ").filter((a) => a !== "straza" && !registered.includes(a));
}

// onMachines says whether an MCP rule names a server on a person's machine.
// registered is null while the list is unread or cannot be trusted, and then
// no rule says so.
export const onMachines = (rule: RuleView, registered?: string[] | null) => !!registered && machineServers(rule, registered).length > 0;

// registeredAfter is the list of registered servers once a draft is
// published: the live ones, plus the servers the draft puts, minus the ones
// it removes.
export function registeredAfter(live: string[], items: Pick<DraftItem, "kind" | "name" | "op">[]): string[] {
  const out = new Set(live);
  for (const it of items) {
    if (it.kind !== "App") continue;
    if (it.op === "remove") out.delete(it.name);
    else out.add(it.name);
  }
  return [...out];
}
