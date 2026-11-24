// Every sentence of the Credentials tab. The tab speaks from the person's
// side: what each server uses when
// their agent calls it, what is missing, and what one press does. The
// sentence under an error's heading comes from words.ts, so nothing here
// repeats it.

// ---- the tab itself ----

export const SUBJECT = "Your credentials";
// readProblem words a failed read for a person: why, and what to do next.
export const readProblem = (why: string) => "Your credentials could not be read: " + why + ". Try again in a moment. If this keeps happening, tell your administrator.";
export const lastRead = (at: string) => "Last read at " + at + ".";
export const READING = "Reading your credentials.";
export const SIGNED_OUT_TITLE = "Sign in to see your credentials.";

// ---- the filter above the table ----

export const STRIP_LABEL = "Whose credentials to show";
export const CHIP_ALL = "All";
export const CHIP_YOURS = "Yours";
export const chipAgent = (agent: string) => "For " + agent;

// ---- the columns ----

export const COL_SERVER = "Server";
export const COL_FOR = "For";
export const COL_USES = "Uses";
export const COL_STATUS = "Status";
export const COL_ACTIONS = "Actions";
export const YOU = "you";

// usesWords is the Uses column: what the server takes, read from the side
// of whoever the row is for. lent says an agent runs on the person's.
export function usesWords(kind: string, who: string, provider: string | undefined, lent: boolean): string {
  const thing = kind === "oauth" ? "sign-in" : "token";
  if (kind !== "oauth" && kind !== "token") return "One shared secret";
  if (lent) return "Your " + thing;
  // An agent never signs in by itself, so its sign-in is not its own.
  if (who && kind === "oauth") return "A sign-in at " + (provider || "the provider");
  return (who ? "Its own " : "Your own ") + thing + (kind === "oauth" ? " at " + (provider || "the provider") : "");
}

// ---- the status words, and the few muted words after some of them ----

export const NOT_SET = "Not set";
export const SET = "Set";
export const EXPIRED = "Expired";
export const NOT_SIGNED_IN = "Not signed in";
export const SIGNED_IN = "Signed in";
export const USES_YOURS = "Uses yours";
export const CANNOT_SIGN_IN = "Cannot sign in";
export const SHARED_ACCOUNT = "Shared account";
export const SHARED = "Shared";
export const SHARED_ONLY = "Shared only";
export const OUT_OF_REACH = "Out of reach";

export const SHARED_LINE = "nothing to set";
export const RENEWS_LINE = "renews itself";
export const sinceLine = (day: string) => "since " + day;
export const untilLine = (day: string) => "until " + day;

// ---- the sentence behind a row's help icon ----

export const whyName = (row: string) => "About " + row;
export const ownNotSet = (app: string) => "Your agents call " + app + " as you once you paste a token you created at " + app + ".";
export const ownNotSignedIn = (app: string, provider: string) => "Your agents call " + app + " as you once you sign in at " + provider + ". Straza keeps the sign-in and renews it.";
export const expiredSentence = (app: string, day: string, who: string) =>
  "The token stopped working on " + day + ". " + (who ? who + "'s" : "Your agents'") + " calls to " + app + " are refused until you paste a new one.";
export const setSentence = (who: string, day: string, setter: string) =>
  (who ? who + " calls the server as itself with this token." : "Your agents call the server as you with this token.") + " Set by " + setter + (day ? " on " + day : "") + ".";
export const lentSentence = (who: string, app: string) => who + " has nothing of its own for " + app + " and calls it as you while the switch on your own row is on. Every such call is recorded as yours, made by the agent.";
export const sharedAccountSentence = (who: string, app: string) => who + " has nothing of its own for " + app + " and calls it on the shared account of " + app + ".";
export const agentNotSet = (who: string, app: string, sponsor: boolean) =>
  who + " has no token of its own, so its calls to " + app + " are refused. Paste one for it here" + (sponsor ? ", or turn on the switch on your own row." : ".");
export const cannotSignIn = (who: string, app: string, sponsor: boolean) =>
  who + " cannot sign in at a provider by itself, so its calls to " + app + " are refused." + (sponsor ? " Sign in on your own row and turn on the switch there, and it calls " + app + " as you." : "");
export const leftover = (app: string, who: string, thing: string) =>
  (who ? who + "'s roles" : "Your roles") + " no longer reach " + app + ", so nothing uses this " + thing + ".";

// ---- the switch on the person's own row ----

export const SWITCH_LABEL = "My agents may use this";
const names = (xs: string[]) => (xs.length <= 1 ? xs.join("") : xs.slice(0, -1).join(", ") + " and " + xs[xs.length - 1]);
export const switchOn = (agents: string[], app: string, thing: string) =>
  (agents.length ? names(agents) : "An agent you sponsor") + (agents.length > 1 ? " call " : " calls ") + app + " as you when " + (agents.length > 1 ? "they have" : "it has") +
  " no " + thing + " of " + (agents.length > 1 ? "their" : "its") + " own. Every such call is recorded as yours, made by the agent.";
export const switchOff = (agents: string[], app: string, thing: string) =>
  (agents.length ? names(agents) : "An agent you sponsor") + (agents.length > 1 ? " need" : " needs") + " a " + thing + " of " + (agents.length > 1 ? "their" : "its") + " own to call " + app + ".";

