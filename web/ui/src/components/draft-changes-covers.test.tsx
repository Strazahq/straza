import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { DraftChanges } from "./draft-changes";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { ManifestDoc } from "@/lib/api";
import { roleOf } from "@/lib/drafts-model";
import { changeRows } from "@/lib/manifest-edit";
import { COVERAGE, COVERS_DETAIL, detailWith, trackerRole } from "@/test/drafts-fixture";

// A draft's role and server cards on a server that pins an object type:
// the objects in the server's own word where the verdict sends a coverage
// entry, else the covers line before and after as the file writes it, and
// the pinned type as a change row, so a draft that first pins one is not
// an empty change.

const roleDoc = (name: string, covers: string[]) =>
  ["apiVersion: straza.dev/v1beta1", "kind: Role", "metadata:", "  name: " + name, "spec:", "  kind: application", "  server: ghapp", "  bindings:", "    - app: ghapp", "      tools: [pr_create]"].concat(covers).join("\n") + "\n";
const EVERY = ["      covers: every"];
const TWO = ["      covers:", "        objects: [Strazahq/pay-web, Strazahq/pay-api]"];

const card = (object: string) => document.querySelector('[data-item="' + object + '"]') as HTMLElement;
const mount = (items: { name: string; doc: string; existed: boolean }[], live: Record<string, { op: "put"; doc: string }> = {}) =>
  render(
    <TooltipProvider>
      <DraftChanges detail={detailWith({ draft: { items: items.map((i) => ({ kind: "Role" as const, op: "put" as const, ...i })) }, live, changes: [] })} />
    </TooltipProvider>,
  );

describe("the objects of a role in a draft, from the coverage list", () => {
  const names = (el: Element | null) => [...(el?.querySelectorAll("[data-name]") || [])].map((n) => [n.textContent, n.hasAttribute("data-added")]);
  const render1 = (d = COVERS_DETAIL) => render(<TooltipProvider><DraftChanges detail={d} /></TooltipProvider>);

  it("shows a changed role's tickets now and after, the added ones marked, and its tools unchanged", () => {
    render1();
    const c = card("Role/tracker-triage");
    expect(c.querySelector("[data-kind]")?.textContent).toBe("role · owned by tracker");
    const row = c.querySelector('[data-fact="Tickets"]') as HTMLElement;
    expect(names(row.querySelector('[data-side="now"]'))).toEqual([["T-1", false]]);
    expect(names(row.querySelector('[data-side="after"]'))).toEqual([["T-1", false], ["+ T-2", true], ["+ T-3", true]]);
    expect(c.querySelector('[data-fact="Tools"]')?.textContent).toBe("Tools3 tools, unchanged");
    expect(c.querySelector('[data-row="Covers on tracker"]')).toBeNull();
    expect(c.querySelector("table")).toBeNull();
  });

  it("shows an existing role that covered no ticket with nothing now and the ticket it gains", () => {
    render1(detailWith({ ...COVERS_DETAIL, verdict: { ...COVERS_DETAIL.verdict, objects: [{ ...COVERAGE, before: [], after: [{ id: "t2", value: "T-2" }] }] } }));
    const row = card("Role/tracker-triage").querySelector('[data-fact="Tickets"]') as HTMLElement;
    expect(row.querySelector('[data-side="now"]')?.textContent).toBe("Nownothing");
    expect(names(row.querySelector('[data-side="after"]'))).toEqual([["+ T-2", true]]);
  });

  it("names every ticket for a role that covers them all", () => {
    render1(detailWith({ ...COVERS_DETAIL, verdict: { ...COVERS_DETAIL.verdict, objects: [{ ...COVERAGE, every_after: true, after: [] }] } }));
    expect(card("Role/tracker-triage").querySelector('[data-fact="Tickets"] [data-side="after"]')?.textContent).toContain("every ticket");
  });

  it("shows a new role's tickets beside its tools", () => {
    const fresh = { ...COVERAGE, role: "tracker-new", holders_count: 0, before: [], after: [{ id: "t9", value: "T-9" }] };
    render1(detailWith({ draft: { items: [{ kind: "Role", name: "tracker-new", op: "put", doc: trackerRole("tracker-new", ["ticket_read"], ["T-9"]), existed: false }] }, verdict: { objects: [fresh] }, live: {}, changes: [] }));
    const c = card("Role/tracker-new");
    expect(names(c.querySelector('[data-fact="Tickets"]'))).toEqual([["T-9", false]]);
    expect(names(c.querySelector('[data-fact="Tools"]'))).toEqual([["ticket_read", false]]);
  });
});

