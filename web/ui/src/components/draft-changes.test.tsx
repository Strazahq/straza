import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { DraftChanges } from "./draft-changes";
import { DraftGains } from "./draft-gains";
import { TooltipProvider } from "@/components/ui/tooltip";
import { roleToolsOf } from "@/lib/drafts-access";
import { changeCounts, roleOf } from "@/lib/drafts-model";
import {
  GOES_WITH,
  LEAVES,
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
} from "@/lib/drafts-words";
import { openDoc, rulesOf } from "@/lib/policy-model";
import { sentence } from "@/lib/policy-words";
import { COVERAGE, COVERS_DETAIL, DETAIL, PUBLISHED_DETAIL, WRITERS_SET, detailWith, readersSet } from "@/test/drafts-fixture";

// What changes and Access after publishing: each
// item in the landed shapes of the server, role and policy pages, the
// reach of each role before and after, and every text as text.

const card = (object: string) => document.querySelector('[data-item="' + object + '"]') as HTMLElement;
const mount = (d = DETAIL) => render(<TooltipProvider><DraftChanges detail={d} /></TooltipProvider>);
const names = (el: Element | null) => [...(el?.querySelectorAll("[data-name]") || [])].map((n) => n.textContent);

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
    expect(names(c.querySelector('[data-fact="Tools"]'))).toEqual(["get_me", "list_issues"]);
    expect(c.textContent).toContain(contactedLine("api.githubcopilot.com", "", 2).split(" at ")[0]);
  });

  it("reads a changed server as the Now and After rows of the change sheets", () => {
    mount();
    const row = card("App/demo-tools").querySelector('[data-row="Rate limit"]') as HTMLElement;
    expect([...row.querySelectorAll("td")].map((td) => td.textContent)).toEqual(["Rate limit", "10 calls per second, per session.", "20 calls per second, per session."]);
  });

  it("reads a new role's tools as chips and a changed role's tools, each that leaves marked", () => {
    mount();
    expect(card("Role/github-readers").querySelector("[data-kind]")?.textContent).toBe("role · owned by github");
    expect(names(card("Role/github-readers").querySelector('[data-fact="Tools"]'))).toEqual(["get_me", "list_issues"]);
    const leaving = card("Role/demo-tools-readers").querySelector('[data-tool="get-env"]') as HTMLElement;
    expect(leaving.textContent).toContain(LEAVES);
    expect(card("Role/demo-tools-readers").querySelector('[data-tool="echo"]')?.textContent).not.toContain(LEAVES);
  });

  it("labels its tables in sentence case", () => {
    mount();
    for (const th of document.querySelectorAll("th")) expect(th.className).not.toContain("uppercase");
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

describe("access after publishing", () => {
  const mountGains = (d = DETAIL) => render(<TooltipProvider><DraftGains gains={d.verdict.gains} objects={d.verdict.objects} toolsOf={roleToolsOf(d)} /></TooltipProvider>);
  const head = (role: string, server?: string) => (document.querySelector('[data-gain-group="' + role + '"]' + (server ? '[data-gain-server="' + server + '"]' : "")) as HTMLElement).textContent;
  const cells = (attr: string, key: string) => [...(document.querySelector("[" + attr + '="' + key + '"]') as HTMLElement).querySelectorAll("td")].map((td) => td.textContent);

  // The fixture's groups sit on github and demo-tools, so each head names
  // its server.
  it("heads each group with who holds the role and what a holder gains or loses, with no badge on a row", () => {
    mountGains();
    expect(head("demo-tools-readers")).toBe("demo-tools-readers on demo-tools · held by sam-sre-agent · loses 1 tool");
    expect(head("github-readers")).toBe("github-readers on github · nobody holds it · gains 1 tool");
    expect(head("github-writers")).toBe("github-writers on github · nobody holds it · gains 1 tool");
    expect([...document.querySelectorAll("th")].map((th) => th.textContent)).toEqual(["Tool", "Now", "After publishing"]);
  });

  // A business role reaches roles on two servers through its closure, so
  // its two groups would read the same without the server.
  it("names the server of each group when the groups span two servers", () => {
    const row = (server: string, tool: string) => ({ role: "engineering", server, tool, holders_count: 3, before: "not-reachable" as const, after: "runs" as const });
    render(<TooltipProvider><DraftGains gains={[row("github", "get_me"), row("jira", "issue_read")]} /></TooltipProvider>);
    expect(head("engineering", "github")).toBe("engineering on github · 3 people · gains 1 tool");
    expect(head("engineering", "jira")).toBe("engineering on jira · 3 people · gains 1 tool");
  });

  // A held role on a pinned server whose row named no ticket now covers
  // one: the draft does not add the role, so it gains the ticket.
  it("reads an existing role that covered no ticket and now covers one as a gain, with its tickets now and after", () => {
    render(<TooltipProvider><DraftGains gains={[]} objects={[{ ...COVERAGE, before: [], after: [{ id: "t2", value: "T-2" }] }]} isNew={() => false} /></TooltipProvider>);
    expect(head("tracker-triage")).toBe("tracker-triage · 1 person · gains 1 ticket: T-2");
    const now = (document.querySelector('[data-gain-objects="tracker-triage"]') as HTMLElement).querySelectorAll("td")[1];
    expect(now.textContent).toBe("nothing");
  });

  it("shows each outcome as a chip in the policy words, and the gate's words once under the table", () => {
    mountGains();
    expect(cells("data-gain", "get_me")).toEqual(["get_me", "no access", "allowed"]);
    expect(cells("data-gain", "create_pull_request")).toEqual(["create_pull_request", "no access", "needs approval"]);
    expect(cells("data-gain", "get-env")).toEqual(["get-env", "needs approval", "no access"]);
    expect(document.querySelector('[data-gain="get_me"] [data-outcome="allowed"]')).not.toBeNull();
    expect(document.querySelector("[data-gains-foot]")?.textContent).toBe(
      "needs approval: a hold, up to 10 minutes, decided by sec-approvers." + "needs approval: a ticket, the person behind the agent.",
    );
  });

  it("folds the tools that keep their outcome, the role's other tools with them", () => {
    mountGains();
    expect(document.querySelector('[data-gain="echo"]')).toBeNull();
    const fold = document.querySelector('[data-gain-same="demo-tools-readers"] details') as HTMLDetailsElement;
    expect(fold.open).toBe(false);
    expect(fold.querySelector("summary")?.textContent).toBe("2 tools, same outcome as now.");
    expect(names(fold)).toEqual(["echo", "get-sum"]);
  });

  it("says the words of a gate that changes on its row, where the outcome stays", () => {
    const gate = { role: "r", server: "s", tool: "deploy", holders_count: 1, before: "needs-approval" as const, after: "needs-approval" as const, before_words: "a ticket", after_words: "a hold" };
    render(<TooltipProvider><DraftGains gains={[gate]} /></TooltipProvider>);
    expect(cells("data-gain", "deploy")).toEqual(["deploy", "needs approvala ticket", "needs approvala hold"]);
  });

  // The covers widening gains no tool, so the group stands on its coverage
  // entry alone, in the server's own nouns.
  it("shows a role that only reaches more tickets, with its tickets now and after and its tools folded", () => {
    mountGains(COVERS_DETAIL);
    expect(head("tracker-triage")).toBe("tracker-triage · 1 person · gains 2 tickets: T-2 and T-3");
    const row = document.querySelector('[data-gain-objects="tracker-triage"]') as HTMLElement;
    expect(row.querySelector("td")?.textContent).toBe("Tickets");
    expect([...row.querySelectorAll("td")].slice(1).map(names)).toEqual([["T-1"], ["T-1", "T-2", "T-3"]]);
    expect((document.querySelector('[data-gain-same="tracker-triage"] summary') as HTMLElement).textContent).toBe("3 tools, same outcome as now.");
    expect(screen.queryByText(NO_GAINS)).toBeNull();
  });

  it("names every ticket for a role widened to all of them", () => {
    mountGains(detailWith({ ...COVERS_DETAIL, verdict: { ...COVERS_DETAIL.verdict, objects: [{ ...COVERAGE, every_after: true, after: [] }] } }));
    expect(head("tracker-triage")).toBe("tracker-triage · 1 person · gains every ticket");
    expect((document.querySelector('[data-gain-objects="tracker-triage"]') as HTMLElement).textContent).toContain("every ticket");
  });

  it("names the tickets of a new role on its group line", () => {
    const fresh = { ...COVERAGE, role: "tracker-new", holders_count: 0, before: [], after: [{ id: "t9", value: "T-9" }] };
    const gain = { role: "tracker-new", server: "tracker", tool: "ticket_read", holders_count: 0, before: "not-reachable" as const, after: "runs" as const };
    render(<TooltipProvider><DraftGains gains={[gain]} objects={[fresh]} isNew={(r) => r === "tracker-new"} /></TooltipProvider>);
    expect(head("tracker-new")).toBe("tracker-new · nobody holds it · on 1 ticket: T-9 · gains 1 tool");
    expect(document.querySelector("[data-gain-objects]")).toBeNull();
  });

  it("adds no group for a coverage entry whose tickets stay", () => {
    render(<TooltipProvider><DraftGains gains={[]} objects={[{ ...COVERAGE, after: COVERAGE.before }]} /></TooltipProvider>);
    expect(document.querySelector("table")).toBeNull();
    expect(screen.getByText(NO_GAINS)).toBeTruthy();
  });

  it("says rows are hidden from this reader instead of claiming no role gains", () => {
    const hidden = "4 rows under Access after publishing are on servers you cannot read, so this view leaves them out. Ask an administrator for the scope apps:read to see them.";
    render(<TooltipProvider><DraftGains gains={[]} hidden={hidden} /></TooltipProvider>);
    const note = document.querySelector("[data-gains-hidden]") as HTMLElement;
    expect(note.textContent).toContain(GAINS_HIDDEN);
    expect(note.textContent).toContain(hidden);
    expect(document.body.textContent).not.toContain(NO_GAINS);
  });

  it("says so when no role gains or loses a tool", () => {
    render(<TooltipProvider><DraftGains gains={[]} /></TooltipProvider>);
    expect(within(document.querySelector("[data-no-gains]") as HTMLElement).getByText(NO_GAINS)).toBeTruthy();
    expect(document.querySelector("table")).toBeNull();
  });
});
