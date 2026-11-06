// The words of an MCP server's tab of listed things. Every noun comes from
// the server's pinned block, its type for one and its plural for many, so
// one server's tab speaks of repos and another's of projects. The module is
// loaded with the tab alone, never with the entry page.
import type { ObjectListing } from "./api";
import { relTimeText } from "./words";

// Nouns are the two words a pinned block gives.
export type Nouns = { type: string; plural: string };

// nounsOf reads the type and the plural, the plural falling back to the
// type plus s as the manifest's own rule does.
export const nounsOf = (b: { type: string; plural?: string }): Nouns => ({ type: b.type, plural: b.plural || b.type + "s" });

export const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

// counted is a number with the noun it counts.
export const counted = (n: number, w: Nouns) => n.toLocaleString("en-US") + " " + (n === 1 ? w.type : w.plural);

// missingCredential says a server that takes a credential has no listing
// credential, naming the card on Overview before the strazactl command.
export const missingCredential = (app: string, w: Nouns) =>
  app + " has no listing credential, so Straza cannot list its " + w.plural + ". Set one under Server authentication on Overview, or with strazactl apps secret set " + app + " --listing.";

export type Strip = { tone: "plain" | "ok" | "danger"; lines: string[] };

// lastListing says when the list now held was read and what its version
// changed. joined and left count the last change, which is the change the
// new badges mark, and the first version changes nothing it could name.
function lastListing(l: ObjectListing, w: Nouns): string {
  const head = "Last listing " + relTimeText(l.listedAt) + ": " + counted(l.count, w);
  if (l.version <= 1) return head + ".";
  const moved = [l.joined ? l.joined + " new" : "", l.left ? l.left + " left the list" : ""].filter(Boolean);
  return head + ", " + (moved.length ? moved.join(", ") : "no change") + ".";
}

// stripOf words the status strip of the tab: how the server is listed,
// then the last listing, or the reason it failed verbatim with what the
// table still shows, or the missing listing credential. A listing that
// never ran yet says when it will.
export function stripOf(app: string, w: Nouns, l: ObjectListing): Strip {
  const lead = app + " lists its " + w.plural + " with " + l.tool + ".";
  const missing = l.credential === "missing" ? missingCredential(app, w) : "";
  const failed = l.outcome === "failed";
  if (!missing && !failed) {
    if (l.outcome !== "ok") return { tone: "plain", lines: [lead, "No listing yet. Straza runs the first one within 30 seconds. Reload the page to see it."] };
    return { tone: l.version > 1 && (l.joined || l.left) ? "ok" : "plain", lines: [lead, lastListing(l, w)] };
  }
  const lines = [lead];
  if (missing) lines.push(missing);
  // A listing without its credential fails on that alone, so the reason
  // it stored says what the missing line says.
  if (failed && l.reason && !missing) lines.push(l.reason);
  if (l.listedAt) lines.push("The list below is from the last good listing, " + relTimeText(l.listedAt) + ".");
  else if (failed) lines.push("No listing has worked yet, so there are no " + w.plural + " to show.");
  return { tone: "danger", lines };
}

// LIST_AGAIN asks for a listing now, on a strip whose listing failed with
// a credential set. listingAgain is the toast once strazad takes it.
export const LIST_AGAIN = "List again";
export const listingAgain = (app: string, w: Nouns) => "Straza is listing " + app + "'s " + w.plural + " again. Reload the page in a minute to see the list.";

// listingHelp is the help icon beside the strip. A server that needs no
// listing credential is listed with none, so the sentence names none.
export const listingHelp = (app: string, tool: string, credential: string) =>
  "Straza calls " + tool + " every 10 minutes" + (credential === "not-needed" ? "" : " with " + app + "'s listing credential") + ". A failed listing changes nothing, and every listing is in the audit log.";

// setByHelp says who may set an attribute in the source, from its setBy:
// org when only the organization's admins may, else the thing's own admins
// too. The sentences avoid an article before the type, which a type that
// starts with a vowel would get wrong.
export const setByHelp = (setBy: string | undefined, w: Nouns) => setBy === "org"
  ? "Only the organization's admins can set it, so the admins of one " + w.type + " cannot move it into a role."
  : "The " + w.type + "'s own admins can set it too.";

// idLabel and idHelp are the help icon on each row that carries the id,
// which no longer takes a column of its own.
export const idLabel = (value: string) => "Id of " + value;
export const idHelp = (w: Nouns, id: string) => "The id the server gives this " + w.type + " is " + id + ". If it is deleted and made again under the same name, it gets a new id, so no access carries over.";
export const coveredHelp = (app: string, w: Nouns) => "The roles whose tools on " + app + " reach this " + w.type + " today.";
export const draftsHelp = (w: Nouns) => "Open drafts you may read that would add a role to this " + w.type + " or drop one from it. Nothing changes until a person publishes the draft.";

export const COLUMN = { covered: "Covered by", drafts: "In open drafts" };

// draftLink names one open draft in the In open drafts column. The change
// it makes, adds or drops, follows as the server sends it.
export const draftLink = (draft: string) => "Draft " + draft;
export const LISTING = "Listing";
export const NO_ROLE = "no role";
export const NO_AGENT = "No agent reaches it.";
export const NEW = "new";
export const NOT_SET = "not set";

export const filterLabel = (w: Nouns) => "Filter " + w.plural;
export const noMatch = (w: Nouns, q: string) => "No " + w.type + ' matches "' + q + '".';
export const emptyList = (w: Nouns) => "The list holds no " + w.plural + ".";
export const reading = (w: Nouns) => "Reading the " + w.plural + " from strazad.";

// drift is the warning when the marks the server sends differ from what
// its manifest pins, with the command that drafts the change.
export const drift = (app: string, w: Nouns) => app + "'s marks differ from the pinned " + w.plural + ". Draft the change with strazactl apps objects draft " + app + ".";
export const declaresTitle = (app: string) => "What " + app + " declares in its tools/list answer";
export const noMarks = (app: string, w: Nouns) => "None of " + app + "'s tools carries a Straza mark, so only its manifest says what its " + w.plural + " are.";
