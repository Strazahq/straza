import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { DraftChanges } from "./draft-changes";
import { DraftGains } from "./draft-gains";
import { TooltipProvider } from "@/components/ui/tooltip";
import { changeCounts, roleOf } from "@/lib/drafts-model";
import {
  GAINS_FOOT,
  GOES_WITH,
  LEAVES,
  NOBODY_GAINS,
  NOTHING_STARTS,
  NO_GAINS,
  OFF_LINE,
  REMOVED_LINE,
  REMOVED_LINE_PAST,
  TOOLS_NOT_READ,
  GAINS_HIDDEN,
  changesLede,
  changesLedePublished,
  contactedLine,
  groupLine,
  holdersLose,
  nobodyHolds,
} from "@/lib/drafts-words";
import { openDoc, rulesOf } from "@/lib/policy-model";
import { sentence } from "@/lib/policy-words";
import { DETAIL, PUBLISHED_DETAIL, VERDICT, WRITERS_SET, detailWith, readersSet } from "@/test/drafts-fixture";

// What changes and Who gains what: each
// item in the landed shapes of the server, role and policy pages, the
// reach of each role before and after, and every text as text.

const card = (object: string) => document.querySelector('[data-item="' + object + '"]') as HTMLElement;
const mount = (d = DETAIL) => render(<TooltipProvider><DraftChanges detail={d} /></TooltipProvider>);

describe("what changes", () => {
  it("counts the items in one lede", () => {
    mount();
    expect(screen.getByText(changesLede(changeCounts(DETAIL.draft.items)))).toBeTruthy();
  });

  it("reads a new remote server in the server page's words, with its tools not read yet", () => {
    mount();
    const c = card("App/github");
    expect(c.querySelector("[data-mark]")?.textContent).toBe("new");
    expect(c.querySelector('[data-fact="Address"]')?.textContent).toBe("Addresshttps://api.githubcopilot.com/mcp/");
    expect(c.querySelector('[data-fact="Tools"]')?.textContent).toBe("Tools" + TOOLS_NOT_READ);
    expect(c.textContent).toContain(NOTHING_STARTS);
  });

  it("reads the tools a contact found for the document the draft holds now", () => {
    mount(detailWith({ contacted: { "App/github": { at: "2026-09-24T10:47:00Z", tools: ["get_me", "list_issues"] } } }));
    const c = card("App/github");
    expect(c.querySelector('[data-fact="Tools"]')?.textContent).toContain("get_me, list_issues");
    expect(c.textContent).toContain(contactedLine("api.githubcopilot.com", "", 2).split(" at ")[0]);
  });

  it("reads a changed server as the Now and After rows of the change sheets", () => {
    mount();
    const row = card("App/demo-tools").querySelector('[data-row="Rate limit"]') as HTMLElement;
    expect([...row.querySelectorAll("td")].map((td) => td.textContent)).toEqual(["Rate limit", "10 calls per second, per session.", "20 calls per second, per session."]);
  });

  it("reads a new role's reach and a changed role's tools, each that leaves marked", () => {
    mount();
    expect(card("Role/github-readers").textContent).toContain("Application role, github's own");
    expect(card("Role/github-readers").textContent).toContain("get_me, list_issues");
    const leaving = card("Role/demo-tools-readers").querySelector('[data-tool="get-env"]') as HTMLElement;
    expect(leaving.textContent).toContain(LEAVES);
    expect(card("Role/demo-tools-readers").querySelector('[data-tool="echo"]')?.textContent).not.toContain(LEAVES);
  });

  it("reads an approval set's rules in the policy words, marked against live", () => {
    mount();
    const pr = card("PolicySet/github-writers-access").querySelector('[data-rule="github-pr-hold"]') as HTMLElement;
    expect(pr.textContent).toContain(sentence(rulesOf(openDoc(WRITERS_SET))[0]));
    expect(pr.querySelector("[data-rule-mark]")?.textContent).toBe("new");
    const set = card("PolicySet/demo-tools-readers-access");
    expect(set.querySelector('[data-rule="get-sum"] [data-rule-mark]')?.textContent).toBe("edited");
    expect(set.querySelector('[data-rule="get-env"] [data-rule-mark]')?.textContent).toBe("removed");
  });

  it.each([
    ["PolicySet/github-writers-access", "github-pr-hold", "text-foreground"],
    ["PolicySet/demo-tools-readers-access", "get-env", "line-through"],
  ])("sets what a rule of %s does at body size, beside its name in the mono face", (set, rule, tone) => {
    mount();
    const [name, does] = [...card(set).querySelectorAll('[data-rule="' + rule + '"] td')];
    expect(name.textContent).toBe(rule);
    expect(name.className).toContain("font-mono");
    for (const cls of ["text-base", tone]) expect(does.className).toContain(cls);
  });

  it("says a set turned off stays stored and gates nothing", () => {
    mount(detailWith({ draft: { items: [{ kind: "PolicySet", name: "demo-tools-readers-access", op: "off", doc: readersSet(true), existed: true }] } }));
    const c = card("PolicySet/demo-tools-readers-access");
    expect(c.querySelector("[data-mark]")?.textContent).toBe("turned off");
    expect(c.textContent).toContain(OFF_LINE);
  });

  it("lists what a removal takes along", () => {
    mount();
    const c = card("App/old-tools");
    expect(c.querySelector("[data-mark]")?.textContent).toBe("removed");
    expect(c.textContent).toContain(REMOVED_LINE);
    expect(c.textContent).toContain(GOES_WITH);
    expect(c.textContent).toContain("the role old-tools-readers");
  });

  it("says why it leaves out a document the reader may not read", () => {
    mount();
    expect(card("Role/secret-role").textContent).toContain(DETAIL.draft.items[7].withheld as string);
  });

  it("shows a document's text as text, never as a link or an image", () => {
    const doc = DETAIL.draft.items[0].doc!.replace("GitHub's remote MCP server", '<a href="https://evil.example">x</a><img src="https://evil.example/p.png">');
    mount(detailWith({ draft: { items: [{ ...DETAIL.draft.items[0], doc }] } }));
    expect(document.querySelector("[data-item] a, [data-item] img")).toBeNull();
    expect(card("App/github").textContent).toContain('<a href="https://evil.example">x</a>');
  });

  it("reads the role a document names", () => {
    expect(roleOf(DETAIL.draft.items[2].doc)?.server).toBe("github");
  });
});

