// The reading of the Credentials tab's rows:
// which owner a row belongs to, the band it sorts into, and when an agent
// with nothing of its own calls a server as the person who sponsors it.
// rank is the one place the tab asks which band a row is in.
import type { ServerRow } from "@/lib/api";

// CredentialRow is one server for one owner: the server's row as
// GET /v1/self/servers answered it, with the owner it was read for. The
// owner is "" for the signed-in person and the agent's username for a
// sponsored agent.
export type CredentialRow = ServerRow & { who: string };

// BANDS are the four group bands in the order the table draws them; the
// index of a band is the rank that sorts into it.
export const BANDS = ["Needs you", "Set", "Nothing you can do here", "Out of reach"];

// oneProcess says the server runs as one process under one identity, so no
// caller's own credential can be injected into it.
export const oneProcess = (r: CredentialRow) => r.runtime === "command" || r.runtime === "oci";

// expired says a pasted token is past the day it was recorded to stop
// working. A grant renews itself, so only a pasted token can be past its
// expiry.
export const expired = (r: CredentialRow) => r.kind === "token" && !!r.expires_at && Date.parse(r.expires_at) < Date.now();

// rank is the band a row sorts into: 0 needs the person, 1 is set or runs
// on something that works, 2 has nothing the person can do here, 3 is out
// of the owner's reach. rows are every owner's rows, since an agent's row
// with nothing of its own runs on the person's own row for the server.
export function rank(r: CredentialRow, rows: CredentialRow[]): number {
  if (!r.reached) return 3;
  if (r.kind === "none" || r.kind === "static" || oneProcess(r)) return 1;
  if (r.connected) return expired(r) ? 0 : 1;
  if (!r.who) return 0;
  if (viaSponsor(r, ownOf(rows, r.app)) || r.agents === "shared") return 1;
  // An agent can never sign in at a provider by itself, so a sign-in it
  // lacks is not the person's to set; a token it lacks is.
  return r.kind === "oauth" ? 2 : 0;
}

// ownOf is the person's own row for the same server, which is what decides
// whether an agent's calls run on the person's credential.
export const ownOf = (rows: CredentialRow[], app: string) => rows.find((r) => !r.who && r.app === app);

// viaSponsor says an agent with nothing of its own calls the server as the
// person who sponsors it: the server allows it, the person's credential
// works, and the person's switch is on.
export function viaSponsor(r: CredentialRow, own: CredentialRow | undefined): boolean {
  return !!own && r.agents === "sponsor" && !!own.connected && !!own.allow_agents && !expired(own);
}

// keyOf names one row across every owner's list.
export const keyOf = (r: CredentialRow) => r.who + "/" + r.app;

// needing counts the rows that wait for the person to act.
export const needing = (rows: CredentialRow[]) => rows.filter((r) => rank(r, rows) === 0).length;

// thingOf is the word a sentence uses for what the row holds.
export const thingOf = (r: CredentialRow) => (r.kind === "oauth" ? "sign-in" : "token");

// sortRows puts the rows in the order the table draws them: band first, so
// the rows that wait for the person lead, then the server's name. Each row
// is ranked against all of rows, so a caller that shows one owner sorts
// every owner's rows first and narrows after.
export function sortRows(rows: CredentialRow[]): CredentialRow[] {
  return [...rows].sort((a, b) => rank(a, rows) - rank(b, rows) || a.app.localeCompare(b.app) || a.who.localeCompare(b.who));
}
