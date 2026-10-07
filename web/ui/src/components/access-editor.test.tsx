import * as React from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AccessEditor } from "./access-editor";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type FixedRule, type Plan, emptyPlan, grantMatchers, planFor } from "@/lib/access-plan";
import type { Unread } from "@/lib/access-read";
import type { AppRow, BindingRow, RoleRow, ToolRow } from "@/lib/api";
import type { Plain } from "@/lib/policy-model";
import { HOW_HOLD, HOW_TICKET, HOW_TICKET_END, W1, WHO_TEAM } from "@/lib/policy-words";
import {
  DENY_NEEDS_LATER, EVERY_NOTE, LATER_LOCKED, NAMES_ONLY, NAME_FIRST, NO_ALLOW_UNDER_EVERY, OTHERS_UNREAD, SET_FOR_TOOL, USE_SERVER_SETTING, newToolsRunWords, policyReadOnly,
  policyUnread, shapeProblemWords,
} from "@/lib/role-words";

const app: AppRow = { id: "app-9", name: "demo-tools", runtime: "http", status: "running", reached_by: [] };
const TOOLS: ToolRow[] = [
  { id: "t1", app: "demo-tools", app_id: "app-9", name: "get-sum", description: "Adds two numbers" },
  { id: "t2", app: "demo-tools", app_id: "app-9", name: "list-files", description: "Lists a directory" },
  { id: "t3", app: "demo-tools", app_id: "app-9", name: "get-env" },
];
const NAMES = TOOLS.map((t) => t.name);

const approvers: RoleRow[] = [
  { id: "r-9", name: "sec-approvers", kind: "approver", holder_count: 2 },
  { id: "r-8", name: "ops-approvers", kind: "approver", holder_count: 9 },
];

type HarnessProps = {
  seed?: Plan;
  stored?: BindingRow | null;
  fixed?: Record<string, FixedRule>;
  readOnly?: boolean;
  namesOnly?: boolean;
  unread?: Unread;
  written?: Plain[] | null;
  unnamed?: boolean;
};

// latest is the plan the harness holds after the last render, so a case
// can read what the editor would hand the commit.
let latest: Plan = emptyPlan();

// Harness holds the plan the way the sheets and the wizard do: the editor
// draws it and hands every change back.
function Harness({ seed, stored = null, fixed = {}, readOnly, namesOnly, unread, written, unnamed }: HarnessProps) {
  const [plan, setPlan] = React.useState<Plan>(() => seed || planFor(stored, NAMES));
  latest = plan;
  return (
    <TooltipProvider>
      <AccessEditor
        role="dev-tools"
        app={app}
        tools={TOOLS}
        plan={plan}
        onPlan={(update) => setPlan(update)}
        fixed={fixed}
        approvers={approvers}
        ownSet="dev-tools-access"
        stored={stored}
        readOnly={readOnly}
        namesOnly={namesOnly}
        unread={unread}
        written={written}
        unnamed={unnamed}
      />
    </TooltipProvider>
  );
}

// ticketShape is a tool's own ticket, a day then an hour.
const ticketShape = { pool: "sponsor", how: "ticket" as const, hold: 120, ticket: 86400, grant: 3600 };

// seeded is a plan that reaches the three tools under the glob.
const seeded = (patch: Partial<Plan>): Plan => ({ ...emptyPlan(), reach: "later", ...patch });

