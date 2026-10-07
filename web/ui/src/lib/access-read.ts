// What a door reads before it opens the access editor on a role's access to
// one server: the role's own set, the plan it seeds, the rules the editor
// shows and leaves alone, and whether the viewer may change policy. The
// role page's sheet and the server page's Add role sheet read the same way,
// so both open on the same plan.
import { type FixedRule, type Plan, fixedFrom, planFor, readOwnRules, rulesFor } from "./access-plan";
import { type ApiError, type BindingRow, type PreviewEntry, getPolicy } from "./api";
import { heldKinds } from "./held-kinds";
import { openDoc, rulesOf } from "./policy-model";
import { accessSetName, heldWord, shortShapeWords } from "./role-words";
import { snapshot } from "./session";

// readSetText reads one set's stored text: null when there is none,
// undefined when it could not be read.
export async function readSetText(name: string): Promise<string | null | undefined> {
  try {
    return (await getPolicy(name)).yaml || "";
  } catch (e) {
    return (e as ApiError).status === 404 ? null : undefined;
  }
}

// Unread says which reads behind the editor failed: own for the role's own
// set, others for the server's preview of the role, the one read of every
// other policy that decides a call. The editor then never says what a call
// does from what it could not read.
export type Unread = { own: boolean; others: boolean };

// AccessRead is what the editor opens on: the plan the stored row and the
// own set say, the rules it shows and does not change, the rule ids the own
// set keeps for other rules, which a save keeps clear of, the reads that
// failed, and the own set's text as readSetText answered it.
export type AccessRead = { initial: Plan; fixed: Record<string, FixedRule>; taken: string[]; unread: Unread; text: string | null | undefined };

// accessRead seeds the editor for role on app. ownText is the role's own
// set as readSetText answered it. preview is the server's preview of the
// role on app: null when it could not be read, undefined when none was
// asked. texts are other sets' texts, which turn a held tool's word into
// hold or ticket with its window. An own set that could not be read seeds
// the plan from the row alone and shows every rule the preview names as
// fixed, since the editor cannot tell which are its own and must not write
// over them.
export function accessRead(role: string, app: string, names: string[], binding: BindingRow | null, ownText: string | null | undefined, preview: Record<string, PreviewEntry> | null | undefined, texts: Record<string, string | null> = {}): AccessRead {
  const ownSet = accessSetName(role);
  const own = ownText === undefined ? null : readOwnRules(ownText, app, names);
  const fixed = fixedFrom(preview, own ? ownSet : "", own);
  const kinds = heldKinds(preview, texts);
  for (const t of Object.keys(fixed)) {
    const f = fixed[t];
    if (f.status === "approve_gated" && f.set !== ownSet && kinds[t]) fixed[t] = { ...f, word: heldWord(shortShapeWords(kinds[t])) };
  }
  let taken: string[] = [];
  if (own && ownText) {
    try {
      taken = rulesOf(openDoc(ownText)).map((v) => v.id).filter((id) => id && !own.owned.includes(id));
    } catch {
      taken = [];
    }
  }
  return { initial: planFor(binding, names, own), fixed, taken, unread: { own: ownText === undefined, others: preview === null }, text: ownText };
}

// retires says whether a save would leave the role's own set with no rule,
// which deletes it: the plan it opened with wrote rules, the plan now
// writes none, and the set keeps no other rule.
export const retires = (read: AccessRead, plan: Plan, app: string, names: string[], role: string): boolean =>
  !read.taken.length && rulesFor(read.initial, app, names, role).length > 0 && rulesFor(plan, app, names, role).length === 0;

// mayWritePolicy says whether this session may publish policy: a full
// grant or policy:write. A session without it changes which tools a role
// reaches and reads what a call does. Display only: the server re-checks
// every write.
export function mayWritePolicy(): boolean {
  const grants = snapshot()?.grants || "";
  return grants === "full" || grants.split(",").includes("policy:write");
}
