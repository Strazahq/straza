// Every sentence of the Credentials tab. The tab speaks from the person's
// side: what each server uses when
// their agent calls it, what is missing, and what one press does. The
// server page's own words for the kinds come from lib/server-words.ts, and
// the error shapes from lib/say.ts, so nothing here repeats them.

// ---- the tab itself ----

export const SUBJECT = "Your credentials";
export const READING = "Reading your credentials.";
export const SIGNED_OUT_TITLE = "Sign in to see your credentials.";
export const SIGNED_OUT_BODY = "Deciding needs no sign-in. Setting the credentials your agents use does.";
export const LEDE = "The servers your roles reach, and what each one uses when you or an agent you sponsor calls it.";

// countWords is the filter row's count: how many servers are shown and how
// many of them wait for the person.
export const countWords = (shown: number, need: number) =>
  shown + (shown === 1 ? " server, " : " servers, ") + need + (need === 1 ? " needs you" : " need you");

// ---- the chip strip ----

export const STRIP_LABEL = "Whose credentials to show";
export const CHIP_ALL = "all";
export const chipYours = (n: number) => n + " yours";
export const chipAgent = (n: number, agent: string) => n + " for " + agent;

// ---- the columns ----

export const COL_SERVER = "Server";
export const COL_FOR = "For";
export const COL_CREDENTIAL = "Credential";
export const COL_STATUS = "Status";
export const COL_DETAIL = "Detail";
export const COL_ACTIONS = "Actions";
export const CREDENTIAL_HELP = "Whose account the server uses when you or an agent calls it, in the words of the server page: one shared secret, each caller's own token, each caller's own sign-in.";
export const YOU = "you";
export const SPONSORED_LINE = "an agent you sponsor";

// kindWords is the Credential column: the server page's kind, read from the
// side of whoever the row is for.
export function kindWords(kind: string, who: string, provider: string | undefined): string {
  if (kind === "oauth") return (who ? "its own sign-in at " : "your own sign-in at ") + (provider || "the provider");
  if (kind === "token") return who ? "its own token" : "your own token";
  return "one shared secret";
}

// ---- the status words and the small line under each ----

export const NOT_SET = "Not set";
export const SET = "Set";
export const expiredWord = (day: string) => "Expired " + day;
export const NOT_SIGNED_IN = "Not signed in";
export const AGENT_SIGN_IN_UNAVAILABLE = "Agent browser sign-in unavailable";
export const SIGNED_IN = "Signed in";
export const USES_YOUR_TOKEN = "Uses your token";
export const USES_YOUR_SIGN_IN = "Uses your sign-in";
export const SHARED = "Shared";
export const SHARED_ONLY = "Shared only";
export const NOT_IN_YOUR_REACH = "Not in your reach";
export const NOT_IN_ITS_REACH = "Not in its reach";

export const SHARED_LINE = "nothing to set";
export const ONE_PROCESS_LINE = "one process, one identity";
export const LEFTOVER_LINE = "stays until you remove it";
export const WHILE_SWITCH_ON = "while your switch is on";
export const EXPIRED_LINE = "calls are refused until a new one";
export const OWN_TOKEN_LINE = "your agent calls it as you once you set one";
export const OWN_SIGN_IN_LINE = "your agent calls it as you once you sign in";
export const signedInLine = (day: string) => "since " + day + ", renews itself";
export const setByLine = (who: string, day: string) => "set by " + who + (day ? " on " + day : "");
export const agentRefusedLine = (app: string) => "its calls to " + app + " are refused";
export const agentSharedLine = (app: string) => "it falls back to the shared account of " + app;
export const agentNoBrowserLine = (agent: string) => agent + " cannot sign in through a browser";

// ---- the detail column ----

export const stopsLine = (isExpired: boolean, day: string) =>
  (isExpired ? "stopped working " : "stops working ") + (day || "never, as far as you told us");
export const grantLine = (when: string) => "this grant until " + when;
export const ownNotSet = (app: string) => "Your agent calls " + app + " as you once you paste a token you created at " + app + ".";
export const ownNotSignedIn = (app: string, provider: string) =>
  "Your agent calls " + app + " as you once you sign in at " + provider + ". Straza keeps the grant and renews it.";
