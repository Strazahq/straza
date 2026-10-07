// The items the role doors send through the editors' two saves. A Role put
// is the role's live export with the one change
// the door makes, so every other field goes back as live has it, and the
// server writes it in canonical form. The access editor adds the role's own
// set when its rules change, so an access row and its gate go live together
// or not at all. No door builds a document from a draft's answer, which is
// masked and never round-trips.
import { parse, stringify } from "yaml";
import { grantMatchers } from "./access-plan";
import { type ApiError, getPolicy } from "./api";
import type { SaveItem } from "./draft-save";
import { type GrantInput, ownSet, planRows } from "./grant-commit";
import { docText } from "./policy-model";
import { UNREACHABLE_STEP, accessSetName, setUnreadable } from "./role-words";
import { savedEdit } from "./save-words";

// RoleSpec is the spec of a Role document as the export writes it
// (spec/objects section 2).
export type RoleSpec = {
  kind?: string;
  description?: string;
  server?: string;
  implies?: string[];
  bindings?: { app: string; tools: string[] }[];
  packs?: string[];
};

type RoleDocument = { apiVersion: string; kind: string; metadata: { name: string }; spec: RoleSpec };

// Items is what a door sends, or the sentence that says why it sends
// nothing.
export type Items = { items: SaveItem[] } | { error: string };

// put answers the Role put of doc, with the empty fields left out as the
// export leaves them.
function put(doc: RoleDocument): SaveItem {
  const s = doc.spec;
  if (!s.description) delete s.description;
  for (const k of ["implies", "bindings", "packs"] as const) if (!s[k] || !s[k].length) delete s[k];
  return { kind: "Role", name: doc.metadata.name, op: "put", doc: stringify(doc) };
}

// roleItem answers the Role put of the export text with change applied to
// its spec.
export function roleItem(text: string, change: (spec: RoleSpec) => void): SaveItem {
  const doc = parse(text) as RoleDocument;
  doc.spec = doc.spec || {};
  change(doc.spec);
  return put(doc);
}

// setItem answers the item of the role's own set for one save of the
// access editor: put with its new rules, remove when no rule is left, null
// when there is no set and nothing to write, or the sentence that stops
// the save. A read strazad did not answer changed nothing, so it says the
// set was not read rather than that a step may have landed.
export async function setItem(input: GrantInput): Promise<SaveItem | null | { error: string }> {
  const name = accessSetName(input.role.name);
  const set = await ownSet(input);
  if ("error" in set) return { error: set.error === UNREACHABLE_STEP ? setUnreadable(name) : set.error };
  if ("retire" in set) return { kind: "PolicySet", name, op: "remove", doc: "" };
  if ("doc" in set) return { kind: "PolicySet", name, op: "put", doc: docText(set.doc) };
  return null;
}

// OwnSet is the role's own set as the access sheet reads it: its text as
// readSetText answers it, null when there is none and undefined when it
// could not be read, and saved when the text is a saved edit nobody
// published, which the policies route answers in place of the published
// text.
export type OwnSet = { text: string | null | undefined; saved: boolean };

// readOwnSet reads the set name for the access sheet.
export async function readOwnSet(name: string): Promise<OwnSet> {
  try {
    const d = await getPolicy(name);
    return { text: d.yaml || "", saved: !!d.drift };
  } catch (e) {
    return { text: (e as ApiError).status === 404 ? null : undefined, saved: false };
  }
}

// accessItems answers the items of one save of the access editor on a role
// that exists: its live Role document with the one access row, when the row
// changes, then its own set, when the rules change. roleText is the role's
// live export. The row joins the export's rows in place of the row on the
// same server, so a row the page did not show is never dropped, and a
// second server is the server's to refuse. While the own set holds a saved
// edit nobody published, the editor stands on unreviewed text, so nothing
// is sent.
export async function accessItems(input: GrantInput, roleText: string): Promise<Items> {
  const name = accessSetName(input.role.name);
  if ((await readOwnSet(name)).saved) return { error: savedEdit(name) };
  const rows = planRows(input);
  const items: SaveItem[] = [];
  if (rows.some((r) => r.key === "grant")) {
    const tools = grantMatchers(input.plan, input.tools.map((t) => t.name));
    items.push(roleItem(roleText, (s) => { s.bindings = (s.bindings || []).filter((b) => b.app !== input.app.name).concat({ app: input.app.name, tools }); }));
  }
  if (rows.some((r) => r.key === "publish")) {
    const set = await setItem(input);
    if (set && "error" in set) return set;
    if (set) items.push(set);
  }
  return { items };
}

// NewRole is what the New role wizard knows of the role it makes.
export type NewRole = { name: string; kind: string; description: string; implies: string[] };

// newRoleItems answers the items of the New role wizard and the Add role
// sheet: the Role put of the new role, owned by the server of the access
// row grant names when there is one, then the role's own set when the grant
// writes rules. The document names no pack, because a draft never applies
// one, so packs are bound after the publish.
export async function newRoleItems(role: NewRole, grant: GrantInput | null): Promise<Items> {
  const spec: RoleSpec = { kind: role.kind, description: role.description, implies: role.implies.slice().sort() };
  if (grant) {
    spec.server = grant.app.name;
    spec.bindings = [{ app: grant.app.name, tools: grantMatchers(grant.plan, grant.tools.map((t) => t.name)) }];
  }
  const items = [put({ apiVersion: "straza.dev/v1beta1", kind: "Role", metadata: { name: role.name }, spec })];
  if (grant && planRows(grant).some((r) => r.key === "publish")) {
    const set = await setItem(grant);
    if (set && "error" in set) return set;
    if (set) items.push(set);
  }
  return { items };
}
