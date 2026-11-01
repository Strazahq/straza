import { describe, expect, it } from "vitest";
import { type Covers, coversDoc, coversKey, coversOf, deadTools, pinnedOf, reachOf, uncovered, withPick } from "./access-covers";
import { type Plan, choiceOf, emptyPlan, grantMatchers, planFor, planSentence, withCovers } from "./access-plan";
import { coversCell, coversClause, deadTool, everyLine, laterDead, onWhichLede, theseLine, unticked } from "./access-object-words";
import type { AppRow, BindingRow, ObjectsBlock } from "./api";
import { nounsOf } from "./object-words";

// The covers line of an access row on a server that pins an object type:
// read from a binding or a file, compared by id, written as values, and the
// plan's move that keeps a row on some objects to the tools that name one.

const BLOCK: ObjectsBlock = {
  type: "repo",
  plural: "repos",
  list: { tool: "repo_list" },
  tools: { pr_create: { argument: "repo" }, pr_view: { argument: "repo" }, get_me: "none", search_code: "credential", repo_create: "all" },
};
const TOOLS = ["get_me", "pr_create", "pr_view", "repo_create", "repo_list", "search_code"];
const ghapp: AppRow = { id: "app-1", name: "ghapp", runtime: "remote", status: "running", reached_by: [], manifest: { straza: { objects: BLOCK } } };
const plain: AppRow = { id: "app-2", name: "brave-search", runtime: "remote", status: "running", reached_by: [] };
const w = nounsOf(BLOCK);
const two: Covers = { objects: [{ value: "Strazahq/pay-api", id: "R1" }, { value: "Strazahq/pay-web", id: "R2" }] };
const plan = (patch: Partial<Plan>): Plan => ({ ...emptyPlan(), ...patch });
const row = (tools: string[], covers?: BindingRow["covers"]): BindingRow => ({ id: "b-1", app: "ghapp", role: "ghapp-write", tools, covers });

describe("the covers model", () => {
  it("reads each tool's reach off the pinned block, a tool it does not name as all", () => {
    const cases: [string, string][] = [["pr_create", "argument"], ["get_me", "none"], ["search_code", "credential"], ["repo_create", "all"], ["repo_list", "all"]];
    for (const [tool, want] of cases) expect(reachOf(BLOCK, tool), tool).toBe(want);
    expect(deadTools(BLOCK, TOOLS)).toEqual(["repo_create", "repo_list", "search_code"]);
    expect(pinnedOf(ghapp)).toBe(BLOCK);
    expect(pinnedOf(plain)).toBe(null);
    expect(pinnedOf({ ...plain, manifest: { straza: { objects: { ...BLOCK, type: "" } } } })).toBe(null);
  });

  it("reads a covers line from a binding and from a role file, the objects in value order", () => {
    const cases: [string, Parameters<typeof coversOf>[0], Covers | undefined][] = [
      ["no line", undefined, undefined],
      ["every from a binding", { every: true }, { every: true }],
      ["every from a file", "every", { every: true }],
      ["objects from a binding", { objects: [{ value: "Strazahq/pay-web", id: "R2" }, { value: "Strazahq/pay-api", id: "R1" }] }, two],
      ["objects from a file", { objects: ["b", "a"] }, { objects: [{ value: "a", id: "" }, { value: "b", id: "" }] }],
      ["a line with neither form", {}, undefined],
    ];
    for (const [name, line, want] of cases) expect(coversOf(line), name).toEqual(want);
  });

  it("compares lines by id, so a rename is no change, and writes values sorted", () => {
    const renamed: Covers = { objects: [{ value: "Strazahq/payments-api", id: "R1" }, { value: "Strazahq/pay-web", id: "R2" }] };
    expect(coversKey(renamed)).toBe(coversKey(two));
    expect(coversKey({ objects: [two.objects[0] as never] })).not.toBe(coversKey(two));
    expect(coversKey({ every: true })).toBe("every");
    expect(coversKey(undefined)).toBe("");
    expect(coversDoc(two)).toEqual({ objects: ["Strazahq/pay-api", "Strazahq/pay-web"] });
    expect(coversDoc({ every: true })).toBe("every");
    expect(coversDoc(undefined)).toBe(undefined);
  });

  it("picks and unpicks an object by id", () => {
    const one = withPick(undefined, { value: "Strazahq/pay-web", id: "R2" });
    expect(withPick(one, { value: "Strazahq/pay-api", id: "R1" })).toEqual(two);
    expect(withPick(two, { value: "Strazahq/pay-web", id: "R2" })).toEqual({ objects: [{ value: "Strazahq/pay-api", id: "R1" }] });
  });

  it("asks for a covers line only on a server that pins a type", () => {
    const cases: [string, AppRow, Covers | undefined, boolean][] = [
      ["no card on a pinned server", ghapp, undefined, true],
      ["these with none picked", ghapp, { objects: [] }, true],
      ["every", ghapp, { every: true }, false],
      ["these with two picked", ghapp, two, false],
      ["a server that pins nothing", plain, undefined, false],
    ];
    for (const [name, app, c, want] of cases) expect(uncovered(app, c), name).toBe(want);
  });
});