export const NO_SWITCH_HERE = "This server does not let agents run on a person's sign-in, so there is no switch for your agents.";
export const agentOwnToken = (agent: string, app: string) =>
  agent + " calls " + app + " as itself with this token, and its calls are recorded as its own.";

// expiredSentence is the expired token's own line: when it stopped, who
// recorded that, and what is refused until a new one is pasted.
export const expiredSentence = (app: string, day: string, who: string) =>
  "The token stopped working on " + day + (who ? ", as it was recorded. " + who + "'s" : ", as you recorded it. Your agent's") +
  " calls to " + app + " are refused until you paste a new one.";

// leftover is the line of a row whose owner no longer reaches the server.
export const leftover = (app: string, who: string, thing: string) =>
  (who ? who + "'s roles" : "Your roles") + " no longer reach " + app + ", so nothing can use this " + thing +
  ". It stays until you remove it.";

// agentSignIn is the sentence of an agent's sign-in row: why the agent has
// no sign-in of its own, and what runs its calls instead, by the server's
// agents value. The route through an administrator is the same for every
// such row, so the table prints it once at the foot instead.
export function agentSignIn(agent: string, app: string, provider: string, agents: string | undefined, via: boolean): string {
  const head = agent + " cannot sign in at " + provider + ", because a sign-in is stored only for the person who is signed in to Straza.";
  if (via) return head + " It runs " + app + " as you while the switch on your own row is on.";
  if (agents === "sponsor") return head + " Sign in to " + app + " on your own row and turn on the switch there, and it runs as you.";
  if (agents === "shared") return head + " It falls back to the shared account of " + app + ".";
  return head;
}

// agentNoToken is the line of an agent's token row with no token of its
// own: what runs its calls meanwhile, and the way out.
export function agentNoToken(agent: string, app: string, agents: string | undefined, via: boolean): string {
  const head = agent + " has no token of its own";
  if (via) return head + " and runs " + app + " as you while the switch on your own row is on. Paste one here to give it its own.";
  if (agents === "shared") return head + " and falls back to the shared account of " + app + ". Paste one here to give it its own.";
  return head + ", so its calls to " + app + " are refused. Paste one for it here" +
    (agents === "sponsor" ? ", or turn on the switch on your own row." : ".");
}

// ---- the switch ----

export const SWITCH_LABEL = "My agents may use this";
const list = (names: string[]) => (names.length ? names.join(", ") : "An agent you sponsor");

// switchOn names the agents the switch lets call the server as the person,
// and says how those calls are recorded.
export const switchOn = (names: string[], app: string, thing: string) =>
  list(names) + (names.length > 1 ? " call " : " calls ") + app + " as you when " + (names.length > 1 ? "they have" : "it has") +
  " no " + thing + " of " + (names.length > 1 ? "their" : "its") + " own. Every such call is recorded as yours, made by the agent.";

// switchOff says what the agents need of their own while the switch is off.
export const switchOff = (names: string[], app: string, thing: string) =>
  "Off. " + list(names) + (names.length > 1 ? " need" : " needs") + " a " + thing + " of " +
  (names.length > 1 ? "their" : "its") + " own to call " + app + ".";

// ---- the row actions ----

export const PASTE = "Paste a token";
export const REPLACE = "Replace";
export const PASTE_NEW = "Paste a new token";
export const REMOVE = "Remove";
export const DISCONNECT = "Disconnect";
export const SIGN_IN_AGAIN = "Sign in again";
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
export const whoseToken = (who: string, app: string) =>
  (who ? who + " calls " : "You call ") + app + " with " + (who ? "its" : "your") + " own token.";
export const TOKEN_LABEL = "Token";
export const tokenHint = (app: string) => "The one " + app + " gave you.";
export const EXPIRY_LABEL = "Stops working on, optional";
export const expiryHint = (app: string) => "As " + app + " shows it. The row turns red on that day.";
export const SAVE_TOKEN = "Save token";
export const tokenRequired = (app: string) => "A token is required. Paste the one " + app + " gave you.";

