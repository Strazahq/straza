// The words of pinning a server's objects from its page: the setup card on
// Overview, the Pin dialog, and the listing credential row under the
// server's own secret. Every noun comes from the server's own marks or its
// pinned block, its type for one and its plural for many, so one server's
// page speaks of repos and another's of databases. Only the server page's
// lazy chunk imports this file.
import { type Nouns, cap } from "./object-words";

// The setup card.
export const setupLead = (app: string, w: Nouns) => app + "'s tools work on " + w.plural + ". Pin them so roles can pick " + w.plural + " and Straza can list them.";
export const pinStep = (w: Nouns) => "Pin " + w.plural + ". You see what gets pinned before anything changes.";
export const pinButton = (w: Nouns) => "Pin " + w.plural;
export const pinnedStep = (w: Nouns) => cap(w.plural) + " are pinned.";
// listingStep is step 2, which waits for step 1 because the server refuses
// a listing credential before a pin.
export const listingStep = (app: string, lister: string, waits: boolean) =>
  "Give Straza the listing credential, a second token that " + app + " accepts for " + lister + " only." + (waits ? " After step 1." : "");
export const SET_IT = "Set it";

// The Pin dialog.
export const pinTitle = (app: string, w: Nouns) => "Pin " + w.plural + " on " + app + "?";
export const pinBody = (w: Nouns) =>
  "Straza will check every call against the " + w.plural + " the caller's role covers. Nobody's access changes when you pin: roles made before keep every " + w.type + ".";
export const PIN_FACT = { type: "Object type", from: "Read from", list: "Listed by" };
export const typeFact = (w: Nouns) => w.type + " (" + w.plural + ")";
// readFrom names the argument when every marked tool reads the same one.
export const readFrom = (arg: string, n: number, total: number) => (arg ? "the " + arg + " argument" : "an argument") + ", on " + n + " of " + total + " tools";
export const listedBy = (w: Nouns) => ", which reaches every " + w.type;
export const pinFoot = (app: string) => "All of it comes from " + app + "'s own tool marks.";
export const PIN_FOOT_LABEL = "tool marks";
export const pinFootHelp = (app: string, w: Nouns) =>
  "If " + app + " later changes a mark, Straza keeps the pinned one and refuses that tool to roles on some " + w.plural + " until a person pins again.";
export const CANCEL = "Cancel";
export const PIN_PUBLISH = "Pin and publish";
export const pinnedToast = (app: string, w: Nouns) => cap(w.plural) + " are pinned on " + app + ".";

// The listing credential row.
export const LISTING_LABEL = "Listing credential";
export const listingRowHelp = (app: string, lister: string, w: Nouns) =>
  "A second token for " + app + "'s MCP endpoint, which " + app + " accepts for " + lister + " only. Straza calls " + lister + " with it in the background to keep the list of " + w.plural + ".";
export const beforePin = (app: string, w: Nouns) => "Pin " + w.plural + " first. " + app + " has nothing to list for Straza until then.";
export const LISTING_NOT_SET = "Not set.";
export const SET_ONE = "Set one";
export const GENERATE = "Generate";
export const OR_PASTE = "or paste";
export const VALUE_LABEL = "listing credential value";
export const STORE = "Store";
export const fieldFoot = (app: string, lister: string) =>
  "Put the same value in " + app + "'s config, where it accepts the token for " + lister + ". Straza stores it sealed, never sends it on an agent's call, and never shows it again.";
export const PASTE_MISSING = "Paste the value first, or press Generate to make one.";
// generatedLine sits under a generated value, which Store sends only once
// the server accepts it, since storing it starts a listing at once.
export const generatedLine = (app: string) => "Put this value in " + app + "'s config and restart " + app + ", then press Store.";
export const COPY = "Copy";
export const COPIED = "Copied the listing credential.";
export const COPY_REFUSED = "The browser refused the clipboard, so the value is selected. Copy it by hand.";
export const STORED = "Stored, sealed";
export const SET_AGAIN = "Set it again";
export const REMOVE = "Remove";
export const storedToast = (app: string, w: Nouns) => "The listing credential for " + app + " is stored. Straza is listing its " + w.plural + " now.";
// The confirm before Remove says what a failed listing does to the list
// Straza holds, as the strip on the server's tab says it.
export const removeTitle = (app: string) => "Remove the listing credential of " + app + "?";
export const removeBody = (app: string, w: Nouns) =>
  "Straza can no longer list " + app + "'s " + w.plural + " until you set one again. A failed listing changes nothing, so the list of " + w.plural + " stays as the last good listing left it.";
export const REMOVE_CONFIRM = "Remove credential";
export const removedToast = (app: string) => "The listing credential for " + app + " was removed.";