describe("what a published draft changed", () => {
  // The change record, never live state: live moved on to 30 calls per
  // second after the publish, and the cards must still read 10 to 20.
  it("reads each item from the change record, in the past tense", () => {
    mount(PUBLISHED_DETAIL);
    expect(screen.getByText(changesLedePublished(changeCounts([
      { kind: "App", op: "put", existed: true },
      { kind: "Role", op: "remove", existed: true },
      { kind: "PolicySet", op: "put", existed: true },
    ])))).toBeTruthy();
    const row = card("App/demo-tools").querySelector('[data-row="Rate limit"]') as HTMLElement;
    expect([...row.querySelectorAll("td")].map((td) => td.textContent)).toEqual(["Rate limit", "10 calls per second, per session.", "20 calls per second, per session."]);
    expect(card("App/demo-tools").textContent).not.toContain("30 calls");
  });

  it("marks the removed role and lists what the removal took along", () => {
    mount(PUBLISHED_DETAIL);
    const c = card("Role/old-readers");
    expect(c.querySelector("[data-mark]")?.textContent).toBe("removed");
    expect(c.textContent).toContain(REMOVED_LINE_PAST);
    expect(c.textContent).toContain(GOES_WITH);
    expect(c.textContent).toContain("the approval set old-readers-access");
  });

  it("keeps the edited and the removed rules of the set", () => {
    mount(PUBLISHED_DETAIL);
    const set = card("PolicySet/demo-tools-readers-access");
    expect(set.querySelector("[data-mark]")?.textContent).toBe("changes");
    expect(set.querySelector('[data-rule="get-sum"] [data-rule-mark]')?.textContent).toBe("edited");
    expect(set.querySelector('[data-rule="get-env"] [data-rule-mark]')?.textContent).toBe("removed");
  });
});

describe("who gains what", () => {
  const mountGains = (gains = DETAIL.verdict.gains) => render(<TooltipProvider><DraftGains gains={gains} /></TooltipProvider>);

  it("says nobody gains when nobody holds the roles, and who loses what they reach today", () => {
    mountGains();
    const lead = document.querySelector("[data-gains-lead]") as HTMLElement;
    expect(lead.textContent).toContain(NOBODY_GAINS);
    expect(lead.textContent).toContain(nobodyHolds(["github-readers", "github-writers"]));
    expect(lead.textContent).toContain(holdersLose(["demo-tools-readers"]));
    expect(screen.getByText(GAINS_FOOT)).toBeTruthy();
  });

  it("groups each role's tools with its holders and marks each row", () => {
    mountGains();
    expect(screen.getByText(groupLine("demo-tools-readers", "demo-tools", ["sam-sre-agent"], 1))).toBeTruthy();
    expect(screen.getByText(groupLine("github-readers", "github", [], 0))).toBeTruthy();
    const cells = (tool: string) => [...(document.querySelector('[data-gain="' + tool + '"]') as HTMLElement).querySelectorAll("td")].map((td) => td.textContent);
    expect(cells("get_me")).toEqual(["get_me", "not reachable", "runs at once", "gains"]);
    expect(cells("create_pull_request")).toEqual(["create_pull_request", "not reachable", "needs approval: a hold, up to 10 minutes, decided by sec-approvers", "gains"]);
    expect(cells("get-env")).toEqual(["get-env", "needs approval: a ticket, the person behind the agent", "not reachable", "loses"]);
    expect(cells("echo")).toEqual(["echo", "runs at once", "runs at once", "same"]);
  });

  it("says rows are hidden from this reader instead of claiming nobody gains", () => {
    const hidden = "4 rows of who gains what are on servers you cannot read, so this view leaves them out. Ask an administrator for the scope apps:read to see them.";
    render(<TooltipProvider><DraftGains gains={[]} hidden={hidden} /></TooltipProvider>);
    const lead = document.querySelector("[data-gains-lead]") as HTMLElement;
    expect(lead.textContent).toContain(GAINS_HIDDEN);
    expect(lead.textContent).toContain(hidden);
    expect(lead.textContent).not.toContain(NO_GAINS);
    expect(lead.textContent).not.toContain(NOBODY_GAINS);
  });

  it("keeps what it can say about the rows it shows when others are hidden", () => {
    render(<TooltipProvider><DraftGains gains={VERDICT.gains} hidden="2 rows are hidden." /></TooltipProvider>);
    const lead = document.querySelector("[data-gains-lead]") as HTMLElement;
    expect(lead.textContent).not.toContain(NOBODY_GAINS);
    expect(lead.textContent).toContain(nobodyHolds(["github-readers", "github-writers"]));
    expect(lead.textContent).toContain(holdersLose(["demo-tools-readers"]));
  });

  it("says so when no role gains or loses a tool", () => {
    mountGains([]);
    expect(within(document.querySelector("[data-gains-lead]") as HTMLElement).getByText(NO_GAINS)).toBeTruthy();
    expect(document.querySelector("table")).toBeNull();
  });
});
