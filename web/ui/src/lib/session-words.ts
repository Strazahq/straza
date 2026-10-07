// The sentences of the Sessions screen and the session sheet.
import type { SessionRow } from "./api";

// Attestation vocabulary, the wire's real value set: managed, advisory,
// none. One sentence each, on the badge's tooltip and in the sheet.
export const ATT_TITLE: Record<string, string> = {
  managed: "Managed install: every artifact the registry lists for this harness and platform reported a hash from its allowed set.",
  advisory: "User-mode install: hashes are recorded but not verified against the registry, or nothing relevant is registered to check them against.",
  none: "Nothing measured, or a managed claim failed verification, which is tamper evidence. governance.minAttestation sets the floor a check-in must reach.",
};
export const ATT_LEGEND = "Attestation: managed means the install hashes were verified against the registry, advisory means hashes were reported but not verifiable, none means nothing was measured or a managed claim failed.";
export const attTone = (a: string | undefined): "ok" | "warn" | "danger" => (a === "managed" ? "ok" : a === "advisory" ? "warn" : "danger");
// ATT_PHRASE is the short reading beside the attestation badge on the
// sheet, where the full sentence is too long to sit in a facts grid.
export const ATT_PHRASE: Record<string, string> = {
  managed: "hashes verified against the registry",
  advisory: "hashes reported, not verifiable",
  none: "nothing measured, or a managed claim failed",
};

// Wiring status, server-classified: which harness-config render a
// session's managed wiring hash matches.
export const WIRING_TITLE: Record<string, string> = {
  current: "The managed hook configuration matches the version this server publishes.",
  allowed: "An approved hook configuration from an earlier version or a custom installation. Re-run install --managed to use the current configuration. Retiring the hash can prevent managed check-in.",
  mismatch: "This hash matches no registered row: tampered or never registered. With require-managed active such a session is refused at start.",
  unmeasured: "No managed measurement: a user-mode install.",
};
export const wiringTone = (w: string | undefined): "ok" | "warn" | "danger" | "plain" => (w === "current" ? "ok" : w === "allowed" ? "warn" : w === "mismatch" ? "danger" : "plain");
export const shortHash = (h: string | undefined) => (h ? h.replace(/^sha256:/, "").slice(0, 12) + "…" : "");
// wiringPhrase is the first sentence of the wiring status, the reading the
// sheet sets beside the badge. The rest of WIRING_TITLE stays on the
// tooltip, where a longer explanation belongs.
export function wiringPhrase(w: string | undefined): string {
  const title = WIRING_TITLE[w || ""] || "";
  const end = title.indexOf(". ");
  return end < 0 ? title : title.slice(0, end + 1);
}

export const statusTone = (s: string): "ok" | "danger" | "plain" => (s === "active" ? "ok" : s === "revoked" ? "danger" : "plain");
export const shortID = (id: string) => id.slice(0, 13) + "…";
export const harnessWords = (s: Pick<SessionRow, "harness" | "client_version">) => s.harness + (s.client_version ? ", straza " + s.client_version : "");

// The kill switch.
export const revokeBody = (n: number, name: string) =>
  "Every agent action in " + (n === 1 ? "this session" : "all " + n + " selected sessions") + (name ? " of " + name : "") +
  " is denied within seconds. A session revoke is a stand-down, not a ban: the same device can check in again as a new session. To cut access off entirely, revoke the device or lock the user on their Users sheet.";
export const OWN_SESSION_WARNING = "This includes the console's own session, so you will be signed out.";
export const revokeLabel = (n: number) => (n === 1 ? "Revoke session" : "Revoke " + n + " sessions");
export const selectedWords = (n: number, shown: number, name: string) => n + " selected of " + shown + " shown" + (name ? ", all " + name : "");
export const revokedWords = (n: number) => n + (n === 1 ? " session revoked." : " sessions revoked.");

// The headline above the filter row: the count of active sessions on the
// server at stat size, with these words beside it.
export const activeWords = (n: number) => (n === 1 ? "active session" : "active sessions");

// The push health line beside the headline.
export function pushLine(push: { lane_up?: boolean; connected?: number } | undefined, active: number): string {
  if (!push) return "";
  if (!push.lane_up) return "Live updates are unavailable on this server instance. Clients check for changes every 30 seconds.";
  const c = push.connected || 0;
  return "Live updates: " + c + " connected to this server instance." + (c < active ? " Other clients check for changes every 30 seconds." : "");
}

export const SENTINEL_LINE = "The sentinel is alert-only; revoking is your call.";

// The group header of one person's sessions: grouped by user, so a fleet
// reads as people.
export const ATT_RANK: Record<string, number> = { managed: 3, advisory: 2, none: 1 };
export const worstAttestation = (levels: string[]) => levels.reduce((w, a) => ((ATT_RANK[a] || 0) < (ATT_RANK[w] || 9) ? a : w), "managed");
export const groupWords = (n: number) => n + (n === 1 ? " session" : " sessions");
export const groupActive = (active: number) => (active ? active + " active" : "none active");
export const showGroup = (name: string) => "Show the sessions of " + name;
export const hideGroup = (name: string) => "Hide the sessions of " + name;
export const countWords = (loaded: number, more: boolean) => loaded + (loaded === 1 ? " session" : " sessions") + " loaded" + (more ? ", more on the server" : "");
export const EMPTY_ACTIVE = "No agent is checked in right now. A session appears when an agent's harness checks in.";
export const EMPTY_FILTERED = "No session matches these filters.";
export const READING = "Reading the session list.";

// The two doors of the sheet's Record section, one line each.
export const RECORD_TRANSCRIPT = "Recorded transcript";
export const RECORD_AUDIT = "Every decision and change about this session on the chain";
