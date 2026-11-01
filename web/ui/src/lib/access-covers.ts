// The covers line of an access row on a server whose manifest pins an
// object type: every object of the type, or these objects by id. The
// access editor, the role doors and the role page read it the same way, so
// the model lives apart from all of them. No React and no sentence here.
import type { AppRow, CoversLine, ObjectListing, ObjectsBlock } from "./api";

// Picked is one object a row names: the value a role file writes, the id
// it resolves to, and the title the picker showed, which is never sent.
export type Picked = { value: string; id: string; title?: string };

// Covers is what the editor holds for the covers line: every object, or
// the objects picked, an empty list while none is picked yet.
export type Covers = { every: true } | { objects: Picked[] };

// CoversDoc is the covers line as a role file writes it: the scalar every,
// or the values, never the ids.
export type CoversDoc = "every" | { objects: string[] };

// ToolReach is what a pinned block says of one tool. A tool the block does
// not name reads all, the listing tool included, as the gateway reads it.
export type ToolReach = "argument" | "none" | "credential" | "all";

// pinnedOf answers the type a server's live manifest pins, or null.
export function pinnedOf(app: AppRow | null | undefined): ObjectsBlock | null {
  const b = app?.manifest?.straza?.objects;
  return b && b.type ? b : null;
}

// reachOf reads one tool's reach off a pinned block.
export function reachOf(block: ObjectsBlock, tool: string): ToolReach {
  const v = block.tools?.[tool];
  if (v && typeof v === "object") return "argument";
  return v === "none" || v === "credential" ? v : "all";
}

// deadTools lists the tools a row on some objects cannot hold: nothing
// keeps a tool whose reach is all to the row's objects, and the reach of a
// login is not checked yet, so credential counts as all.
export const deadTools = (block: ObjectsBlock, names: string[]): string[] => names.filter((t) => {
  const r = reachOf(block, t);
  return r === "all" || r === "credential";
});

// partial says the covers line names objects rather than every one.
export const partial = (c: Covers | undefined): c is { objects: Picked[] } => !!c && "objects" in c;

const byValue = (a: Picked, b: Picked) => (a.value < b.value ? -1 : a.value > b.value ? 1 : 0);

// coversOf reads the covers line a binding or a role file carries. A line
// with neither form reads as no line.
export function coversOf(line: CoversLine | CoversDoc | null | undefined): Covers | undefined {
  if (line === "every" || (line && typeof line === "object" && "every" in line && line.every)) return { every: true };
  if (line && typeof line === "object" && Array.isArray(line.objects)) {
    const objects = (line.objects as (string | Picked)[]).map((o) => (typeof o === "string" ? { value: o, id: "" } : { value: o.value, id: o.id }));
    return { objects: objects.sort(byValue) };
  }
  return undefined;
}

// coversKey is what two covers lines are compared by: the ids, so a
// renamed object is no change. A line read from a file has no ids, and its
// values stand in for them.
export function coversKey(c: Covers | undefined): string {
  if (!c) return "";
  if (!partial(c)) return "every";
  return "objects:" + c.objects.map((o) => o.id || "=" + o.value).sort().join("\x00");
}

// coversDoc is the line a role file writes, the values sorted.
export function coversDoc(c: Covers | undefined): CoversDoc | undefined {
  if (!c) return undefined;
  return partial(c) ? { objects: c.objects.map((o) => o.value).sort() } : "every";
}

// withPick adds the object to a partial line, or takes it off when it is
// there, keeping the list in value order.
export function withPick(c: Covers | undefined, o: Picked): Covers {
  const list = partial(c) ? c.objects : [];
  const has = list.some((x) => x.id === o.id);
  return { objects: (has ? list.filter((x) => x.id !== o.id) : list.concat(o)).sort(byValue) };
}

// listed says a listing has given the list a count can be read from: it
// ran and the listing credential is there. An older server sends no
// listing state, and its count stands.
export const listed = (l: ObjectListing | null | undefined): boolean => !l || (l.outcome === "ok" && l.credential !== "missing");

// uncovered says a save on the server must first say which objects the
// row covers: the server pins a type, and no card is picked or the list
// names nothing.
export const uncovered = (app: AppRow | null | undefined, c: Covers | undefined): boolean =>
  !!pinnedOf(app) && (!c || (partial(c) && !c.objects.length));
