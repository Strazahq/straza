// The stepwise commit behind the server page's Add role and Edit tools
// sheet: create the role when it is new,
// remove the access row the save replaces, write the new one, and, when
// the rules change, write them into the role's own policy set. The steps
// run in order with no client rollback, so a half-landed save says exactly
// what landed and what did not.
//
// The set is edited in place through the Policies area's document model,
// so a hand-written comment or key in it survives a save. The save makes
// the set's rules for one server what the plan says and leaves every other
// rule where it was.
import { type OwnRule, type Plan, type Shape, SPONSOR, callSentence, grantMatchers, ownRuleOf, readOwnRules, rulesFor, sameRules } from "./access-plan";
import { type ApiError, type AppRow, type BindingRow, type PolicyDoc, type RoleRow, type ToolRow, applyPolicy, createBinding, createRole, deactivatePolicy, deletePolicy, getPolicy, removeBinding } from "./api";
import { type Doc, type Plain, addRule, docProblems, docText, moveRule, newPolicyDoc, openDoc, readSet, removeRule, rulesOf, setApprove, setClass, setNames, setReason, uniqueId } from "./policy-model";
import { ROW_REMOVE, UNREACHABLE_STEP, accessSetDescription, approveGroupReason, accessSetName, retireHalf, retireKept, rowCreate, rowGrant, rowPublish, rowRetire, setMismatch, setUnparsed, setUnreadable, toolsWords } from "./role-words";
import { refused } from "./say";

// CommitRow is one act of the save and the state it landed in. draft is
// the publish row when the set was stored but not activated; refused is a
// row the server answered no to before anything was written.
export type CommitRow = {
  key: "create" | "remove" | "grant" | "publish";
  label: string;
  state: "pending" | "running" | "done" | "failed" | "draft" | "refused";
  error?: string;
};

// GrantInput is one save: the role (with create when the save makes it),
// the server and its tools, the plan, the access row this replaces, and
// keep for a save whose tools are unchanged, which writes only the rules.
// before is the plan the editor opened with, read back from the role's own
// set: the rules are written when the plan's differ from it, or, with no
// before, when the plan has any.
export type GrantInput = {
  role: { name: string; id?: string; create?: { kind: string; description: string } };
  app: AppRow;
  tools: ToolRow[];
  plan: Plan;
  replace?: BindingRow | null;
  keep?: boolean;
  before?: Plan;
};

// Published is the stored set the publish dialog decides over: its text,
// the text before the save, and whether the live version is the one being
// replaced. Both texts are rendered the same way, so they differ only where
// the save changed something.
export type Published = { name: string; yaml: string; baseYaml: string | null; replaces: boolean };

export type GrantResult = { rows: CommitRow[]; role?: RoleRow; published?: Published };

const names = (tools: ToolRow[]) => tools.map((t) => t.name);

// planRows is what the save will do, before it does any of it: the review
// step and the sheet both draw this list.
export function planRows(input: GrantInput): CommitRow[] {
  const all = names(input.tools);
  const app = input.app.name;
  const role = input.role.name;
  const matchers = grantMatchers(input.plan, all);
  const rules = input.before ? !sameRules(input.before, input.plan, app, all, role) : rulesFor(input.plan, app, all, role).length > 0;
  const rows: CommitRow[] = [];
  if (input.role.create) rows.push({ key: "create", label: rowCreate(role), state: "pending" });
  if (input.replace) rows.push({ key: "remove", label: ROW_REMOVE, state: "pending" });
  if (!input.keep) rows.push({ key: "grant", label: rowGrant(toolsWords(matchers, all.length)), state: "pending" });
  if (rules) rows.push({ key: "publish", label: rowPublish(accessSetName(role), callSentence(input.plan, app, all, role)), state: "pending" });
  return rows;
}

// mark patches one row, the way the publish dialog finishes the publish
// row after the run has returned.
export const mark = (rows: CommitRow[], key: CommitRow["key"], patch: Partial<CommitRow>): CommitRow[] =>
  rows.map((r) => (r.key === key ? { ...r, ...patch } : r));

const sameShape = (a: Shape, b: Shape) => a.pool === b.pool && a.how === b.how && (a.how === "hold" ? a.hold === b.hold : a.ticket === b.ticket && a.grant === b.grant);
const sameNames = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i]);

// writeShape changes a kept rule's pool and windows where the new setting
// differs, so an unchanged rule keeps its text to the byte.
function writeShape(doc: Doc, id: string, was: Shape, want: Shape) {
  if (was.pool !== want.pool) setApprove(doc, id, { who: want.pool === SPONSOR ? { sponsor: true, roles: [] } : { sponsor: false, roles: [want.pool] } });
  const switched = was.how !== want.how;
  if (switched) setClass(doc, id, want.how);
  if (want.how === "hold" && (switched || was.hold !== want.hold)) setApprove(doc, id, { timeoutSeconds: want.hold });
  if (want.how === "ticket" && (switched || was.ticket !== want.ticket)) setApprove(doc, id, { ticketTTLSeconds: want.ticket });
  if (want.how === "ticket" && (switched || was.grant !== want.grant)) setApprove(doc, id, { grantTTLSeconds: want.grant });
}

