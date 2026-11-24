import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftChanges } from "./draft-changes";
import { DraftGains } from "./draft-gains";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type DraftDetail, listApps, listRoles } from "@/lib/api";
import { roleToolsOf } from "@/lib/drafts-access";
import { roleOf } from "@/lib/drafts-model";
import {
  SHORT_ALSO,
  NO_GAINS,
  REMOVED_LINE,
  REMOVED_LINE_PAST,
  TOOLS_NOT_READ,
  GAINS_HIDDEN,
  contactedLine,
  notLive,
} from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { COVERAGE, COVERS_DETAIL, DETAIL, PUBLISHED_DETAIL, WRITERS_SET, detailWith, readersSet, role } from "@/test/drafts-fixture";

// What changes and Access after publishing: one card per item with its
// kind, change, View as YAML and the link to its live page, a facts grid
// in the landed words of the server, role and policy pages, a policy set's
// rules as a table, and every text as text.

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), listApps: vi.fn(), listRoles: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));

const card = (object: string) => document.querySelector('[data-item="' + object + '"]') as HTMLElement;
const fact = (object: string, label: string) => card(object).querySelector('[data-fact="' + label + '"]') as HTMLElement;
type Opts = { registered?: string[] | null; holders?: (role: string) => number | null; onYaml?: (object: string) => void; remoteRps?: number | null };
const mount = (d: DraftDetail = DETAIL, o: Opts = {}) =>
  render(<TooltipProvider><DraftChanges detail={d} registered={o.registered ?? null} holders={o.holders || (() => null)} onYaml={o.onYaml || vi.fn()} remoteRps={o.remoteRps === undefined ? 20 : o.remoteRps} /></TooltipProvider>);
const names = (el: Element | null) => [...(el?.querySelectorAll("[data-name]") || [])].map((n) => n.textContent);
const pills = (el: Element | null) => [...(el?.querySelectorAll("span.rounded-pill") || [])].map((p) => p.textContent);

