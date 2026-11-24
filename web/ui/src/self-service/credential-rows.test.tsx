import { describe, expect, it } from "vitest";
import { BANDS, type CredentialRow, needing, rank, sortRows } from "./credential-rows";

// The bands of the Credentials tab: every kind of row and the band it sorts
// into, the count of rows that wait for the person, and the table's order.

const AGENT = "altair";
const PAST = new Date(Date.now() - 9 * 86400000).toISOString();
const FUTURE = new Date(Date.now() + 9 * 86400000).toISOString();

const row = (over: Partial<CredentialRow>): CredentialRow => ({
  app: "github", runtime: "remote", kind: "token", reached: true, connected: false, who: "", ...over,
});
const own = (over: Partial<CredentialRow>) => row({ agents: "sponsor", ...over });
const agent = (over: Partial<CredentialRow>) => row({ who: AGENT, ...over });

// lending is the person's own row whose credential an agent may run on.
const lending = (kind: string) => own({ kind, connected: true, allow_agents: true });

type Case = { name: string; r: CredentialRow; rows?: CredentialRow[]; band: number };

const CASES: Case[] = [
  { name: "the person's own token, no longer reached", r: own({ reached: false, connected: true }), band: 3 },
  { name: "an agent's sign-in, no longer reached", r: agent({ kind: "oauth", reached: false, connected: true }), band: 3 },
  { name: "a shared secret", r: row({ kind: "static" }), band: 1 },
  { name: "a server with no credential kind", r: row({ kind: "none" }), band: 1 },
  { name: "a command server", r: row({ kind: "static", runtime: "command" }), band: 1 },
  { name: "a container server", r: row({ kind: "static", runtime: "oci" }), band: 1 },
  { name: "an agent's row on a command server", r: agent({ kind: "static", runtime: "command" }), band: 1 },
  { name: "the person's own token, working", r: own({ connected: true }), band: 1 },
  { name: "the person's own token, working until a later day", r: own({ connected: true, expires_at: FUTURE }), band: 1 },
  { name: "the person's own sign-in, working", r: own({ kind: "oauth", connected: true }), band: 1 },
  { name: "the person's own token, not set", r: own({}), band: 0 },
  { name: "the person's own sign-in, not signed in", r: own({ kind: "oauth" }), band: 0 },
  { name: "the person's own token, expired", r: own({ connected: true, expires_at: PAST }), band: 0 },
  { name: "an agent's own token, set", r: agent({ agents: "own", connected: true }), band: 1 },
  { name: "an agent's own token, set until a later day", r: agent({ agents: "own", connected: true, expires_at: FUTURE }), band: 1 },
  { name: "an agent's own token, expired", r: agent({ agents: "own", connected: true, expires_at: PAST }), band: 0 },
  { name: "an agent's token, running on the person's", r: agent({ agents: "sponsor" }), rows: [lending("token")], band: 1 },
  { name: "an agent's sign-in, running on the person's", r: agent({ kind: "oauth", agents: "sponsor" }), rows: [lending("oauth")], band: 1 },
  { name: "an agent's token, on a shared account", r: agent({ agents: "shared" }), band: 1 },
  { name: "an agent's sign-in, on a shared account", r: agent({ kind: "oauth", agents: "shared" }), band: 1 },
  { name: "an agent's token, not set, each caller its own", r: agent({ agents: "own" }), band: 0 },
  { name: "an agent's token, not set, the person's switch off", r: agent({ agents: "sponsor" }), rows: [own({ connected: true, allow_agents: false })], band: 0 },
  { name: "an agent's token, not set, the person's token expired", r: agent({ agents: "sponsor" }), rows: [own({ connected: true, allow_agents: true, expires_at: PAST })], band: 0 },
  { name: "an agent's token, not set, the person has none", r: agent({ agents: "sponsor" }), rows: [own({ allow_agents: true })], band: 0 },
  { name: "an agent's token, not set, no row of the person's", r: agent({ agents: "sponsor" }), rows: [], band: 0 },
  { name: "an agent's token, not set, the person's switch on another server", r: agent({ agents: "sponsor" }), rows: [{ ...lending("token"), app: "gitlab" }], band: 0 },
  { name: "an agent's sign-in, not connected, each caller its own", r: agent({ kind: "oauth", agents: "own" }), band: 2 },
  { name: "an agent's sign-in, not connected, the person's switch off", r: agent({ kind: "oauth", agents: "sponsor" }), rows: [own({ kind: "oauth", connected: true, allow_agents: false })], band: 2 },
  { name: "an agent's sign-in, not connected, the person not signed in", r: agent({ kind: "oauth", agents: "sponsor" }), rows: [own({ kind: "oauth", allow_agents: true })], band: 2 },
];

describe("the bands of the Credentials tab", () => {
  it("names the four bands in the order the table draws them", () => {
    expect(BANDS).toEqual(["Needs you", "Set", "Nothing you can do here", "Out of reach"]);
  });

  it.each(CASES)("puts $name in band $band", ({ r, rows, band }) => {
    expect(rank(r, [...(rows || []), r])).toBe(band);
  });

  it("counts the rows that wait for the person, the agents' included", () => {
    const rows = (lends: boolean) => [
      own({}),
      own({ app: "jira", connected: true, expires_at: PAST }),
      own({ app: "slack", connected: true, allow_agents: lends }),
      agent({ agents: "own" }),
      agent({ app: "slack", agents: "sponsor" }),
      agent({ app: "midpoint", kind: "oauth", agents: "own" }),
      row({ app: "sentry", kind: "static" }),
    ];
    // The person's github and expired jira wait, and so do the agent's
    // github and, while the person's slack token does not lend, its slack.
    expect(needing(rows(false))).toBe(4);
    expect(needing(rows(true))).toBe(3);
    expect(needing([])).toBe(0);
  });

  it("sorts by band, then by server, then by owner, ranking an agent's row against the person's", () => {
    const rows = [
      own({ app: "gitlab", reached: false, connected: true }),
      agent({ app: "midpoint", kind: "oauth", agents: "own" }),
      row({ app: "sentry", kind: "static" }),
      own({ app: "slack", connected: true, allow_agents: true }),
      agent({ app: "slack", agents: "sponsor" }),
      agent({ app: "github", agents: "own" }),
      own({ app: "github" }),
      row({ app: "filesystem", kind: "static", runtime: "command" }),
    ];
    expect(sortRows(rows).map((r) => (r.who ? r.app + " for " + r.who : r.app))).toEqual([
      "github", "github for " + AGENT,
      "filesystem", "sentry", "slack", "slack for " + AGENT,
      "midpoint for " + AGENT,
      "gitlab",
    ]);
  });
});
