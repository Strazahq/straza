// The hold or ticket behind each tool a preview says needs approval. The
// server's preview names the set and the rule that hold a tool but not the
// rule's class, so a reading surface reads each such set once and looks the
// rule up in its stored text. A rule it cannot find counts as needs
// approval, never as a guess.
import { SPONSOR, type Shape } from "./access-plan";
import type { PreviewEntry } from "./api";
import { openDoc, rulesOf } from "./policy-model";

// heldSets lists, once each, the sets whose rules hold a tool in the
// preview.
export function heldSets(byTool: Record<string, PreviewEntry> | null | undefined): string[] {
  const out: string[] = [];
  for (const e of Object.values(byTool || {})) if (e.status === "approve_gated" && e.setName && !out.includes(e.setName)) out.push(e.setName);
  return out;
}

// heldKinds reads the approval setting of each held tool off its rule.
// texts holds each set's stored text, null for a set that could not be
// read. A tool whose set or rule is missing, or whose rule is no approve
// rule, is left out.
export function heldKinds(byTool: Record<string, PreviewEntry> | null | undefined, texts: Record<string, string | null | undefined>): Record<string, Shape> {
  const rules: Record<string, Record<string, Shape>> = {};
  const rulesIn = (set: string): Record<string, Shape> => {
    if (rules[set]) return rules[set];
    const byId: Record<string, Shape> = {};
    const text = texts[set];
    if (text) {
      try {
        for (const r of rulesOf(openDoc(text))) {
          if (!r.id || (r.posture !== "hold" && r.posture !== "ticket")) continue;
          const pool = r.who.roles.length ? r.who.roles.join(", ") : SPONSOR;
          byId[r.id] = { pool, how: r.posture, hold: r.timeoutSeconds, ticket: r.ticketTTLSeconds, grant: r.grantTTLSeconds };
        }
      } catch {
        // A set the console cannot parse classifies nothing.
      }
    }
    rules[set] = byId;
    return byId;
  };
  const out: Record<string, Shape> = {};
  for (const [tool, e] of Object.entries(byTool || {})) {
    if (e.status !== "approve_gated" || !e.setName || !e.ruleId) continue;
    const shape = rulesIn(e.setName)[e.ruleId];
    if (shape) out[tool] = shape;
  }
  return out;
}
