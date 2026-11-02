// The words of a role's covers line on a server that pins an object type:
// the access editor's On which question, the role page's column, the
// review, the draft views and the role doors' refusals. Every noun comes
// from the server's pinned block, so one server's words speak of repos and
// another's of projects. The module loads with the role screens, never with
// the entry page.
import type { Covers } from "./access-covers";
import type { ObjectsBlock } from "./api";
import { type Nouns, counted } from "./object-words";
import { list } from "./policy-words";

// article is the indefinite article before a type, from its first letter.
const article = (type: string) => (/^[aeio]/i.test(type) ? "An " : "A ");

// SHOWN_VALUES is how many values a line names before it counts the rest.
export const SHOWN_VALUES = 3;

// valuesWords names up to three values in order and counts the rest.
function valuesWords(values: string[]): string {
  const sorted = values.slice().sort();
  if (sorted.length <= SHOWN_VALUES) return list(sorted);
  return sorted.slice(0, SHOWN_VALUES).join(", ") + " and " + (sorted.length - SHOWN_VALUES) + " more";
}

export const onWhich = (w: Nouns) => "On which " + w.plural;
export const COVERS_HEAD = "Covers";
export const onWhichHelp = (app: string, w: Nouns) =>
  "A call that names one " + w.type + " is checked against the " + w.plural + " this role covers. " + app + "'s manifest says which tools name one, and which argument carries it.";

// onWhichLede counts the tools whose calls name one object.
export function onWhichLede(app: string, w: Nouns, named: number): string {
  if (!named) return "Pick which " + w.plural + " this role reaches with " + app + "'s tools.";
  return app + " says " + named + " of its tools " + (named === 1 ? "works" : "work") + " on one " + w.type + ". Pick which " + w.plural + " this role reaches with them.";
}

export const everyCard = (w: Nouns) => "Every " + w.type;
export const theseCard = (w: Nouns) => "These " + w.plural;

// everyLine is the Every card's line, with today's count when the list was
// read.
export const everyLine = (app: string, w: Nouns, total: number | null) =>
  total === null ? "Every " + w.type + " " + app + " lists, today and later." : "All " + total + " today, and every " + w.type + " " + app + " lists later.";
export const theseLine = (app: string, w: Nouns) => "Pick from " + app + "'s list. " + article(w.type) + w.type + " added later stays out until you add it.";

// deadTool is the row of a tool a role on some objects cannot hold: its
// calls reach every object, or name none and rely on a login Straza does
// not check yet.
export const deadTool = (tool: string, w: Nouns, credential: boolean) =>
  credential
    ? tool + " cannot be in a role on some " + w.plural + ": its calls do not name " + article(w.type).toLowerCase() + w.type + ", and Straza does not check a role's own login yet. Make a role on every " + w.type + " for it."
    : tool + " cannot be in a role on some " + w.plural + ": nothing keeps it to them. Make a role on every " + w.type + " for it.";
export const laterDead = (app: string, w: Nouns) =>
  "A tool " + app + " adds later cannot be in a role on some " + w.plural + ": nothing keeps it to them. Make a role on every " + w.type + " for it.";
export const unticked = (tools: string[], w: Nouns) =>
  list(tools) + (tools.length === 1 ? " was unticked: it cannot" : " were unticked: they cannot") + " be in a role on some " + w.plural + ".";
export const missingCovers = (w: Nouns) => "Say which " + w.plural + " this role covers.";

// everyHeld is R4 on the Every card: this deployment keeps covers every on
// a server's own role to a global admin.
export const everyHeld = (role: string, app: string, w: Nouns) =>
  "On this deployment only a global admin may make " + role + " cover every " + w.type + ", including the " + w.plural + " " + app + " lists later. Name the " + w.plural + " it covers, or ask a global admin."

// unknownKey refuses a save over an access row that carries a key the
// console does not edit, which a save would drop.
export const unknownKey = (role: string, key: string) =>
  role + "'s access row carries " + key + ", which this console cannot edit. Edit the role as a file: strazactl roles export " + role + ", then strazactl drafts create -f.";

export const pickLabel = (value: string) => "Pick " + value;
export const removeLabel = (value: string) => "Remove " + value;
export const pickedWords = (n: number, w: Nouns) => (n ? counted(n, w) + " picked" : "None picked yet. Tick " + w.plural + " in the list below.");
export const STORED = "How it is stored";