const row = (tool: string) => document.querySelector('[data-tool="' + tool + '"]') as HTMLElement;
const sentence = () => (document.querySelector("[data-grant-sentence]") as HTMLElement).textContent;
const tick = (tool: string) => screen.getByRole("checkbox", { name: "tool " + tool }) as HTMLInputElement;
const card = (name: string) => screen.getByRole("radio", { name });
const choice = (tool: string, name: string) => within(row(tool)).getByRole("radio", { name });
const block = () => within(document.querySelector("[data-approval]") as HTMLElement);
const attr = (selector: string, name: string) => (document.querySelector(selector) as HTMLElement).getAttribute(name);
const textOf = (selector: string) => (document.querySelector(selector) as HTMLElement | null)?.textContent || "";
// ahead says a comes before b in the document.
const ahead = (a: Element, b: Element) => (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;

// rules opens the fold under the editor and reads the rules it shows.
async function rules(title: string): Promise<string> {
  await userEvent.click(screen.getByRole("button", { name: title }));
  return (document.querySelector("pre") as HTMLElement).textContent || "";
}

describe("the access editor", () => {
  it("lists every tool with what it does, and says when the server gave no description", () => {
    render(<Harness />);
    expect(screen.getByRole("columnheader", { name: "Tool" })).toBeTruthy();
    expect(within(row("get-sum")).getAllByRole("cell")[2].textContent).toBe("Adds two numbers");
    expect(within(row("get-env")).getAllByRole("cell")[2].textContent).toBe("The server gave no description.");
    expect(textOf("[data-shown]")).toBe("3 of 3 shown");
  });

  it("narrows the list to the filter and says when nothing matches", async () => {
    render(<Harness />);
    await userEvent.type(screen.getByLabelText("Filter tools"), "get");
    expect(textOf("[data-shown]")).toBe("2 of 3 shown");
    await userEvent.clear(screen.getByLabelText("Filter tools"));
    await userEvent.type(screen.getByLabelText("Filter tools"), "nothing");
    expect(screen.getByText("No tool on this server matches this filter.")).toBeTruthy();
  });

  it("stores the ticked names, every name today, or the glob, one card each", async () => {
    render(<Harness />);
    expect(attr("[data-reach]", "data-reach")).toBe("tick");
    expect(sentence()).toBe("Tick at least one tool: an access row with no tool gives dev-tools nothing on demo-tools.");
    await userEvent.click(within(row("get-sum")).getAllByRole("cell")[1]);
    expect(tick("get-sum").checked).toBe(true);
    expect(grantMatchers(latest, NAMES)).toEqual(["get-sum"]);
    expect(sentence()).toBe("Holders of dev-tools reach 1 of 3 tools of demo-tools. No call needs approval from this role.");

    await userEvent.click(card("Every tool it has today"));
    expect(attr("[data-reach]", "data-reach")).toBe("today");
    expect(NAMES.every((n) => tick(n).checked && tick(n).disabled)).toBe(true);
    expect(grantMatchers(latest, NAMES)).toEqual(["get-env", "get-sum", "list-files"]);
    expect(screen.getByText("All 3 by name. A tool it gains later stays out.")).toBeTruthy();

    await userEvent.click(card("Every tool, and tools added later"));
    expect(grantMatchers(latest, NAMES)).toEqual(["*"]);
    expect(sentence()).toContain("reach every tool of demo-tools, tools added later included.");
  });

  it("answers what a call does with three cards, and moves to per tool when one tool is held", async () => {
    render(<Harness seed={seeded({})} />);
    expect(attr("[data-call]", "data-call")).toBe("allow");
    expect(document.querySelector("[data-approval]")).toBeNull();

    await userEvent.click(choice("get-sum", "require approval"));
    expect(attr("[data-call]", "data-call")).toBe("per");
    expect(choice("get-sum", "require approval").getAttribute("aria-checked")).toBe("true");
    expect(document.querySelector('[data-approval="demo-tools"]')).toBeTruthy();

    await userEvent.click(card("Require approval for every call"));
    expect(latest.call).toBe("every");
    expect(textOf("[data-every-note]")).toBe(EVERY_NOTE);
    expect(NAMES.every((n) => choice(n, "require approval").getAttribute("aria-checked") === "true")).toBe(true);

    await userEvent.click(card("Allow every call"));
    expect(latest.call).toBe("allow");
    expect(document.querySelector("[data-approval]")).toBeNull();
    expect(sentence()).toContain("No call needs approval from this role.");
  });

  it("greys allow under every call with its reason, and keeps the tool held", async () => {
    render(<Harness seed={seeded({ call: "every" })} />);
    const allow = choice("get-sum", "allow");
    expect(allow.getAttribute("aria-disabled")).toBe("true");
    expect(allow.getAttribute("title")).toBe(NO_ALLOW_UNDER_EVERY);
    await userEvent.click(allow);
    expect(choice("get-sum", "require approval").getAttribute("aria-checked")).toBe("true");
    expect(latest.choice["get-sum"]).toBeUndefined();
  });

  it("offers deny only under the glob, and writes it as one deny rule", async () => {
    render(<Harness seed={{ ...emptyPlan(), picked: { "get-sum": true }, call: "per" }} />);
    const deny = choice("get-sum", "deny");
    expect(deny.getAttribute("aria-disabled")).toBe("true");
    expect(deny.getAttribute("title")).toBe(DENY_NEEDS_LATER);
    await userEvent.click(deny);
    expect(choice("get-sum", "allow").getAttribute("aria-checked")).toBe("true");

    await userEvent.click(card("Every tool, and tools added later"));
    expect(choice("get-sum", "deny").getAttribute("aria-disabled")).toBeNull();
    await userEvent.click(choice("get-sum", "deny"));
    expect(choice("get-sum", "deny").getAttribute("aria-checked")).toBe("true");
    const text = await rules("The rule this writes, in dev-tools-access");
    expect(text).toContain("- id: demo-tools-deny");
    expect(text).toContain("deny: [get-sum]");
  });

  it("gives one held tool its own approval, and takes it back", async () => {
    render(<Harness seed={seeded({ call: "per", choice: { "get-sum": "approve", "get-env": "approve" } })} />);
    expect(textOf('[data-shape-line="get-sum"]')).toContain("hold, up to 2 minutes, the person behind the agent");

    await userEvent.click(within(row("get-sum")).getByRole("button", { name: SET_FOR_TOOL }));
    const own = within(document.querySelector('[data-own="get-sum"]') as HTMLElement);
    await userEvent.click(own.getByRole("radio", { name: HOW_TICKET }));
    expect(latest.own["get-sum"].how).toBe("ticket");
    expect(latest.shape.how).toBe("hold");
    expect(sentence()).toContain("get-sum needs a ticket the person behind the agent grants within a day; the same call within an hour runs.");
    const text = await rules("The 2 rules this writes, in dev-tools-access");
    expect(text.indexOf("- id: demo-tools-get-sum-approve")).toBeLessThan(text.indexOf("- id: demo-tools-approve"));

    await userEvent.click(own.getByRole("button", { name: USE_SERVER_SETTING }));
    expect(document.querySelector('[data-own="get-sum"]')).toBeNull();
    expect(latest.own["get-sum"]).toBeUndefined();
    expect(screen.getByRole("button", { name: "The rule this writes, in dev-tools-access" })).toBeTruthy();
    expect(textOf("pre")).not.toContain("demo-tools-get-sum-approve");
  });

  it("writes a ticket with both windows and no timeout", async () => {
    render(<Harness seed={seeded({ call: "every" })} />);
    await userEvent.click(block().getByRole("radio", { name: HOW_TICKET }));
    expect((block().getByRole("spinbutton", { name: HOW_TICKET }) as HTMLInputElement).value).toBe("1");
    expect(block().getByRole("combobox", { name: HOW_TICKET + " unit" }).textContent).toBe("day");
    const grant = block().getByRole("spinbutton", { name: HOW_TICKET_END });
    await userEvent.clear(grant);
    await userEvent.type(grant, "2");
    expect(latest.shape).toMatchObject({ how: "ticket", ticket: 86400, grant: 7200 });
    const text = await rules("The rule this writes, in dev-tools-access");
    expect(text).toContain("- id: demo-tools-every-call-approve");
    expect(text).toContain("class: ticket");
    expect(text).toContain("ticketTTLSeconds: 86400");
    expect(text).toContain("grantTTLSeconds: 7200");
    expect(text).not.toContain("timeoutSeconds");
    expect(text).not.toContain("toolNames");
  });

  it("says under the approval when the server would refuse a window", async () => {
    render(<Harness seed={seeded({ call: "every" })} />);
    expect(document.querySelector("[data-problem]")).toBeNull();
    await userEvent.click(block().getByRole("combobox", { name: HOW_HOLD + " unit" }));
    await userEvent.click(await screen.findByRole("option", { name: "hours" }));
    expect(latest.shape.hold).toBe(7200);
    expect(textOf("[data-problem]")).toBe(shapeProblemWords.hold);
  });

  it("warns that a tool added later runs, only for the glob chosen per tool", async () => {
    render(<Harness seed={seeded({})} />);
    expect(document.querySelector("[data-new-tools]")).toBeNull();
    await userEvent.click(card("Choose per tool"));
    expect(textOf("[data-new-tools]")).toBe(newToolsRunWords("demo-tools"));
    expect(sentence()).toContain("A tool demo-tools gains later runs without approval until someone sets it here.");
    await userEvent.click(card("Only the tools you tick"));
    expect(document.querySelector("[data-new-tools]")).toBeNull();
  });

  it("warns when a hold goes to an approver role with few holders", async () => {
    render(<Harness seed={seeded({ call: "every" })} />);
    expect(document.querySelector("[data-w1]")).toBeNull();
    await userEvent.click(block().getByRole("radio", { name: WHO_TEAM }));
    expect(latest.shape.pool).toBe("sec-approvers");
    expect(textOf("[data-w1]")).toBe(W1(2, 120));
    await userEvent.click(block().getByRole("combobox", { name: WHO_TEAM }));
    await userEvent.click(await screen.findByRole("option", { name: "ops-approvers" }));
    expect(document.querySelector("[data-w1]")).toBeNull();
  });

  it("names the set that decides a fixed tool and offers no choice for it", () => {
    const fixed: Record<string, FixedRule> = {
      "get-sum": { set: "dev-guardrails", word: "", status: "approve_gated" },
      "get-env": { set: "dev-tools-access", ruleId: "env-arg-check", word: "needs approval: ticket within a day, then an hour to run", status: "approve_gated" },
      "list-files": { set: "dev-guardrails", word: "", status: "hidden_policy" },
    };
    render(<Harness seed={seeded({ call: "per" })} fixed={fixed} />);
    expect(textOf('[data-fixed="get-sum"]')).toBe("needs approval (dev-guardrails)");
    expect(textOf('[data-fixed="get-env"]')).toBe("needs approval: ticket within a day, then an hour to run (dev-tools-access)");
    expect(textOf('[data-fixed="list-files"]')).toBe("denied (dev-guardrails)");
    expect(within(row("get-sum")).queryByRole("radio", { name: "allow" })).toBeNull();
    expect(within(row("get-sum")).getByRole("button", { name: "Help: get-sum" })).toBeTruthy();
  });

  it("draws the Policy column as words for a server admin, and keeps the ticks", async () => {
    const seed: Plan = { ...emptyPlan(), picked: { "get-sum": true, "get-env": true }, call: "per", choice: { "get-sum": "approve" } };
    render(<Harness seed={seed} readOnly />);
    expect(screen.queryByRole("radio", { name: "Only the tools you tick" })).toBeNull();
    expect(screen.queryByRole("radio", { name: "Allow every call" })).toBeNull();
    expect(textOf("[data-read-only]")).toBe(policyReadOnly("dev-tools"));
    expect(textOf('[data-policy-word="get-sum"]')).toBe("needs approval: hold, up to 2 minutes");
    expect(textOf('[data-policy-word="get-env"]')).toBe("allowed");
    expect(within(row("list-files")).getAllByRole("cell")[3].textContent).toBe("not reached");
    expect(within(row("get-sum")).queryByRole("radio")).toBeNull();
    expect(document.querySelector("[data-grant-sentence]")).toBeNull();
    await userEvent.click(tick("list-files"));
    expect(latest.picked["list-files"]).toBe(true);
  });

  it("turns off the glob card for a server admin and says why", async () => {
    render(<Harness namesOnly />);
    const later = card("Every tool, and tools added later");
    expect(later.getAttribute("aria-disabled")).toBe("true");
    expect(screen.getByText(NAMES_ONLY)).toBeTruthy();
    await userEvent.click(later);
    expect(latest.reach).toBe("tick");
    expect(attr("[data-reach]", "data-reach")).toBe("tick");
  });

  // A global admin gave the role every tool and tools added later; its
  // server admin sees that on and greyed, with or
  // without the right to publish policy.
  const glob: BindingRow = { id: "b-1", app: "demo-tools", role: "dev-tools", tools: ["*"] };
  it.each([["a server admin who publishes policy", false], ["a server admin who reads policy only", true]])("keeps the glob on and greyed for %s, and a tick taken off narrows it to names", async (_case, readOnly) => {
    render(<Harness namesOnly readOnly={readOnly} stored={glob} />);
    const later = card("Every tool, and tools added later");
    expect(later.getAttribute("aria-checked")).toBe("true");
    expect(later.getAttribute("aria-disabled")).toBe("true");
    expect(later.textContent).toContain(LATER_LOCKED);
    expect(LATER_LOCKED).toBe("Only a global admin turns tools added later on or off. Tick tools below to narrow this role to them.");
    expect(NAMES.map((t) => tick(t).checked)).toEqual([true, true, true]);
    expect(tick("get-sum").disabled).toBe(false);
    await userEvent.click(tick("get-sum"));
    expect(latest.reach).toBe("tick");
    expect(grantMatchers(latest, NAMES)).toEqual(["get-env", "list-files"]);
    expect(card("Every tool, and tools added later").getAttribute("aria-checked")).toBe("false");
    await userEvent.click(card("Every tool, and tools added later"));
    expect(latest.reach).toBe("tick");
    await userEvent.click(tick("get-sum"));
    expect(grantMatchers(latest, NAMES)).toEqual(["get-env", "get-sum", "list-files"]);
  });

  it("leaves the glob card live for a global admin on a role that has it", () => {
    render(<Harness stored={glob} />);
    const later = card("Every tool, and tools added later");
    expect(later.getAttribute("aria-checked")).toBe("true");
    expect(later.getAttribute("aria-disabled")).toBeNull();
    expect(screen.queryByText(LATER_LOCKED)).toBeNull();
    expect(tick("get-sum").disabled).toBe(true);
  });

  it("seeds a stored pattern from the names it matches today and says saving replaces it", () => {
    const stored: BindingRow = { id: "b-2", app: "demo-tools", role: "dev-tools", tools: ["get.*", "list-files"] };
    render(<Harness stored={stored} />);
    expect(tick("list-files").checked).toBe(true);
    expect(tick("get-sum").checked).toBe(false);
    expect(textOf("[data-pattern-note]")).toBe("This access row uses a pattern (get.* and list-files) the console does not edit. Saving replaces it with the tools ticked below.");
  });

  // Each case is the reads that failed and the note above the read-only
  // column that says so.
  const dark: [string, Unread, string][] = [
    ["the own set and the preview", { own: true, others: true }, "You may change which tools dev-tools reaches, but your session cannot read dev-tools-access or the other policies that decide its calls, so the Policy column cannot say what a call does. An administrator who may read policies sees them under Policies."],
    ["the own set", { own: true, others: false }, "You may change which tools dev-tools reaches, but your session cannot read dev-tools-access, the role's own rules, so the Policy column cannot say what a call does. An administrator who may read policies sees them under Policies."],
    ["the preview", { own: false, others: true }, "You may change which tools dev-tools reaches, but your session cannot read the other policies that decide its calls, so the Policy column cannot say what a call does. An administrator who may read policies sees them under Policies."],
  ];
  it.each(dark)("never says what a call does in the read-only column when %s could not be read", (_what, unread, note) => {
    const seed: Plan = { ...emptyPlan(), picked: { "get-sum": true, "get-env": true }, call: "per", choice: { "get-sum": "approve" } };
    const fixed: Record<string, FixedRule> = { "get-env": { set: "dev-guardrails", word: "", status: "hidden_policy" } };
    render(<Harness seed={seed} readOnly unread={unread} fixed={fixed} />);
    expect(textOf("[data-read-only]")).toBe(note);
    expect(policyUnread("dev-tools", "dev-tools-access", unread.own, unread.others)).toBe(note);
    expect(textOf('[data-policy-word="get-sum"]')).toBe("not readable here");
    expect(textOf('[data-fixed="get-env"]')).toBe("denied (dev-guardrails)");
    expect(within(row("list-files")).getAllByRole("cell")[3].textContent).toBe("not reached");
  });

  it("warns an editor whose preview could not be read that its rows show only the role's own rules", () => {
    const { unmount } = render(<Harness seed={seeded({ call: "per" })} unread={{ own: false, others: true }} />);
    expect(textOf("[data-others-unread]")).toBe(OTHERS_UNREAD);
    expect(choice("get-sum", "allow").getAttribute("aria-checked")).toBe("true");
    unmount();
    render(<Harness seed={seeded({ call: "per" })} unread={{ own: false, others: false }} />);
    expect(document.querySelector("[data-others-unread]")).toBeNull();
  });

  it("folds the rules a door says a save stores, with their ids and reasons, instead of fresh ones", async () => {
    const written: Plain[] = [{ id: "sum-hold", tools: ["mcp.call"], apps: ["demo-tools"], toolNames: { allow: ["get-sum"] }, effect: "allow", mode: "approve", approve: { deciders: ["sponsor"], timeoutSeconds: 120, binding: "call" }, reason: "Straza: get-sum is the showcase" }];
    render(<Harness seed={seeded({ call: "per", choice: { "get-sum": "approve" } })} written={written} />);
    const text = await rules("The rule this writes, in dev-tools-access");
    expect(text).toContain("- id: sum-hold");
    expect(text).toContain("binding: call");
    expect(text).toContain('reason: "Straza: get-sum is the showcase"');
    expect(text).not.toContain("demo-tools-approve");
  });

  it("says what is missing while a new role has no name, and draws no rules", () => {
    render(<Harness seed={seeded({ call: "every" })} unnamed />);
    expect(sentence()).toBe(NAME_FIRST);
    expect(screen.queryByRole("button", { name: /rules? this writes/ })).toBeNull();
  });

  it.each([["a named role", false], ["a new role with no name yet", true]])("leads the filter and the tool table with the sentence at body size for %s", (_case, unnamed) => {
    render(<Harness seed={seeded({ call: "every" })} unnamed={unnamed} />);
    const said = document.querySelector("[data-grant-sentence]") as HTMLElement;
    expect(ahead(said, screen.getByLabelText("Filter tools"))).toBe(true);
    expect(ahead(said, document.querySelector("[data-tool-table]") as HTMLElement)).toBe(true);
    expect(said.className.split(" ")).toContain("text-base");
  });

  it("keeps the rules fold under the tool table, which has no scroll of its own", () => {
    render(<Harness seed={seeded({ call: "every" })} />);
    const table = document.querySelector("[data-tool-table]") as HTMLElement;
    expect(ahead(table, screen.getByRole("button", { name: "The rule this writes, in dev-tools-access" }))).toBe(true);
    expect(table.className).not.toMatch(/max-h-|overflow-(y-)?(auto|scroll)/);
  });

  // Each case is a tool, the choice its row holds and the trust tone that
  // fills the chosen segment.
  const tones: [string, string, string, string][] = [
    ["list-files", "allow", "bg-ok-bg", "text-ok"],
    ["get-sum", "require approval", "bg-warn-bg", "text-warn"],
    ["get-env", "deny", "bg-danger-bg", "text-danger"],
  ];
  it.each(tones)("fills only the chosen segment of %s, %s, with its trust tone, and every segment is a control 32 px tall", (tool, name, fill, ink) => {
    render(<Harness seed={seeded({ call: "per", choice: { "get-sum": "approve", "get-env": "deny" } })} />);
    for (const segment of within(row(tool)).getAllByRole("radio")) {
      const classes = segment.className.split(" ");
      const chosen = segment === choice(tool, name);
      expect(segment.getAttribute("aria-checked")).toBe(String(chosen));
      expect(classes).toContain("h-8");
      expect(classes.filter((c) => /^bg-(ok|warn|danger|accent)-bg$/.test(c))).toEqual(chosen ? [fill] : []);
      expect(classes.includes(ink)).toBe(chosen);
    }
  });

  it("tops every cell of a row with one line as tall as the choice control, the name in mono included", () => {
    render(<Harness seed={seeded({ call: "per" })} />);
    for (const cell of within(row("get-sum")).getAllByRole("cell")) expect(cell.className.split(" ")).toEqual(expect.arrayContaining(["align-top", "leading-8"]));
  });

  it("scrolls a tool's own approval into view when it opens, and only then", async () => {
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView");
    render(<Harness seed={seeded({ call: "per", choice: { "get-sum": "approve", "get-env": "approve" }, own: { "get-env": ticketShape } })} />);
    expect(scroll).not.toHaveBeenCalled();
    await userEvent.click(within(row("get-sum")).getByRole("button", { name: SET_FOR_TOOL }));
    expect(scroll).toHaveBeenCalledTimes(1);
    expect(scroll.mock.contexts[0]).toBe(document.querySelector('[data-own="get-sum"]'));
    expect(scroll).toHaveBeenCalledWith({ block: "nearest" });
    await userEvent.click(within(document.querySelector('[data-own="get-sum"]') as HTMLElement).getByRole("radio", { name: HOW_TICKET }));
    expect(scroll).toHaveBeenCalledTimes(1);
    scroll.mockRestore();
  });

  it("carries the help sentence of the Policy column", () => {
    render(<Harness />);
    expect(screen.getByRole("button", { name: "Help: Policy" })).toBeTruthy();
  });
});