// Kept is an owned rule kept for a wanted rule, with its place in the set,
// its reason and how many of the wanted rule's tools it names.
type Kept = { own: OwnRule; index: number; reason: unknown; overlap: number };

// Job is one rule the plan wants: the owned rules that keep doing it, or
// the id of the rule added for it.
type Job = { own: OwnRule; want: Plain; kept: Kept[]; added?: string };

// writePlan makes the set's rules for one server the rules the plan wants.
// An owned rule is kept when it names tools of a wanted rule of its kind
// (the rule for every call needs no tools), preferring the one whose
// setting already matches, so its reason, notify, binding and comments
// stay. Several kept rules can share one wanted rule, each keeping the
// tools it names, and the one naming the most takes the new tools. Owned
// rules nothing wants are removed. A new rule lands beside this server's
// kept rules in the order rulesFor gives, and the rule for every call
// stays after every tool's own rule, since the earlier approval decides.
// A kept rule's reason changes only when it is the one this editor wrote
// for its old tools, so a reason that names a tool stays true. It answers
// the ids of the server's rules it leaves.
function writePlan(doc: Doc, owned: string[], plan: Plan, app: string, tools: string[], role: string): string[] {
  const views = rulesOf(doc);
  const mine = views.filter((v) => owned.includes(v.id)).map((v) => ({ own: ownRuleOf(v.raw, app) as OwnRule, index: v.index, reason: v.raw.reason }));
  const taken = views.map((v) => v.id).filter((id) => !owned.includes(id));
  const jobs: Job[] = rulesFor(plan, app, tools, role, taken).map((want) => ({ own: ownRuleOf(want, app) as OwnRule, want, kept: [] }));
  const pairs: { job: Job; kept: Kept; alike: boolean }[] = [];
  for (const job of jobs) {
    for (const n of mine) {
      if (n.own.kind !== job.own.kind) continue;
      const overlap = job.own.kind === "every" ? 1 : n.own.tools.filter((t) => job.own.tools.includes(t)).length;
      if (overlap) pairs.push({ job, kept: { ...n, overlap }, alike: !n.own.shape || !job.own.shape || sameShape(n.own.shape, job.own.shape) });
    }
  }
  pairs.sort((a, b) => Number(b.alike) - Number(a.alike) || b.kept.overlap - a.kept.overlap || a.kept.index - b.kept.index);
  const used: string[] = [];
  for (const p of pairs) {
    if (used.includes(p.kept.own.id) || (p.job.own.kind === "every" && p.job.kept.length)) continue;
    used.push(p.kept.own.id);
    p.job.kept.push(p.kept);
  }
  for (const n of mine) if (!used.includes(n.own.id)) removeRule(doc, n.own.id);

  for (const job of jobs) {
    if (!job.kept.length) continue;
    const primary = job.kept.slice().sort((a, b) => b.overlap - a.overlap || a.index - b.index)[0];
    const covered = job.kept.flatMap((k) => k.own.tools.filter((t) => job.own.tools.includes(t)));
    for (const k of job.kept) {
      if (job.own.kind !== "every") {
        const list = k.own.tools.filter((t) => job.own.tools.includes(t)).concat(k === primary ? job.own.tools.filter((t) => !covered.includes(t)) : []);
        if (!sameNames(list, k.own.tools)) {
          setNames(doc, k.own.id, list);
          if (k.reason === approveGroupReason(app, k.own.tools)) setReason(doc, k.own.id, approveGroupReason(app, list));
        }
      }
      if (k.own.shape && job.own.shape) writeShape(doc, k.own.id, k.own.shape, job.own.shape);
    }
  }

  const ids = () => rulesOf(doc).map((v) => v.id);
  const spots = (js: Job[]) => js.flatMap((j) => (j.added ? [j.added] : j.kept.map((k) => k.own.id))).map((id) => ids().indexOf(id));
  jobs.forEach((job, i) => {
    if (job.kept.length) return;
    const id = uniqueId(String(job.want.id), ids());
    const after = spots(jobs.slice(i + 1));
    const before = spots(jobs.slice(0, i));
    addRule(doc, { ...job.want, id });
    if (after.length) moveRule(doc, id, Math.min(...after));
    else if (before.length) moveRule(doc, id, Math.max(...before) + 1);
    job.added = id;
  });
  const every = jobs.find((j) => j.own.kind === "every");
  const everyId = every && (every.added || every.kept[0].own.id);
  const last = Math.max(-1, ...spots(jobs.filter((j) => j.own.kind === "group")));
  if (everyId && ids().indexOf(everyId) < last) moveRule(doc, everyId, last);
  const left = jobs.flatMap((j) => (j.added ? [j.added] : j.kept.map((k) => k.own.id)));
  return ids().filter((id) => left.includes(id));
}