// fileCommands is the role file then the command that drafts it, the CLI
// form of a role on a server that pins a type.
export const fileCommands = (text: string, file: string) => text + "strazactl drafts create -f " + file;

// coversClause is what the covers line adds to a sentence on the row, empty
// while the line names nothing.
export function coversClause(app: string, w: Nouns, c: Covers | undefined): string {
  if (!c) return "";
  if (!("objects" in c)) return "on every " + w.type + ", including the " + w.plural + " " + app + " lists later";
  if (!c.objects.length) return "";
  return "on " + counted(c.objects.length, w) + ": " + valuesWords(c.objects.map((o) => o.value));
}

// coversCell is the role page's cell for a row: what it covers, and under
// it today's count for a row on every object.
export function coversCell(w: Nouns, c: Covers | undefined): string {
  if (!c) return missingCovers(w);
  if (!("objects" in c)) return "every " + w.type;
  return counted(c.objects.length, w) + ": " + valuesWords(c.objects.map((o) => o.value));
}
export const todayCount = (n: number, w: Nouns) => counted(n, w) + " today";

// coversOn and coversText word the covers line in the draft views, which
// read role files that do not name the type: the line as the file says it.
export const coversOn = (app: string) => "Covers on " + app;
export const coversText = (c: Covers | undefined) => (!c ? "not set" : "objects" in c ? c.objects.map((o) => o.value).join(", ") : "every");

// PINNED_TYPE labels the change row of a server's pinned type, TOOL_MARKS
// the row of the tools whose marks moved.
export const PINNED_TYPE = "Pinned type";
export const TOOL_MARKS = "Tool marks";

type Mark = string | { argument: string } | undefined;

// markKey is a mark in canonical form, the reach plus the argument name. A
// tool the block does not name reads all.
const markKey = (m: Mark) => (m && typeof m === "object" ? "argument:" + m.argument : m === "none" || m === "credential" ? m : "all");

// markWords says what one tool's mark does, in the block's own type.
function markWords(tool: string, m: Mark, type: string): string {
  if (m && typeof m === "object") return tool + " names " + article(type).toLowerCase() + type + " in " + m.argument;
  if (m === "none") return tool + " reaches none";
  return m === "credential" ? tool + " relies on a login" : tool + " reaches every " + type;
}

// marksMoved words the tools whose mark differs between two pinned blocks,
// before and after, in name order. Both empty when no mark moved.
export function marksMoved(was: ObjectsBlock, now: ObjectsBlock): [string, string] {
  const tools = [...new Set([...Object.keys(was.tools || {}), ...Object.keys(now.tools || {})])].sort();
  const moved = tools.filter((t) => markKey(was.tools?.[t]) !== markKey(now.tools?.[t]));
  return [moved.map((t) => markWords(t, was.tools?.[t], was.type)).join(", "), moved.map((t) => markWords(t, now.tools?.[t], now.type)).join(", ")];
}

// marksWords counts the tools a pinned block marks by what each reaches:
// 3 tools name a repo, 2 reach every repo, 1 reaches none.
function marksWords(b: ObjectsBlock): string {
  const n = { argument: 0, all: 0, credential: 0, none: 0 };
  for (const v of Object.values(b.tools || {})) n[typeof v === "object" ? "argument" : v === "none" || v === "credential" ? v : "all"]++;
  const verb = (count: number, one: string, many: string) => (count === 1 ? one : many);
  const parts: [number, string][] = [];
  if (n.argument) parts.push([n.argument, verb(n.argument, "names ", "name ") + article(b.type).toLowerCase() + b.type]);
  if (n.all) parts.push([n.all, verb(n.all, "reaches every ", "reach every ") + b.type]);
  if (n.credential) parts.push([n.credential, verb(n.credential, "relies on a login", "rely on a login")]);
  if (n.none) parts.push([n.none, verb(n.none, "reaches none", "reach none")]);
  return parts.map(([count, words], i) => count + (i ? " " : count === 1 ? " tool " : " tools ") + words).join(", ");
}

// pinnedWords words a server's whole pinned block: the type and plural,
// the listing tool and the tool marks, so a change to any of them shows.
export function pinnedWords(b: ObjectsBlock | undefined, none: string): string {
  if (!b || !b.type) return none;
  const head = b.type + " (" + (b.plural || b.type + "s") + "), listed by " + (b.list?.tool || none);
  const marks = marksWords(b);
  return marks ? head + ". " + marks + "." : head;
}