describe("the covers line in a draft, where the verdict sends no coverage entry", () => {
  it("reads a row's covers line from a role file, in value order", () => {
    expect(roleOf(roleDoc("ghapp-write", TWO))?.bindings).toEqual([{ app: "ghapp", tools: ["pr_create"], covers: { objects: [{ value: "Strazahq/pay-api", id: "" }, { value: "Strazahq/pay-web", id: "" }] } }]);
    expect(roleOf(roleDoc("ghapp-write", []))?.bindings).toEqual([{ app: "ghapp", tools: ["pr_create"] }]);
  });

  it("names the line of a new role beside its tools", () => {
    mount([{ name: "ghapp-write", doc: roleDoc("ghapp-write", TWO), existed: false }]);
    expect(card("Role/ghapp-write").querySelector('[data-fact="Covers on ghapp"]')?.textContent).toBe("Covers on ghappStrazahq/pay-api, Strazahq/pay-web");
  });

  // Each case is the live line and the draft's line of a changed role:
  // a change shows as a Now and After row, the same line shows none.
  const cases: [string, string[], string[], [string, string] | null][] = [
    ["every narrowed to two repos", EVERY, TWO, ["every", "Strazahq/pay-api, Strazahq/pay-web"]],
    ["a line set where there was none", [], EVERY, ["not set", "every"]],
    ["the same line", EVERY, EVERY, null],
  ];
  it.each(cases)("shows %s", (_case, was, now, want) => {
    mount([{ name: "ghapp-write", doc: roleDoc("ghapp-write", now), existed: true }], { "Role/ghapp-write": { op: "put", doc: roleDoc("ghapp-write", was) } });
    const row = card("Role/ghapp-write").querySelector('[data-row="Covers on ghapp"]');
    if (!want) expect(row).toBe(null);
    else expect(Array.from((row as HTMLElement).querySelectorAll("td")).map((td) => td.textContent)).toEqual(["Covers on ghapp", ...want]);
  });

  it("counts a first pin of a type as a change of the server", () => {
    const before: ManifestDoc = { straza: { runtime: { kind: "remote", remote: { url: "https://ghapp.internal/mcp" } } } };
    const after: ManifestDoc = { straza: { ...before.straza, objects: { type: "repo", plural: "repos", list: { tool: "repo_list" } } } };
    expect(changeRows(before, after)).toEqual([["Pinned type", "none", "repo (repos), listed by repo_list"]]);
    expect(changeRows(after, before)).toEqual([["Pinned type", "repo (repos), listed by repo_list", "none"]]);
    expect(changeRows(after, after)).toEqual([]);
  });

  // Each case is a change of marks the counts alone would hide, and the
  // Tool marks row that names each tool whose mark moved.
  const marks: [string, Record<string, string | { argument: string }>, Record<string, string | { argument: string }>, [string, string]][] = [
    ["a renamed argument", { pr_create: { argument: "repo" } }, { pr_create: { argument: "repository" } }, ["pr_create names a repo in repo", "pr_create names a repo in repository"]],
    ["a swap of marks with equal counts", { pr_create: { argument: "repo" }, repo_create: "all" }, { pr_create: "all", repo_create: { argument: "repo" } },
      ["pr_create names a repo in repo, repo_create reaches every repo", "pr_create reaches every repo, repo_create names a repo in repo"]],
    ["a tool marked none for the first time", {}, { get_me: "none" }, ["get_me reaches every repo", "get_me reaches none"]],
  ];
  it.each(marks)("names each tool whose mark moved for %s", (_case, was, now, want) => {
    const pinned = (tools: Record<string, string | { argument: string }>): ManifestDoc => ({ straza: { objects: { type: "repo", plural: "repos", list: { tool: "repo_list" }, tools } } });
    const rows = changeRows(pinned(was), pinned(now));
    expect(rows.find((r) => r[0] === "Tool marks")).toEqual(["Tool marks", ...want]);
  });

  it("counts a change of the tool marks or the listing tool alone as a change of the server", () => {
    const pinned = (tools: Record<string, string | { argument: string }>, list = "repo_list"): ManifestDoc => ({ straza: { objects: { type: "repo", plural: "repos", list: { tool: list }, tools } } });
    const was = pinned({ pr_create: { argument: "repo" }, pr_view: { argument: "repo" }, repo_create: "all" });
    const now = pinned({ pr_create: { argument: "repo" }, pr_view: { argument: "repo" }, repo_create: "all", get_me: "none", search_code: "credential" });
    expect(changeRows(was, now)).toEqual([
      ["Pinned type", "repo (repos), listed by repo_list. 2 tools name a repo, 1 reaches every repo.", "repo (repos), listed by repo_list. 2 tools name a repo, 1 reaches every repo, 1 relies on a login, 1 reaches none."],
      ["Tool marks", "get_me reaches every repo, search_code reaches every repo", "get_me reaches none, search_code relies on a login"],
    ]);
    expect(changeRows(was, pinned({ pr_create: { argument: "repo" }, pr_view: { argument: "repo" }, repo_create: "all" }, "repo_search"))[0][2]).toBe(
      "repo (repos), listed by repo_search. 2 tools name a repo, 1 reaches every repo.",
    );
    expect(changeRows(pinned({ issue_view: { argument: "id" } }), pinned({}))[0][1]).toBe("repo (repos), listed by repo_list. 1 tool names a repo.");
  });
});