// storedRules is what a save would store for one server in the role's own
// set, read off the set's stored text without saving: the same in-place
// write, then the server's rules it leaves, with their ids, reasons,
// binding and notify. null when the text does not parse.
export function storedRules(text: string, plan: Plan, app: string, tools: string[], role: string): Plain[] | null {
  const doc = openDoc(text);
  if (docProblems(doc).length) return null;
  const ids = writePlan(doc, readOwnRules(text, app, tools).owned, plan, app, tools, role);
  return rulesOf(doc).filter((v) => ids.includes(v.id)).map((v) => v.raw);
}

type SetWrite = { doc: Doc; baseYaml: string | null; replaces: boolean } | { retire: true; live: boolean } | { none: true } | { error: string };

// ownSet reads the role's own set and answers what to store: a new set
// when there is none and the plan has rules, the set edited in place when
// it matches exactly this role, retire when the edit leaves it with no
// rule, or none when there is no set and nothing to write. A set that
// matches anything else is left alone, because writing into it would
// change who the rules gate. The role doors' drafts read the set through
// it too (role-draft.ts).
export async function ownSet(input: GrantInput): Promise<SetWrite> {
  const role = input.role.name;
  const name = accessSetName(role);
  const app = input.app.name;
  const all = names(input.tools);
  let existing: PolicyDoc | null = null;
  try {
    existing = await getPolicy(name);
  } catch (e) {
    const err = e as ApiError;
    if (err.status !== 404) return { error: err.unreachable ? UNREACHABLE_STEP : setUnreadable(name) };
  }
  if (!existing) {
    const rules = rulesFor(input.plan, app, all, role);
    if (!rules.length) return { none: true };
    return { doc: newPolicyDoc({ name, description: accessSetDescription(role), priority: 100, roles: [role], rules }), baseYaml: null, replaces: false };
  }
  const text = existing.yaml || "";
  const doc = openDoc(text);
  if (docProblems(doc).length) return { error: setUnparsed(name) };
  const facts = readSet(doc);
  if (facts.roles.length !== 1 || facts.roles[0] !== role || facts.matchOther) return { error: setMismatch(name, role) };
  const baseYaml = docText(doc);
  writePlan(doc, readOwnRules(text, app, all).owned, input.plan, app, all, role);
  if (!rulesOf(doc).length) return facts.capture || facts.escape ? { error: retireKept(name) } : { retire: true, live: existing.status === "active" };
  return { doc, baseYaml, replaces: existing.status === "active" };
}

// runGrant runs the steps in order and calls onRow after every state
// change. It stops at the first failure, leaving the rest waiting, and it
// stops with the publish row running once the set is stored: activating it
// is the publish dialog's decision, not this module's. A save that leaves
// the set with no rule deletes it instead, turning it off first when it
// is live, because the server refuses a set with no rule.
export async function runGrant(input: GrantInput, onRow: (rows: CommitRow[]) => void): Promise<GrantResult> {
  const matchers = grantMatchers(input.plan, names(input.tools));
  const role = input.role.name;
  const setName = accessSetName(role);
  let rows = planRows(input);
  const out: GrantResult = { rows };
  const put = (key: CommitRow["key"], patch: Partial<CommitRow>) => {
    rows = mark(rows, key, patch);
    out.rows = rows;
    onRow(rows);
  };
  const fail = (key: CommitRow["key"], text: string) => {
    put(key, { state: "failed", error: text });
    return out;
  };
  const refuse = (key: CommitRow["key"], e: unknown) => {
    const err = e as ApiError;
    return fail(key, err.unreachable ? UNREACHABLE_STEP : refused(err));
  };

  for (const key of rows.map((r) => r.key)) {
    put(key, { state: "running" });
    if (key === "create" && input.role.create) {
      try {
        out.role = await createRole(role, input.role.create.kind, input.role.create.description);
      } catch (e) {
        return refuse(key, e);
      }
    } else if (key === "remove" && input.replace) {
      try {
        await removeBinding(input.replace.id);
      } catch (e) {
        return refuse(key, e);
      }
    } else if (key === "grant") {
      try {
        await createBinding(input.app.id, role, matchers);
      } catch (e) {
        return refuse(key, e);
      }
    } else if (key === "publish") {
      const set = await ownSet(input);
      if ("error" in set) return fail(key, set.error);
      if ("retire" in set) {
        put(key, { label: rowRetire(setName) });
        try {
          if (set.live) await deactivatePolicy(setName);
        } catch (e) {
          return refuse(key, e);
        }
        try {
          await deletePolicy(setName);
        } catch (e) {
          const err = e as ApiError;
          return fail(key, err.unreachable ? UNREACHABLE_STEP : (set.live ? retireHalf(setName) + " " : "") + refused(err));
        }
      } else if ("doc" in set) {
        const yaml = docText(set.doc);
        try {
          await applyPolicy(yaml);
        } catch (e) {
          return refuse(key, e);
        }
        out.published = { name: setName, yaml, baseYaml: set.baseYaml, replaces: set.replaces };
        return out;
      }
    }
    put(key, { state: "done" });
  }
  return out;
}