// ---- the row actions ----

export const PASTE = "Paste a token";
export const REPLACE = "Replace";
export const PASTE_NEW = "Paste a new token";
export const REMOVE = "Remove";
export const DISCONNECT = "Disconnect";
export const CANCEL = "Cancel";
export const signInWith = (provider: string) => "Sign in with " + provider;

// actionName is a row action's accessible name. The button says the verb
// alone, because the row it sits in already says which server and whose it
// is; a screen reader has no row in view, so the name carries both.
export const actionName = (label: string, row: string) => label + ", " + row;

// ---- the paste sheet ----

export const pasteTitle = (app: string, set: boolean, who: string) =>
  (set ? "Replace the token from " : "Paste a token from ") + app + (who ? " for " + who : "");
export const sealedLine = (app: string) =>
  "Straza tests it against " + app + " once, then keeps only a sealed copy. Nothing shows it again.";
export const TOKEN_LABEL = "Token";
export const tokenHint = (app: string) => "The one " + app + " gave you.";
export const EXPIRY_LABEL = "Stops working on, optional";
export const expiryHint = (app: string) => "As " + app + " shows it.";
export const SAVE_TOKEN = "Save token";
export const tokenRequired = (app: string) => "A token is required. Paste the one " + app + " gave you.";

// ---- the confirm dialogs ----

export const REMOVE_TOKEN = "Remove token";
export const REMOVE_SIGN_IN = "Remove sign-in";
export const removeTokenTitle = (app: string, who: string) =>
  who ? "Remove " + who + "'s " + app + " token?" : "Remove your " + app + " token?";
// removeTokenBody says what removing a token does. An agent whose token goes
// falls back to the shared account or to the person's own credential where
// the server allows that, and is refused otherwise.
export function removeTokenBody(app: string, who: string, agents?: string): string {
  if (!who) return "Your agents' calls to " + app + " are refused until you paste another.";
  if (agents === "shared") return who + " then calls " + app + " on the shared account.";
  if (agents === "sponsor") return who + " then calls " + app + " as you while the switch on your own row is on. With the switch off its calls are refused.";
  return who + "'s calls to " + app + " are refused until you paste another.";
}
export const leftoverTitle = (app: string, thing: string) => "Remove the " + app + " " + thing + "?";
export const leftoverBody = (app: string, who: string) =>
  (who ? who + "'s roles" : "Your roles") + " no longer reach " + app + ", so nothing uses it.";
export const disconnectTitle = (app: string) => "Disconnect from " + app + "?";
export const disconnectBody = (app: string) =>
  "Your agents' calls to " + app + " are refused until you sign in again. The switch for your agents goes off with it.";

// ---- the foot of the table ----

// noneLine counts the servers in reach that need no credential at all and
// names them, since they are never rows. noneOnly names them when no row
// stands above it.
export const noneLine = (apps: string[], who: string) =>
  apps.length + (apps.length > 1 ? " more servers " : " more server ") + (who ? who + " reaches" : "you reach") +
  (apps.length > 1 ? " need" : " needs") + " no credential: " + apps.join(", ") + ".";
export const noneOnly = (apps: string[]) =>
  (apps.length > 1 ? "Your servers need" : "Your server needs") + " no credential: " + apps.join(", ") + ".";

// ---- the empty states ----

export const emptyTitle = (who: string) => (who ? "Nothing for " + who + " yet" : "No credentials to set yet");
export const EMPTY_BODY = "None of your roles gives you a server.";
export const EMPTY_FILTER = "No row for this choice.";

// ---- what a press answers ----

export const tokenSaved = (app: string, who: string) =>
  "Token saved. " + (who ? who + " calls " + app + " as itself." : "Your agents call " + app + " as you.");
export const TOKEN_REMOVED = "Token removed.";
export const LEFTOVER_GONE = "Removed.";
export const DISCONNECTED = "Disconnected.";
export const switchedOn = (app: string) => "Switched on. Your agents call " + app + " as you when they have nothing of their own.";
export const switchedOff = (app: string, thing: string) => "Switched off. Your agents need a " + thing + " of their own for " + app + ".";
export const signedInToast = (app: string) => "Signed in. Your agents call " + app + " as you.";

// ---- a sign-in that came back from the provider ----

export const FINISHING = "Finishing the sign-in.";
export const CAME_BACK_SIGNED_OUT = "The sign-in at the provider finished, but this tab is no longer signed in to Straza, so nothing was stored. Sign in here, then press the server's Sign in button again.";
export const providerSaidNo = (word: string) =>
  "The provider answered " + word + ". Press the server's Sign in button to try again.";

// ---- the headings of an error block, each what did not happen ----

export const NOT_SAVED = "Token not saved.";
export const TOKEN_NOT_REMOVED = "Token not removed.";
export const SIGN_IN_NOT_REMOVED = "Sign-in not removed.";
export const NOT_DISCONNECTED = "Not disconnected.";
export const SWITCH_NOT_CHANGED = "Switch not changed.";
export const SIGN_IN_NOT_STARTED = "Sign-in did not start.";
export const SIGN_IN_NOT_FINISHED = "Sign-in did not finish.";