describe("the plan's covers line", () => {
  it("seeds the line from the stored binding", () => {
    expect(planFor(row(["pr_create"], { every: true }), TOOLS).covers).toEqual({ every: true });
    expect(planFor(row(["pr_create"], { objects: [{ value: "Strazahq/pay-api", id: "R1" }] }), TOOLS).covers).toEqual({ objects: [{ value: "Strazahq/pay-api", id: "R1" }] });
    expect(planFor(row(["pr_create"]), TOOLS).covers).toBe(undefined);
  });

  // Each case is a plan, the line picked, and the tools the row names
  // after: a pick of objects unticks what nothing keeps to them.
  const cases: [string, Plan, Covers, string[]][] = [
    ["every keeps the glob", plan({ reach: "later" }), { every: true }, ["*"]],
    ["these turns the glob into ticks without the dead tools", plan({ reach: "later" }), two, ["get_me", "pr_create", "pr_view"]],
    ["these turns every tool today into ticks without them", plan({ reach: "today" }), two, ["get_me", "pr_create", "pr_view"]],
    ["these unticks a dead tool", plan({ picked: { pr_create: true, repo_create: true } }), two, ["pr_create"]],
    ["these leaves live ticks alone", plan({ picked: { pr_create: true } }), two, ["pr_create"]],
  ];
  it.each(cases)("%s", (_case, p, covers, want) => {
    const next = withCovers(p, covers, TOOLS, BLOCK);
    expect(grantMatchers(next, TOOLS)).toEqual(want);
    expect(next.covers).toEqual(covers);
  });

  // A deny choice never turns into an allow: leaving the glob leaves a
  // denied tool out of the ticks, and a deny read back under ticks stays.
  it("never widens a denied tool when the row moves to some repos", () => {
    const denyUnderGlob = plan({ reach: "later", call: "per", choice: { pr_create: "deny" }, covers: { every: true } });
    const a = withCovers(denyUnderGlob, two, TOOLS, BLOCK);
    expect(grantMatchers(a, TOOLS)).toEqual(["get_me", "pr_view"]);
    expect(choiceOf(a, "pr_create")).toBe(null);
    expect(a.choice.pr_create).toBe("deny");
    const everyCall = withCovers({ ...denyUnderGlob, call: "every" }, two, TOOLS, BLOCK);
    expect(grantMatchers(everyCall, TOOLS)).toEqual(["get_me", "pr_view"]);
    const readBack = plan({ picked: { pr_create: true, pr_view: true, repo_create: true }, call: "per", choice: { pr_create: "deny" } });
    const b = withCovers(readBack, two, TOOLS, BLOCK);
    expect(grantMatchers(b, TOOLS)).toEqual(["pr_create", "pr_view"]);
    expect(choiceOf(b, "pr_create")).toBe("deny");
  });

  it("joins the covers line to the sentence on what the row reaches", () => {
    const p = plan({ picked: { pr_create: true, pr_view: true } });
    expect(planSentence(p, "ghapp", TOOLS, "ghapp-write", coversClause("ghapp", w, two))).toBe(
      "Holders of ghapp-write reach 2 of 6 tools of ghapp, on 2 repos: Strazahq/pay-api and Strazahq/pay-web. No call needs approval from this role.",
    );
    expect(planSentence(p, "ghapp", TOOLS, "ghapp-write", coversClause("ghapp", w, { every: true }))).toBe(
      "Holders of ghapp-write reach 2 of 6 tools of ghapp, on every repo, including the repos ghapp lists later. No call needs approval from this role.",
    );
    expect(planSentence(p, "ghapp", TOOLS, "ghapp-write", coversClause("ghapp", w, { objects: [] }))).toBe(planSentence(p, "ghapp", TOOLS, "ghapp-write"));
    expect(planSentence(plan({}), "ghapp", TOOLS, "ghapp-write", "on every repo")).toBe(planSentence(plan({}), "ghapp", TOOLS, "ghapp-write"));
  });
});

describe("the covers words", () => {
  it("speaks in the server's type and plural", () => {
    expect(everyLine("ghapp", w, 10)).toBe("All 10 today, and every repo ghapp lists later.");
    expect(everyLine("ghapp", w, null)).toBe("Every repo ghapp lists, today and later.");
    expect(theseLine("ghapp", w)).toBe("Pick from ghapp's list. A repo added later stays out until you add it.");
    expect(theseLine("gitlab", nounsOf({ type: "issue" }))).toBe("Pick from gitlab's list. An issue added later stays out until you add it.");
    expect(deadTool("repo_create", w, false)).toBe("repo_create cannot be in a role on some repos: nothing keeps it to them. Make a role on every repo for it.");
    expect(deadTool("query", nounsOf({ type: "database" }), true)).toBe(
      "query cannot be in a role on some databases: its calls do not name a database, and Straza does not check a role's own login yet. Make a role on every database for it.",
    );
    expect(laterDead("ghapp", w)).toBe("A tool ghapp adds later cannot be in a role on some repos: nothing keeps it to them. Make a role on every repo for it.");
    expect(unticked(["repo_create"], w)).toBe("repo_create was unticked: it cannot be in a role on some repos.");
    expect(unticked(["repo_create", "search_code"], w)).toBe("repo_create and search_code were unticked: they cannot be in a role on some repos.");
    expect(onWhichLede("ghapp", w, 14)).toBe("ghapp says 14 of its tools work on one repo. Pick which repos this role reaches with them.");
    expect(coversCell(w, { objects: ["a", "b", "c", "d", "e"].map((v) => ({ value: v, id: v })) })).toBe("5 repos: a, b, c and 2 more");
    expect(coversCell(w, undefined)).toBe("Say which repos this role covers.");
  });
});