// ---- the confirm dialogs ----

export const REMOVE_TOKEN = "Remove token";
export const REMOVE_SIGN_IN = "Remove sign-in";
export const removeTokenTitle = (app: string, who: string) =>
  who ? "Remove the token you set for " + who + "?" : "Remove your " + app + " token?";
export const removeTokenBody = (app: string, who: string) =>
  (who ? who + "'s" : "Your agent's") + " calls to " + app + " are refused until you paste another.";
export const leftoverTitle = (app: string, thing: string) => "Remove the " + app + " " + thing + "?";
export const leftoverBody = (app: string, who: string) =>
  (who ? who + "'s roles" : "Your roles") + " no longer reach " + app + ", so nothing uses it. It goes for good.";
export const disconnectTitle = (app: string) => "Disconnect from " + app + "?";
export const disconnectBody = (app: string, who: string) =>
  who
    ? who + "'s calls to " + app + " are refused until it signs in again."
    : "Your agent's calls to " + app + " are refused until you sign in again. The switch for your agents goes off with it.";

// ---- the foot of the table ----

export const HINTS_TITLE = "Sign-in and ownership";
export const oneProcessHint = (runtime: string) =>
  (runtime === "command" ? "A command" : "A container") +
  " server is one process and one identity, so each caller's own credential cannot be injected.";
export const ADMIN_ROUTE = "An agent cannot sign in at a provider, because a sign-in is stored only for the person who is signed in to Straza. An administrator can set that server's agents value to sponsor so it runs on your sign-in once you allow it.";

// noneLine counts the servers in reach that need no credential at all and
// names them, since they are never rows.
export const noneLine = (apps: string[], who: string) =>
  apps.length + (apps.length > 1 ? " more servers " : " more server ") + (who ? who + " reaches" : "you reach") +
  (apps.length > 1 ? " need" : " needs") + " no credential: " + apps.join(", ") + ".";

// ---- the empty states ----

export const emptyTitle = (who: string) => (who ? "Nothing for " + who + " yet" : "No credentials to set yet");
export const emptyBody = (who: string) =>
  (who ? who + "'s roles" : "Your roles") + " reach no MCP server, so there is nothing to set. A server appears here as soon as a role that reaches it is assigned" +
  (who ? " to " + who : " to you") + ".";

// ---- what a press answers ----

export const tokenSaved = (app: string, who: string) =>
  "Token saved. " + (who ? who + " calls " + app + " as itself." : "Your agent calls " + app + " as you.");
export const TOKEN_REMOVED = "Token removed. The row is back under Needs you.";
export const LEFTOVER_GONE = "Removed. The row is gone, because nothing reaches that server any more.";
export const DISCONNECTED = "Disconnected. The row is back under Needs you.";
export const switchedOn = (app: string) => "Switched on. Your agents call " + app + " as you when they have nothing of their own.";
export const switchedOff = (app: string, thing: string) => "Switched off. Your agents need a " + thing + " of their own for " + app + ".";
export const signedInToast = (app: string) => "Signed in. Your agent calls " + app + " as you.";

// ---- a sign-in that came back from the provider ----

export const FINISHING = "Finishing the sign-in.";
export const CAME_BACK_SIGNED_OUT = "A provider sign-in came back to this tab, but the tab is not signed in to Straza, so nothing was stored. Sign in, then press the Sign in button on the server's row again.";
export const providerSaidNo = (word: string) =>
  "The provider answered " + word + ", so nothing changed. Press the Sign in button on the server's row to try again.";

// ---- the subjects an error block names ----

export const SUBJECT_SAVE = "Save token";
export const SUBJECT_REMOVE = "Remove token";
export const SUBJECT_REMOVE_SIGN_IN = "Remove sign-in";
export const SUBJECT_DISCONNECT = "Disconnect";
export const SUBJECT_SWITCH = "My agents may use this";
export const SUBJECT_SIGN_IN = "Sign in";
