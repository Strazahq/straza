import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { type Draft, ReviewStep } from "./role-steps";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type FixedRule, type Plan, defaultShape, emptyPlan, withCall, withChoice, withOwn, withReach } from "@/lib/access-plan";
import type { AppRow, PackRow, ToolRow } from "@/lib/api";

// The Review of New role lists every tool the role reaches with what a call
// to it does. The rules go live with the role in one publish, so it asks
// nothing about them, and it says that packs are bound after the publish.

// The editor is its own suite; the review never draws it.
vi.mock("@/components/access-editor", () => ({ AccessEditor: () => null }));

const apps: AppRow[] = [{ id: "app-1", name: "scout-tools", runtime: "remote", status: "running", reached_by: [] }];
const tools: ToolRow[] = [
  { id: "t1", app: "scout-tools", app_id: "app-1", name: "search", description: "Full-text search over the scout index." },
  { id: "t2", app: "scout-tools", app_id: "app-1", name: "fetch", description: "Fetches one document by id." },
  { id: "t3", app: "scout-tools", app_id: "app-1", name: "summarize", description: "Summarizes a document." },
];
const toolsOf = (app: string) => tools.filter((t) => t.app === app);

const ticked = (...names: string[]): Plan => ({ ...emptyPlan(), picked: Object.fromEntries(names.map((n) => [n, true])) });
const ticket = { ...defaultShape(), pool: "sec-approvers", how: "ticket" as const };

const pack: PackRow = { id: "p-1", name: "onboarding", version: "3" };
const draftOf = (plan: Plan, packs: string[] = []): Draft => ({ kind: "application", name: "scout-role", description: "", server: "scout-tools", plans: { "scout-tools": plan }, composed: [], packs });

function Mounted({ plan, fixed = {}, packs = [] }: { plan: Plan; fixed?: Record<string, FixedRule>; packs?: string[] }) {
  return (
    <TooltipProvider>
      <ReviewStep draft={draftOf(plan, packs)} apps={apps} toolsOf={toolsOf} fixed={fixed} packs={[pack]} hadPacks acts={[]} commands={"strazactl roles create scout-role --kind application"} />
    </TooltipProvider>
  );
}

const band = () => (document.querySelector('[data-server-band="scout-tools"]') as HTMLElement).textContent;
const rows = () => Array.from(document.querySelectorAll("[data-review-tool]")).map((r) => within(r as HTMLElement).getAllByRole("cell").map((c) => c.textContent).join(" | "));

describe("the New role Review", () => {
  // Each case is a plan, the band's count words, and one row per tool the
  // role reaches with its policy word.
  const cases: [string, Plan, string, string[]][] = [
    [
      "lists the ticked tools with their policy words",
      withChoice(ticked("search", "summarize"), "summarize", "approve"),
      "scout-tools2 of 3 tools",
      ["search | Full-text search over the scout index. | allowed", "summarize | Summarizes a document. | needs approval: hold, up to 2 minutes"],
    ],
    [
      "lists every tool under the glob, with no stand-in row",
      withChoice(withReach(emptyPlan(), "later"), "fetch", "deny"),
      "scout-toolsevery tool (3), and tools added later",
      ["search | Full-text search over the scout index. | allowed", "fetch | Fetches one document by id. | denied", "summarize | Summarizes a document. | allowed"],
    ],
    [
      "names each tool's own approval under Require approval for every call",
      withOwn(withCall(withReach(emptyPlan(), "today"), "every"), "fetch", ticket),
      "scout-tools3 of 3 tools",
      [
        "search | Full-text search over the scout index. | needs approval: hold, up to 2 minutes",
        "fetch | Fetches one document by id. | needs approval: ticket within a day, then an hour to run",
        "summarize | Summarizes a document. | needs approval: hold, up to 2 minutes",
      ],
    ],
  ];
  it.each(cases)("%s", (_case, plan, words, want) => {
    render(<Mounted plan={plan} />);
    expect(band()).toBe(words);
    expect(rows()).toEqual(want);
    expect(document.querySelector("[data-every]")).toBeNull();
  });

  it("keeps a tool another policy decides in that policy's words", () => {
    render(<Mounted plan={ticked("search", "fetch")} fixed={{ fetch: { set: "agent-guardrails", word: "needs approval: ticket within a day, then an hour to run", status: "approve_gated" } }} />);
    expect(rows()).toEqual(["search | Full-text search over the scout index. | allowed", "fetch | Fetches one document by id. | needs approval: ticket within a day, then an hour to run (agent-guardrails)"]);
  });

  it("asks nothing about the rules, since they go live with the role", () => {
    render(<Mounted plan={withChoice(ticked("search"), "search", "approve")} />);
    expect(screen.queryByRole("radiogroup", { name: "The rules" })).toBeNull();
    expect(document.querySelector("[data-draft-warning]")).toBeNull();
  });

  it("says the picked packs are bound after the publish, and only when a pack is picked", () => {
    const view = render(<Mounted plan={ticked("search")} />);
    expect(document.querySelector("[data-packs-after]")).toBeNull();
    view.unmount();
    render(<Mounted plan={ticked("search")} packs={["p-1"]} />);
    expect((document.querySelector("[data-packs-row]") as HTMLElement).textContent).toBe("onboarding");
    expect((document.querySelector("[data-packs-after]") as HTMLElement).textContent).toBe("A draft never binds a knowledge pack, so Save and publish binds these once the role is live. After Save draft, bind them on the role's page once the draft is published.");
  });
});
