// The sentences of the Change sheets on a server's Overview: the titles,
// what publishing does, the server's check, and the result of a publish. The
// field words come from server-words, so a sheet and the card it changes
// name a fact the same way.
import type { ManifestDoc } from "./api";
import { callerKind } from "./manifest-edit";

type Which = "connection" | "credential" | "settings";

export const SHEET_TITLE: Record<Which, string> = {
  connection: "Change how Straza reaches ",
  credential: "Change server authentication of ",
  settings: "Change tools and limits of ",
};

export const SHEET_SUB: Partial<Record<Which, string>> = {
  credential: "Set secrets on the Server authentication card.",
};

export const NAME_LOCKED = "Roles, policies, secrets and the audit chain use this name. For a new name, add a server.";
export const ARGS_HINT = "Space-separated.";
export const ENV_HINT = "Plain configuration only. A secret goes on the Server authentication card, where it is encrypted.";
export const ENV_NAMELESS = "A variable has no name. Name it, or remove its row.";
export const ENV_LOCKED = " comes from the stored secret, encrypted. Change it on the Server authentication card.";
export const TO_ENV = "Name the variable the process reads the secret from.";
export const TO_HEADER = "Pick the header the secret travels in.";
export const keepAsWritten = (name: string, template: string) => "Keep as written: " + name + " with " + template;
export const PICK_NONE = "Pick at least one tool, or expose all tools.";
export const keptWords = (kept: string[]) => "Patterns kept as written: " + kept.join(", ") + ".";
export const noToolsYet = (name: string) => "Straza has no tool list for " + name + " right now, so there is nothing to pick. Recheck the server, or expose all tools.";
export const RPS_MISS = "Type a number of calls per second above zero, or leave it empty for no limit.";
export const remoteDefault = (rps: number | null) => (rps ? "the default of " + rps : "the default");
export const remoteRpsMiss = (rps: number | null) => "Type a number of calls per second above zero, or leave it empty for " + remoteDefault(rps) + ".";
export const serverWide = (seconds: number | null) => (seconds ? "the server-wide " + seconds + " s" : "the server-wide default");
export const timeoutMiss = (seconds: number | null) => "Type whole seconds above zero, or leave it empty for " + serverWide(seconds) + ".";
export const NO_MANIFEST = "The published manifest of this server could not be read, so there is nothing to change here. Recheck the server, then open this again.";
export const OFFERED_SHORT = "The server is not running, so only the tools Straza already knows show here.";
export const coveredWords = (tool: string, pattern: string) => tool + " is covered by the pattern " + pattern + ", which is kept as written.";

// The sheet reads the installed manifest from the server as it opens, so
// an edit never starts from a copy the page holds from before the last
// save. Until that read lands there are no fields, and a read that did not
// land says what failed, why, and what to do next.
export const reading = (name: string) => "Reading the published manifest of " + name + ".";
const READ_FAILED = "The published manifest could not be read, so there is nothing to change yet. ";
export const READ_UNREACHABLE = READ_FAILED + "Straza did not answer. Close this, check that it is running, then reload the page and try again.";
export const readRefused = (sentence: string) => READ_FAILED + "Straza said: " + bare(sentence) + ". Close this, reload the page, then try again.";
export const gone = (name: string) => name + " is not in the servers list any more, so there is nothing to change. Close this and reload the page.";

// The shared-secret line of the agents picker, in place of the Add
// wizard's, which speaks of storing the secret during the install.
export const SHARED_LINE: Record<string, string> = {
  token: "Only agents fall back to it.",
  oauth: "Only agents fall back to the stored secret.",
};

export const NOTHING_YET = "Nothing edited yet.";
export const NOTHING_TO_SAVE = "Change a field first.";
export const DRY_PENDING = "Checking the change with Straza.";
export const DRY_UNREACHABLE = "Straza is unreachable, so the change is not checked here. Saving will say.";
export const DRY_ACCEPTED = "Straza accepts this change.";

const bare = (s: string) => s.trim().replace(/\.$/, "");

const KEPT = "Access rows and stored secrets stay as they are.";
const readsShared = (kind: string, agents: string) => kind === "static" || (callerKind(kind) && agents === "shared");

type When = { name: string; paused: boolean; which: Which; before: ManifestDoc; after: ManifestDoc; changed: boolean };

// whenWords is "When you publish": one paragraph per thing Straza does, the
// restart and reconnect left out while the server is paused, and on the
// Connection sheet last what stays as it is. It is empty before an edit.
export function whenWords({ name, paused, which, before, after, changed }: When): string[] {
  if (!changed) return [];
  const p: string[] = [];
  if (paused) p.push(name + " stays paused. Enable starts it with this change.");
  if (which === "connection" && !paused) {
    const ka = before.straza?.runtime?.kind || "remote";
    const kb = after.straza?.runtime?.kind || "remote";
    if (ka !== kb) p.push("Straza stops the old runtime, starts the new one and reads the tools again.");
    else if (kb === "remote") p.push("Straza closes its connections to the old address, connects to the new one and reads the tools again.");
    else if (kb === "oci") p.push("Straza stops the container, starts one from the new image and reads the tools again.");
    else p.push("Straza stops the process, starts it with the new command and reads the tools again.");
    p.push("A call in flight gets an error and can be retried.");
  }
  if (which === "credential") {
    const ca = before.straza?.credential;
    const cb = after.straza?.credential;
    const ka = ca?.kind || "none";
    const kb = cb?.kind || "none";
    const aa = ca?.agents || "own";
    const ab = cb?.agents || "own";
    if (kb !== ka) {
      if (kb === "token") p.push("Each person's calls carry their own token from now on, and a person without one is refused until they paste it on the Credentials tab of their self-service page.");
      if (kb === "oauth") p.push("Each person's calls carry their own sign-in from now on, and a person who has not signed in is refused until they do.");
      if (kb === "none") p.push("Calls and health checks run without a credential from now on.");
      if (kb === "static") p.push("Every call carries the one shared secret from now on.");
    }
    if (readsShared(ka, aa) && !readsShared(kb, ab)) p.push("The stored shared secret stays, unused. Remove it on the Server authentication card.");
    if (callerKind(kb) && ab === "shared" && !(callerKind(ka) && aa === "shared")) {
      p.push("Agents with no " + (kb === "token" ? "token" : "sign-in") + " of their own run on this server's shared secret. People never fall back to it.");
    }
  }
  if (which === "settings") {
    if (!paused) p.push("Straza reconnects to the server.");
    const ta = before.straza?.exposure?.tools;
    const tb = after.straza?.exposure?.tools;
    if (JSON.stringify(ta) !== JSON.stringify(tb) && tb && !tb.includes("*")) {
      p.push("Tools you leave out leave every session's list at once. Access rows that name them stay and reach nothing until you expose them again.");
    }
  }
  if (which === "connection") p.push(KEPT);
  return p;
}

// pausedWords is the toast after a paused server's change is published:
// the server keeps it and stays paused.
export const pausedWords = (name: string) => name + " keeps the new settings and stays paused. Enable starts it with them.";
