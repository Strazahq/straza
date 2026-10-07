// The sentences of the Change sheets on a server's Overview: the titles,
// what publishing does, the server's check, and the result of a publish. The
// field words come from server-words, so a sheet and the card it changes
// name a fact the same way.
import type { ManifestDoc } from "./api";
import { callerKind } from "./manifest-edit";

type Which = "connection" | "credential" | "settings";

export const SHEET_TITLE: Record<Which, string> = {
  connection: "Change how Straza reaches ",
  credential: "Change the credential of ",
  settings: "Change the settings of ",
};

export const SHEET_SUB: Record<Which, string> = {
  connection: "Publishing starts it again under the same name, with the same access rows and secrets.",
  credential: "Secrets are not asked here. Set them on the Credential card, where they are sealed.",
  settings: "What Straza lets through to the server, and how fast.",
};

export const NAME_LOCKED = "The name cannot change: roles, policies, secrets and the audit trail refer to the server by it. For a new name, add a new server.";
export const ADDRESS_HINT = "An http or https address. Straza proxies streamable HTTP to it.";
export const ARGS_HINT = "Space-separated. The process runs on the strazad host and speaks MCP over stdio.";
export const ENV_HINT = "Plain configuration only. A secret goes on the Credential card, where it is sealed.";
export const ENV_NAMELESS = "A variable has no name. Name it, or remove its row.";
export const ENV_LOCKED = " comes from the stored secret, sealed. Change it on the Credential card.";
export const TO_ENV = "A command or container reads the shared secret from an environment variable, so name the variable.";
export const TO_HEADER = "A server over HTTP takes the shared secret in a header, so pick how it is sent.";
export const keepAsWritten = (name: string, template: string) => "Keep as written: " + name + " with " + template;
export const NO_PROVIDER = "No identity provider is configured on this server, so nobody can sign in through one yet. Add oauth.providers.<name> to the strazad config first, or use one shared secret.";
export const DESC_HINT = "One sentence people see under the server's name.";
export const PICK_NONE = "Pick at least one tool, or expose all tools.";
export const keptWords = (kept: string[]) => "Patterns kept as written: " + kept.join(", ") + ".";
export const noToolsYet = (name: string) => "Straza has no tool list for " + name + " right now, so there is nothing to pick. Recheck the server, or expose all tools.";
export const RPS_MISS = "Type a number of calls per second above zero, or leave it empty for no limit.";
export const serverWide = (seconds: number | null) => (seconds ? "the server-wide " + seconds + " s" : "the server-wide default");
export const timeoutMiss = (seconds: number | null) => "Type whole seconds above zero, or leave it empty for " + serverWide(seconds) + ".";
export const NO_MANIFEST = "The installed manifest of this server could not be read, so there is nothing to change here. Recheck the server, then open this again.";
export const OFFERED_SHORT = "The server is not running, so only the tools Straza already knows show here.";
export const coveredWords = (tool: string, pattern: string) => tool + " is covered by the pattern " + pattern + ", which is kept as written.";

// The sheet reads the installed manifest from the server as it opens, so
// an edit never starts from a copy the page holds from before the last
// save. Until that read lands there are no fields, and a read that did not
// land says what failed, why, and what to do next.
export const reading = (name: string) => "Straza is reading the installed manifest of " + name + ", so the fields are not ready yet.";
const READ_FAILED = "The installed manifest could not be read, so there is nothing to change yet. ";
export const READ_UNREACHABLE = READ_FAILED + "strazad did not answer. Close this, check that it is running, then reload the page and try again.";
export const readRefused = (sentence: string) => READ_FAILED + "The server said: " + bare(sentence) + ". Close this, reload the page, then try again.";
export const gone = (name: string) => name + " is not in the servers list any more, so there is nothing to change. Close this and reload the page.";

// The shared-secret line of the agents picker, in place of the Add
// wizard's, which speaks of storing the secret during the install.
export const SHARED_LINE: Record<string, string> = {
  token: "Only agents fall back to it.",
  oauth: "Only agents fall back to the stored secret.",
};

export const NOTHING_YET = "Nothing yet. Edit a field above.";
export const NOTHING_TO_SAVE = "Nothing to save yet: change a field first.";
export const DRY_PENDING = "Checking the change with the server.";
export const DRY_UNREACHABLE = "strazad is unreachable, so the change is not checked here. Saving will say.";
export const dryAccepted = (runtime: string, credential: string) => "The server accepts this change: " + runtime + " runtime, credential " + credential + ".";
export const DRY_AS_YOU_TYPE = " Checked with the server as you type.";

const bare = (s: string) => s.trim().replace(/\.$/, "");

const NOTHING_CHANGES = "Nothing changes yet. Edit a field above and this says what Straza will do.";
const KEPT = "Access rows and stored secrets stay as they are.";
const readsShared = (kind: string, agents: string) => kind === "static" || (callerKind(kind) && agents === "shared");

type When = { name: string; paused: boolean; which: Which; before: ManifestDoc; after: ManifestDoc; changed: boolean };

// whenWords is "When you publish": one paragraph per thing Straza does, the
// restart and reconnect left out while the server is paused, and always
// last what stays as it is.
export function whenWords({ name, paused, which, before, after, changed }: When): string[] {
  if (!changed) return [NOTHING_CHANGES];
  const p: string[] = [];
  if (paused) p.push(name + " stays paused, so nothing starts and every call is still refused. Enable starts it with the new settings.");
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
    if (readsShared(ka, aa) && !readsShared(kb, ab)) p.push("A stored shared secret stays stored, and nothing reads it. The Credential card marks it, so you can remove it.");
    if (callerKind(kb) && ab === "shared" && !(callerKind(ka) && aa === "shared")) {
      p.push("Agents with no " + (kb === "token" ? "token" : "sign-in") + " of their own run on this server's shared secret. People never fall back to it.");
    }
  }
  if (which === "settings") {
    if (!paused) p.push("Straza reconnects to apply it.");
    const ta = before.straza?.exposure?.tools;
    const tb = after.straza?.exposure?.tools;
    if (JSON.stringify(ta) !== JSON.stringify(tb) && tb && !tb.includes("*")) {
      p.push("Tools you leave out leave every session's list at once. Access rows that name them stay and reach nothing until you expose them again.");
    }
    const rb = after.straza?.limits?.rps;
    if (rb && rb !== before.straza?.limits?.rps) p.push("The limit counts each session's calls to this server separately.");
  }
  p.push(KEPT);
  return p;
}

// pausedWords is the toast after a paused server's change is published:
// the server keeps it and stays paused.
export const pausedWords = (name: string) => name + " keeps the new settings and stays paused. Enable starts it with them.";