describe("the head of a card", () => {
  beforeEach(() => vi.resetAllMocks());

  it("names the kind and the server that owns a role in pills, then the change", () => {
    mount();
    const head = (object: string) => [...card(object).querySelectorAll("header [data-kind], header [data-owner], header [data-mark]")].map((x) => x.textContent);
    expect(head("App/github")).toEqual(["MCP server", "new"]);
    expect(head("Role/github-readers")).toEqual(["application role", "owned by github", "new"]);
    expect(head("PolicySet/github-writers-access")).toEqual(["policy", "new"]);
    expect(head("Role/secret-role")).toEqual(["role", "changed"]);
    expect(card("App/github").querySelector("h3")?.className).toContain("font-mono");
  });

  it("offers View as YAML on every card with a document, and none on a withheld or removed item", async () => {
    const onYaml = vi.fn();
    mount(DETAIL, { onYaml });
    const yaml = [...document.querySelectorAll("[data-view-yaml]")].map((b) => b.getAttribute("data-view-yaml"));
    expect(yaml).toEqual(["App/github", "App/demo-tools", "Role/github-readers", "Role/demo-tools-readers", "PolicySet/github-writers-access", "PolicySet/demo-tools-readers-access"]);
    await userEvent.click(within(card("Role/github-readers")).getByRole("button", { name: "View as YAML" }));
    expect(onYaml).toHaveBeenCalledWith("Role/github-readers");
  });

  it("links a live object's card to its page, finding a server's or a role's id by name", async () => {
    vi.mocked(listApps).mockResolvedValue([{ id: "app-7", name: "demo-tools" } as AppRow]);
    mount();
    expect(card("App/github").querySelector("[data-open-live]")).toBeNull();
    await userEvent.click(within(card("PolicySet/demo-tools-readers-access")).getByRole("button", { name: "Open demo-tools-readers-access" }));
    expect(navigate).toHaveBeenCalledWith("policies", ["demo-tools-readers-access"]);
    await userEvent.click(within(card("App/demo-tools")).getByRole("button", { name: "Open demo-tools" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("servers", ["app-7"]));
  });

  it("says why a live link cannot open its page", async () => {
    vi.mocked(listRoles).mockResolvedValue([]);
    vi.mocked(listApps).mockRejectedValue(new ApiError("strazad did not answer", 0, true));
    mount();
    await userEvent.click(within(card("Role/demo-tools-readers")).getByRole("button", { name: "Open demo-tools-readers" }));
    await waitFor(() => expect(notify.failed).toHaveBeenCalledWith(notLive("demo-tools-readers")));
    await userEvent.click(within(card("App/demo-tools")).getByRole("button", { name: "Open demo-tools" }));
    await waitFor(() => expect(notify.failed).toHaveBeenCalledWith("The server list could not be read because Straza did not answer. Check that it is running, then reload."));
    expect(navigate).not.toHaveBeenCalled();
  });

  it("lets the buttons drop under the name below the sm width", () => {
    mount();
    const buttons = (card("App/demo-tools").querySelector("[data-view-yaml]") as HTMLElement).parentElement as HTMLElement;
    expect(buttons.className).toContain("w-full");
    expect(buttons.className).toContain("sm:w-auto");
  });
});

describe("a server's card", () => {
  it("reads a new remote server in the server page's words, with its tools not read yet", () => {
    mount();
    expect(fact("App/github", "Address").textContent).toBe("Addresshttps://api.githubcopilot.com/mcp/");
    expect(fact("App/github", "Tools").textContent).toBe("Tools" + TOOLS_NOT_READ);
    expect(fact("App/github", "Rate limit").textContent).toBe("Rate limit20 calls per second, per session, the default.");
  });

  it("says the remote default unnamed when the config did not name it", () => {
    mount(DETAIL, { remoteRps: null });
    expect(fact("App/github", "Rate limit").textContent).toBe("Rate limitThe default for a remote server.");
  });

  it("reads the tools a contact found for the document the draft holds now", () => {
    mount(detailWith({ contacted: { "App/github": { at: "2026-09-24T10:47:00Z", tools: ["get_me", "list_issues"] } } }));
    expect(names(fact("App/github", "Tools"))).toEqual(["get_me", "list_issues"]);
    expect(card("App/github").textContent).toContain(contactedLine("api.githubcopilot.com", "", 2).split(" at ")[0]);
  });

  it("reads a changed server's fields as now, then after publishing, with no note under them", () => {
    mount();
    const row = fact("App/demo-tools", "Rate limit");
    expect([row.querySelector("[data-was]")?.textContent, row.querySelector("[data-now]")?.textContent]).toEqual(["10 calls per second, per session.", "20 calls per second, per session."]);
    expect(card("App/demo-tools").querySelector("[data-no-other]")).toBeNull();
  });

  it("lists what a removal takes along", () => {
    mount();
    const c = card("App/old-tools");
    expect(c.querySelector("[data-mark]")?.textContent).toBe("removed");
    expect(c.textContent).toContain(REMOVED_LINE);
    expect(c.textContent).toContain(SHORT_ALSO.removed[0]);
    expect(c.textContent).toContain("the role old-tools-readers");
  });

  it("shows a document's text as text, never as a link or an image", () => {
    const doc = DETAIL.draft.items[0].doc!.replace("GitHub's remote MCP server", '<a href="https://evil.example">x</a><img src="https://evil.example/p.png">');
    mount(detailWith({ draft: { items: [{ ...DETAIL.draft.items[0], doc }] } }));
    expect(document.querySelector("[data-item] a, [data-item] img")).toBeNull();
    expect(card("App/github").textContent).toContain('<a href="https://evil.example">x</a>');
  });
});

describe("a role's card", () => {
  it("says who holds a role where the page knows, and nothing where it does not", () => {
    mount(DETAIL, { holders: (r) => (r === "github-readers" ? 0 : r === "demo-tools-readers" ? 1 : null) });
    expect(fact("Role/github-readers", "Holders").textContent).toBe("Holdersnobody yet");
    expect(fact("Role/demo-tools-readers", "Holders").textContent).toBe("Holders1 holder");
    expect(fact("App/github", "Holders")).toBeNull();
  });

  it("puts a new role's tools behind Straza under the outcome the gain rows tell, and the rest plain", () => {
    mount();
    const behind = fact("Role/github-readers", "Behind Straza");
    expect(behind.querySelector('[data-tag="github"]')).not.toBeNull();
    expect(pills(behind)).toContain("2 tools");
    const allowed = behind.querySelector('[data-tool-line="allowed"]') as HTMLElement;
    expect(allowed.querySelector('[data-outcome="allowed"]')?.textContent).toBe("allowed");
    expect(within(allowed).getByText("get_me").getAttribute("title")).toBe("get_me: allowed");
    expect((behind.querySelector('[data-tool-line="plain"]') as HTMLElement).textContent).toBe("list_issues");
  });

  it("says a role with no access row reaches no server", () => {
    const doc = "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n  name: audio-gen\nspec:\n  kind: application\n  bindings: []\n";
    mount(detailWith({ draft: { items: [{ kind: "Role", name: "audio-gen", op: "put", doc, existed: false }] }, live: {} }));
    expect(fact("Role/audio-gen", "Behind Straza").textContent).toBe("Behind Strazano server");
  });

  describe("a business role, which reaches only through the roles it implies", () => {
    const doc = "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n  name: invoice-tools\nspec:\n  kind: business\n  implies: [objects-demo-readers]\n";
    const item = { kind: "Role" as const, name: "invoice-tools", op: "put" as const, doc, existed: false };
    const gain = (tool: string, after: "runs" | "needs-approval") => ({ role: "invoice-tools", server: "objects-demo", tool, holders_count: 0, before: "not-reachable" as const, after });

    it("reads its servers and tools behind Straza from the gain rows of an open draft", () => {
      mount(detailWith({ draft: { items: [item] }, verdict: { gains: [gain("issue_create", "needs-approval"), gain("repo_read", "runs"), gain("whoami", "runs")] }, live: {} }));
      const behind = fact("Role/invoice-tools", "Behind Straza");
      expect(behind.textContent).not.toContain("no server");
      expect(behind.querySelector('[data-behind="objects-demo"] [data-tag="objects-demo"]')).not.toBeNull();
      expect(pills(behind)).toEqual(["3 tools"]);
      expect((behind.querySelector('[data-tool-line="approval"]') as HTMLElement).textContent).toBe("needs approvalissue_create");
      expect((behind.querySelector('[data-tool-line="allowed"]') as HTMLElement).textContent).toBe("allowedrepo_readwhoami");
      expect(fact("Role/invoice-tools", "Implies").textContent).toBe("Impliesobjects-demo-readers");
    });

    it.each([
      ["an open draft whose gain rows name none of its tools", detailWith({ draft: { items: [item] }, verdict: { gains: [] }, live: {} })],
      ["a published draft, whose card gets no gain rows", detailWith({
        draft: { state: "published", items: [item] },
        changes: [{ kind: "Role", name: "invoice-tools", implied: false, before_op: "remove", before_doc: "", before_fp: "", after_op: "put", after_doc: doc, after_fp: "a" }],
        live: {},
      })],
    ])("leaves Behind Straza out on %s, and lets the Implies row stand", (_, d) => {
      mount(d);
      expect(fact("Role/invoice-tools", "Behind Straza")).toBeNull();
      expect(card("Role/invoice-tools").textContent).not.toContain("no server");
      expect(fact("Role/invoice-tools", "Implies").textContent).toBe("Impliesobjects-demo-readers");
    });

    it("never says a changed business role reaches no server", () => {
      const was = doc.replace("[objects-demo-readers]", "[objects-demo-readers, github-readers]");
      mount(detailWith({ draft: { items: [{ ...item, existed: true }] }, live: { "Role/invoice-tools": { op: "put", doc: was } } }));
      expect(fact("Role/invoice-tools", "Behind Straza")).toBeNull();
      expect(fact("Role/invoice-tools", "Implies").querySelector("[data-now]")?.textContent).toBe("objects-demo-readers");
    });
  });

  it("shows five tools a line and opens the rest in place", async () => {
    const tools = ["a1", "a2", "a3", "a4", "a5", "a6", "a7"];
    mount(detailWith({ draft: { items: [{ kind: "Role", name: "wide", op: "put", doc: role("wide", "demo-tools", tools), existed: false }] }, live: {} }));
    const line = () => fact("Role/wide", "Behind Straza").querySelector('[data-tool-line="plain"]') as HTMLElement;
    expect(line().textContent).toBe("a1a2a3a4a5+2 more");
    await userEvent.click(within(line()).getByRole("button", { name: "+2 more" }));
    expect(line().textContent).toBe(tools.join("") + "Show fewer");
  });

  it("keeps a changed role's tools as one row with the one that leaves struck", () => {
    mount();
    const tools = fact("Role/demo-tools-readers", "Tools");
    expect(names(tools)).toEqual(["get-env", "echo", "get-sum"]);
    const leaving = tools.querySelector('[data-name="get-env"]') as HTMLElement;
    expect([leaving.tagName, leaving.hasAttribute("data-removed"), leaving.className.includes("line-through")]).toEqual(["DEL", true, true]);
    expect(card("Role/demo-tools-readers").querySelector("table")).toBeNull();
  });

  it("folds the tools a changed role keeps once there are more than four, and opens them on click", async () => {
    const kept = ["a1", "a2", "a3", "a4", "a5"];
    mount(detailWith({ draft: { items: [{ kind: "Role", name: "wide", op: "put", doc: role("wide", "demo-tools", [...kept, "b1"]), existed: true }] }, live: { "Role/wide": { op: "put", doc: role("wide", "demo-tools", [...kept, "c1"]) } } }));
    const tools = fact("Role/wide", "Tools");
    const shown = () => [...tools.querySelectorAll("[data-name]")].map((n) => n.getAttribute("data-name"));
    expect(shown()).toEqual(["b1", "c1"]);
    expect(tools.querySelector("[data-added]")?.textContent).toBe("+ b1");
    await userEvent.click(within(tools).getByRole("button", { name: "5 unchanged" }));
    expect(shown()).toEqual(["b1", "c1", ...kept]);
  });

  it("shows a changed role's description as now, then after publishing", () => {
    const doc = role("demo-tools-readers", "demo-tools", ["echo", "get-sum"]).replace("spec:\n", "spec:\n    description: Reads the demo tools.\n");
    mount(detailWith({ draft: { items: [{ ...DETAIL.draft.items[3], doc }] } }));
    const row = fact("Role/demo-tools-readers", "Description");
    expect([row.querySelector("[data-was]")?.textContent, row.querySelector("[data-now]")?.textContent]).toEqual(["nothing", "Reads the demo tools."]);
  });

  it("names the policy rules of the draft's sets that name the role", () => {
    mount();
    expect(fact("Role/demo-tools-readers", "Policy rule").textContent).toBe("Policy ruleget-sumindemo-tools-readers-access");
    expect(fact("Role/github-readers", "Policy rule")).toBeNull();
  });

  it("names a server on a person's machine the draft's rules open to a role, once the page can tell", () => {
    const set = WRITERS_SET.replaceAll("github-writers", "github-readers").replace("apps: [github]", "apps: [elevenlabs]");
    const d = detailWith({ draft: { items: [DETAIL.draft.items[2], { kind: "PolicySet", name: "github-readers-access", op: "put", doc: set, existed: false }] }, live: {} });
    mount(d, { registered: ["github"] });
    const machine = fact("Role/github-readers", "Person's machine");
    expect(machine.querySelector('[data-outcome="approval"]')).not.toBeNull();
    expect(machine.querySelector('[data-tag="elevenlabs"]')).not.toBeNull();
    expect(pills(machine)).toEqual(["1 tool"]);
    expect(within(machine).getByText("create_pull_request").getAttribute("title")).toBe("create_pull_request: needs approval");
  });

  it("leaves the row out where the page cannot tell a server on a person's machine apart", () => {
    const set = WRITERS_SET.replaceAll("github-writers", "github-readers").replace("apps: [github]", "apps: [elevenlabs]");
    mount(detailWith({ draft: { items: [DETAIL.draft.items[2], { kind: "PolicySet", name: "github-readers-access", op: "put", doc: set, existed: false }] }, live: {} }));
    expect(fact("Role/github-readers", "Person's machine")).toBeNull();
  });

  it("says why it leaves out a document the reader may not read", () => {
    mount();
    expect(card("Role/secret-role").textContent).toContain(DETAIL.draft.items[7].withheld as string);
  });

  it("reads the role a document names", () => {
    expect(roleOf(DETAIL.draft.items[2].doc)?.server).toBe("github");
  });
});

describe("a policy set's card", () => {
  it("says who a new set applies to and that it records nothing, then lists every policy rule", () => {
    mount();
    expect(fact("PolicySet/github-writers-access", "Applies to").querySelector('[data-tag="github-writers"]')).not.toBeNull();
    expect(fact("PolicySet/github-writers-access", "Recording").textContent).toBe("Recordingno");
    const table = card("PolicySet/github-writers-access").querySelector("table") as HTMLElement;
    expect([...table.querySelectorAll("th")].map((th) => th.textContent)).toEqual(["Policy rule", "Calls", "Outcome"]);
    const cells = [...(table.querySelector('[data-rule="github-pr-hold"]') as HTMLElement).querySelectorAll("td")].map((td) => td.textContent);
    expect(cells).toEqual(["github-pr-hold", "githubcreate_pull_request", "needs approval"]);
    expect(table.getAttribute("style")).toContain("min-width");
    expect((table.parentElement as HTMLElement).className).toContain("overflow-x-auto");
  });

  it("labels its tables in sentence case", () => {
    mount();
    expect(document.querySelectorAll("th").length).toBeGreaterThan(0);
    for (const th of document.querySelectorAll("th")) expect(th.className).not.toContain("uppercase");
  });

  it("says a set with no match applies to everyone", () => {
    mount(detailWith({ draft: { items: [{ kind: "PolicySet", name: "floor", op: "put", doc: WRITERS_SET.replace("  match:\n    roles: [github-writers]\n", ""), existed: false }] }, live: {} }));
    expect(fact("PolicySet/floor", "Applies to").textContent).toBe("Applies toeveryone");
  });

  it("shows a changed set's edited rule one line per field, its old and its new value, and a removed rule", () => {
    mount();
    const table = card("PolicySet/demo-tools-readers-access").querySelector("table") as HTMLElement;
    expect([...table.querySelectorAll("th")].map((th) => th.textContent)).toEqual(["Policy rule", "What changes", "Now", "After publishing"]);
    const sum = table.querySelector('[data-rule="get-sum"]') as HTMLElement;
    expect(sum.getAttribute("data-aspect")).toBe("outcome");
    expect([...sum.querySelectorAll("td")].map((td) => td.textContent)).toEqual([
      "get-sum", "Outcome", "needs approvalthe agent's sponsor2 minutes", "needs approvalsec-approvers5 minutes",
    ]);
    const env = [...(table.querySelector('[data-rule="get-env"]') as HTMLElement).querySelectorAll("td")].map((td) => td.textContent);
    expect(env).toEqual(["get-env", "removed", "needs approvaldemo-toolsget-env", ""]);
  });

  it("shows the patterns of a rule outside the MCP lane on demand, and folds the rules a set keeps", async () => {
    const live = "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: agent-guardrails\nspec:\n  rules:\n    - id: curl-needs-yes\n      tools: [shell.exec]\n      command:\n        denyPatterns: ['re:(sudo\\s+)?curl\\s.*']\n      effect: deny\n      reason: No curl.\n    - id: deny-rm-rf\n      tools: [shell.exec]\n      command:\n        denyPatterns: ['rm -rf *']\n      effect: deny\n      reason: No.\n";
    const next = live.replace("effect: deny\n      reason: No curl.", "effect: allow\n      mode: approve\n      reason: No curl.").replace("denyPatterns: ['re:", "allowPatterns: ['re:");
    mount(detailWith({ draft: { items: [{ kind: "PolicySet", name: "agent-guardrails", op: "put", doc: next, existed: true }] }, live: { "PolicySet/agent-guardrails": { op: "put", doc: live } } }));
    const c = card("PolicySet/agent-guardrails");
    expect([...c.querySelectorAll('[data-rule="curl-needs-yes"]')].map((r) => r.getAttribute("data-aspect"))).toEqual(["outcome"]);
    expect(fact("PolicySet/agent-guardrails", "Applies to").textContent).toBe("Applies toeveryone");
    await userEvent.click(within(c).getByRole("button", { name: "Show the pattern" }));
    expect([...c.querySelectorAll("[data-pattern]")].map((p) => p.textContent)).toEqual(["re:(sudo\\s+)?curl\\s.*"]);
    expect(within(c).getByRole("button", { name: "Hide the pattern" })).toBeTruthy();
    const fold = c.querySelector("[data-unchanged-rules]") as HTMLDetailsElement;
    expect([fold.open, fold.querySelector("summary")?.textContent]).toEqual([false, "1 policy rule unchanged"]);
    expect(fold.querySelector('[data-unchanged-rule="deny-rm-rf"]')?.textContent).toBe("deny-rm-rfdenied");
  });

  it("says a set turned off stays stored, and a removed set is removed", () => {
    mount(detailWith({ draft: { items: [{ kind: "PolicySet", name: "demo-tools-readers-access", op: "off", doc: readersSet(true), existed: true }, { kind: "PolicySet", name: "github-writers-access", op: "remove", existed: true }] }, live: { "PolicySet/github-writers-access": { op: "put", doc: WRITERS_SET } } }));
    const off = card("PolicySet/demo-tools-readers-access");
    expect(off.querySelector("[data-mark]")?.textContent).toBe("turned off");
    expect(pills(off.querySelector('[data-fact="After publishing"]'))).toEqual(["off", "stays stored"]);
    expect(pills(card("PolicySet/github-writers-access").querySelector('[data-fact="After publishing"]'))).toEqual(["removed"]);
  });

  it("draws a server on a person's machine with its own icon only once the page can tell", () => {
    const set = WRITERS_SET.replace("apps: [github]", "apps: [elevenlabs]");
    const d = detailWith({ draft: { items: [{ kind: "PolicySet", name: "github-writers-access", op: "put", doc: set, existed: false }] }, live: {} });
    const icon = () => (card("PolicySet/github-writers-access").querySelector('[data-tag="elevenlabs"] svg') as SVGElement).getAttribute("class") || "";
    const { unmount } = mount(d);
    expect(icon()).toContain("server");
    expect(icon()).not.toContain("laptop");
    unmount();
    mount(d, { registered: ["github"] });
    expect(icon()).toContain("laptop");
  });
});

describe("what a published draft changed", () => {
  // The change record, never live state: live moved on to 30 calls per
  // second after the publish, and the cards must still read 10 to 20.
  it("reads each item from the change record", () => {
    mount(PUBLISHED_DETAIL);
    const row = fact("App/demo-tools", "Rate limit");
    expect([row.querySelector("[data-was]")?.textContent, row.querySelector("[data-now]")?.textContent]).toEqual(["10 calls per second, per session.", "20 calls per second, per session."]);
    expect(card("App/demo-tools").textContent).not.toContain("30 calls");
  });

  it("marks the removed role and lists what the removal took along", () => {
    mount(PUBLISHED_DETAIL);
    const c = card("Role/old-readers");
    expect(c.querySelector("[data-mark]")?.textContent).toBe("removed");
    expect(c.textContent).toContain(REMOVED_LINE_PAST);
    expect(c.textContent).toContain(SHORT_ALSO.removed[1]);
    expect(c.textContent).toContain("the policy old-readers-access");
  });

  it("keeps the edited and the removed rules of the set, under the past tense columns", () => {
    mount(PUBLISHED_DETAIL);
    const set = card("PolicySet/demo-tools-readers-access");
    expect(set.querySelector("[data-mark]")?.textContent).toBe("changed");
    expect([...set.querySelectorAll("th")].map((th) => th.textContent)).toEqual(["Policy rule", "What changed", "Before", "After this publish"]);
    expect(set.querySelector('[data-rule="get-sum"]')?.getAttribute("data-rule-mark")).toBe("edited");
    expect(set.querySelector('[data-rule="get-env"]')?.getAttribute("data-rule-mark")).toBe("removed");
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
