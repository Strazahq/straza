import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NewPolicy } from "./new-policy";
import { TooltipProvider } from "@/components/ui/tooltip";
import {
  type AppRow, type BindingRow, type PolicySetRow, type RoleRow, type ToolRow,
  activatePolicy, applyPolicy, catalogPreview, getPolicy, listApps, listBindings, listPolicies, listRoles, listSessions, listTools, listUsers, simulate, validatePolicy,
} from "@/lib/api";
import { put, take } from "@/lib/handoff";
import {
  CALLS_Q, HOW_HOLD, HOW_Q, HOW_TICKET, INTENT, NAME_BAD, NAME_FREE, NAME_TAKEN, NEXT, REASON, RECORDING_NOTE, REVIEW_NAME,
  RUNS_TODAY, SERVER, STEPS, WHAT_Q, WHO_MISSING, WHO_Q, WHO_ROLE, WHO_SPONSOR, WHO_TEAM, WHOLE_SERVER, W1, alreadyToday, pickedCount,
  publishTitle, reasonApprove, removePick, roleCount, searchRoles, serversNarrowed, unitWord, validWords, VALID_NOW, addToExisting,
  existingSet, PICK_ALL_TITLE, WRITE_YAML, WRITE_YAML_START,
} from "@/lib/policy-words";
import { navigate } from "@/lib/router";
import { checkDraft, createDraft } from "@/lib/drafts-api";
import { SAVE_DRAFT, SAVE_PUBLISH } from "@/lib/save-words";
import { VERDICT } from "@/test/drafts-fixture";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listRoles: vi.fn(),
  listApps: vi.fn(),
  listTools: vi.fn(),
  listBindings: vi.fn(),
  listPolicies: vi.fn(),
  listUsers: vi.fn(),
  listSessions: vi.fn(),
  catalogPreview: vi.fn(),
  validatePolicy: vi.fn(),
  applyPolicy: vi.fn(),
  activatePolicy: vi.fn(),
  getPolicy: vi.fn(),
  simulate: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
// A new set's Save draft joins the working draft through the drafts.
vi.mock("@/lib/drafts-api", async (orig) => ({ ...(await orig<typeof import("@/lib/drafts-api")>()), checkDraft: vi.fn(), createDraft: vi.fn() }));

const roles: RoleRow[] = [
  { id: "r1", name: "dev-tools", kind: "application", description: "The developer seat's tools", holder_count: 2 },
  { id: "r2", name: "sre-tools", kind: "application", description: "Operations tools for the on-call", holder_count: 3 },
  { id: "r3", name: "finance-tools", kind: "application", description: "The ledger agents' tools", holder_count: 1 },
  { id: "r4", name: "auditor-tools", kind: "application", description: "Read lane for the auditors", holder_count: 0 },
  { id: "r5", name: "dev", kind: "business", description: "The developer seat" },
  { id: "r6", name: "sec-approvers", kind: "approver", holder_count: 2 },
];

const app = (name: string): AppRow => ({ id: name, name, runtime: "remote", status: "running", reached_by: [] });
const apps: AppRow[] = ["demo-tools", "ledger-mcp", "midpoint", "ops-mcp", "scout-tools", "straza"].map(app);

const tool = (app: string, name: string): ToolRow => ({ id: app + "/" + name, app, app_id: app, name });
const tools: ToolRow[] = ["echo", "get-sum", "get-env", "write-file", "delete-file", "list-files", "read-file"].map((t) => tool("demo-tools", t));

const bindings: BindingRow[] = ["demo-tools", "midpoint", "scout-tools", "straza"].map((a) => ({ id: "b-" + a, app: a, role: "dev-tools", tools: ["*"] }));

const CLEAN = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };

const sets: PolicySetRow[] = [
  { name: "org-baseline", status: "active", summary: { matchRoles: [], postures: { deny: 3 } } },
  { name: "dev-tools-access", status: "active", summary: { matchRoles: ["dev-tools"], postures: { deny: 1, hold: 1 } } },
  { name: "dev-guardrails", status: "active", summary: { matchRoles: ["dev-tools"], postures: { deny: 2, hold: 4, ticket: 2, allow: 4 } } },
];

const preview = {
  entries: [
    { app: "demo-tools", tool: "get-sum", status: "approve_gated", reason: "", setName: "dev-guardrails" },
    { app: "demo-tools", tool: "get-env", status: "approve_gated", reason: "", setName: "dev-guardrails" },
    ...["echo", "write-file", "delete-file", "list-files", "read-file"].map((t) => ({ app: "demo-tools", tool: t, status: "visible", reason: "" })),
  ],
};

const STORED = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: dev-tools-access
spec:
  priority: 200
  match:
    roles: [dev-tools]
  rules:
    # the gate the role page wrote
    - id: deny-scout-tools-export
      tools: [mcp.call]
      apps: [scout-tools]
      toolNames:
        deny: [export]
      effect: deny
`;

// WRITTEN is the document the example stores: write-file and
// delete-file on demo-tools, held for the sponsor, for dev-tools.
const WRITTEN = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: dev-tools-approvals
  description: write-file and delete-file on demo-tools need approval by the person behind the agent within 2 minutes.
spec:
  priority: 150
  match:
    roles: [dev-tools]
  rules:
    - id: approve-demo-tools-write-file-delete-file
      tools: [mcp.call]
      apps: [demo-tools]
      toolNames:
        allow: [write-file, delete-file]
      effect: allow
      mode: approve
      approve:
        deciders: [sponsor]
        timeoutSeconds: 120
      reason: "Straza: write-file and delete-file on demo-tools need approval"
`;

const SUBJECT = "write-file and delete-file on demo-tools";

const mount = () => render(<TooltipProvider><NewPolicy /></TooltipProvider>);
const click = (name: string) => userEvent.click(screen.getByRole("button", { name }));
const pick = (name: string) => userEvent.click(screen.getByRole("radio", { name }));
const tick = (name: string) => userEvent.click(screen.getByRole("checkbox", { name }));
const heading = (name: string) => screen.findByRole("heading", { level: 2, name });
const foot = () => within(document.querySelector("[data-wizard-foot]") as HTMLElement);
const stepLabels = () =>
  within(screen.getByRole("list", { name: "wizard steps" }))
    .getAllByRole("listitem")
    .map((li) => (li.textContent || "").replace(/^\d/, ""));
const attr = (selector: string, name: string) => (document.querySelector(selector) as HTMLElement).getAttribute(name);
const textOf = (selector: string) => (document.querySelector(selector) as HTMLElement).textContent || "";
const yaml = () => textOf("[data-policy-yaml]");

// toCalls answers What and Who the way the journey does and lands on
// Which calls with demo-tools open.
async function toCalls() {
  mount();
  await heading(WHAT_Q);
  await click(NEXT);
  await pick(WHO_ROLE);
  await pick("dev-tools");
  await click(NEXT);
  await heading(CALLS_Q);
  await screen.findByRole("checkbox", { name: "write-file" });
}

// toReview picks the two tools of the example and walks on to the
// review.
async function toReview() {
  await toCalls();
  await tick("write-file");
  await tick("delete-file");
  await click(NEXT);
  await heading(HOW_Q);
  await click(NEXT);
  await screen.findByRole("textbox", { name: REVIEW_NAME });
}

describe("the New policy wizard", () => {
  beforeEach(() => {
    vi.mocked(listRoles).mockResolvedValue(roles);
    vi.mocked(listApps).mockResolvedValue(apps);
    vi.mocked(listTools).mockResolvedValue(tools);
    vi.mocked(listBindings).mockResolvedValue(bindings);
    vi.mocked(listPolicies).mockResolvedValue({ items: sets });
    vi.mocked(listUsers).mockResolvedValue({ items: [{ username: "joe" }, { username: "agent-sam" }] as never, next_cursor: "" });
    vi.mocked(listSessions).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(catalogPreview).mockResolvedValue(preview as never);
    vi.mocked(validatePolicy).mockResolvedValue({ ok: true, rules: 1, matchRoles: ["dev-tools"] });
    vi.mocked(applyPolicy).mockResolvedValue({ name: "dev-tools-approvals", status: "draft" });
    vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "active", yaml: STORED });
    vi.mocked(simulate).mockResolvedValue({ active: { effect: "allow" }, draft: { effect: "approve" }, subject: {}, snapshot: "7c1e9a2b" });
    vi.mocked(navigate).mockClear();
    vi.mocked(applyPolicy).mockClear();
    vi.mocked(activatePolicy).mockClear();
    vi.mocked(checkDraft).mockReset().mockResolvedValue({ items: [], verdict: CLEAN });
    vi.mocked(createDraft).mockReset().mockResolvedValue({ draft: { id: "39", revision: 2, state: "open", door: "console", authors: [], items: [], title: "", created_at: "", updated_at: "" }, verdict: CLEAN });
    take("policies-new");
  });

  it("names the five steps, and leaves How out for Allow", async () => {
    mount();
    await heading(WHAT_Q);
    expect(stepLabels()).toEqual([STEPS.what, STEPS.who, STEPS.calls, STEPS.how, STEPS.review]);
    await pick(INTENT.allow.title);
    expect(stepLabels()).toEqual([STEPS.what, STEPS.who, STEPS.calls, STEPS.review]);
  });

  it("opens on Which calls when a door named the role, the intent and the lane", async () => {
    put("policies-new", { role: "dev-tools", intent: "deny", lane: "shell" });
    mount();
    await heading(CALLS_Q);
    expect(screen.getByRole("button", { name: "Shell command" }).getAttribute("aria-pressed")).toBe("true");
    await click("Back");
    await heading(WHO_Q);
    expect(screen.getByRole("radio", { name: "dev-tools" }).getAttribute("aria-checked")).toBe("true");
  });

  it("offers the three intents, the recording note and the YAML door", async () => {
    mount();
    await heading(WHAT_Q);
    for (const intent of [INTENT.approve, INTENT.deny, INTENT.allow]) expect(screen.getByRole("radio", { name: intent.title })).toBeTruthy();
    expect(screen.getByText(RECORDING_NOTE, { exact: false })).toBeTruthy();
    expect(screen.getByRole("button", { name: WRITE_YAML })).toBeTruthy();
  });

  it("saves a written text for a new set to the working draft under the name it gives", async () => {
    mount();
    await heading(WHAT_Q);
    await click(WRITE_YAML);
    await userEvent.click(await screen.findByRole("button", { name: SAVE_DRAFT }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["39"]));
    expect(createDraft).toHaveBeenCalledWith({ items: [{ kind: "PolicySet", name: "my-policy", op: "put", doc: WRITE_YAML_START }], working: true });
    expect(applyPolicy).not.toHaveBeenCalled();
  });

  it("asks for Everyone or a role, and says what is missing when neither is picked", async () => {
    mount();
    await heading(WHAT_Q);
    await click(NEXT);
    await heading(WHO_Q);
    await click(NEXT);
    expect(foot().getByText(WHO_MISSING)).toBeTruthy();
    await pick(WHO_ROLE);
    const card = screen.getByRole("radio", { name: "dev-tools" });
    expect(card.textContent).toContain("2 holders");
    expect(card.textContent).toContain("demo-tools");
    // Four roles need no search box.
    expect(screen.queryByRole("textbox", { name: searchRoles(4) })).toBeNull();
  });

  it("searches the roles from nine of them and caps the cards at twelve", async () => {
    const many = Array.from({ length: 14 }, (_, i) => ({ id: "m" + i, name: "team-" + String(i).padStart(2, "0"), kind: "application", holder_count: 1 }));
    vi.mocked(listRoles).mockResolvedValue(many as RoleRow[]);
    mount();
    await heading(WHAT_Q);
    await click(NEXT);
    await pick(WHO_ROLE);
    const search = await screen.findByRole("textbox", { name: searchRoles(14) });
    expect(textOf("[data-role-count]")).toBe(roleCount(12, 14));
    await userEvent.type(search, "team-13");
    await waitFor(() => expect(textOf("[data-role-count]")).toBe(roleCount(1, 14)));
    await pick("team-13");
    await userEvent.clear(search);
    expect(textOf("[data-role-count]")).toBe(roleCount(12, 14));
    expect(textOf("[data-selected-role]")).toContain("team-13");
    expect(within(screen.getByRole("radiogroup", { name: WHO_ROLE })).getAllByRole("radio")[0].getAttribute("aria-label")).toBe("team-00");
    await userEvent.type(search, "team-13");
    expect(screen.getByRole("radio", { name: "team-13" }).getAttribute("aria-checked")).toBe("true");
    expect(document.querySelector("[data-selected-role]")).toBeNull();
  });

  it("keeps the role cards in order when selecting by mouse and arrow key", async () => {
    mount();
    await heading(WHAT_Q);
    await click(NEXT);
    await pick(WHO_ROLE);
    const group = within(screen.getByRole("radiogroup", { name: WHO_ROLE }));
    const names = () => group.getAllByRole("radio").map((r) => r.getAttribute("aria-label"));
    const before = names();
    const second = group.getAllByRole("radio")[1];
    await userEvent.click(second);
    expect(names()).toEqual(before);
    expect(second.getAttribute("aria-checked")).toBe("true");
    expect(document.activeElement).toBe(second);
    const keyboard = userEvent.setup();
    await keyboard.keyboard("{ArrowRight>}");
    await waitFor(() => expect(group.getAllByRole("radio")[2].getAttribute("aria-checked")).toBe("true"));
    await keyboard.keyboard("{/ArrowRight}");
    expect(names()).toEqual(before);
  });

  it("narrows the servers to the role's reach and says what each tool does today", async () => {
    await toCalls();
    expect(textOf("[data-narrowed]")).toContain(serversNarrowed("dev-tools", 4, 6));
    expect(screen.getByRole("combobox", { name: SERVER }).textContent).toContain("demo-tools");
    await waitFor(() => expect(textOf('[data-today="get-sum"]')).toBe(alreadyToday("needs approval", "dev-guardrails")));
    expect(textOf('[data-today="write-file"]')).toBe(RUNS_TODAY);
  });

  it("picks every tool from the header box, and drops one from its chip", async () => {
    await toCalls();
    await tick(PICK_ALL_TITLE);
    expect(textOf("[data-picked-count]")).toBe(pickedCount(7, 7));
    expect(screen.getAllByRole("checkbox").every((box) => (box as HTMLInputElement).checked)).toBe(true);
    await click(removePick("write-file"));
    expect(textOf("[data-picked-count]")).toBe(pickedCount(6, 7));
    expect(document.querySelector('[data-pick="write-file"]')).toBeNull();
    expect(document.querySelector('[data-pick="delete-file"]')).toBeTruthy();
    expect((screen.getByRole("checkbox", { name: PICK_ALL_TITLE }) as HTMLInputElement).indeterminate).toBe(true);
    await tick(PICK_ALL_TITLE);
    await tick(PICK_ALL_TITLE);
    expect(screen.getAllByRole("checkbox").every((box) => !(box as HTMLInputElement).checked)).toBe(true);
    expect((screen.getByRole("checkbox", { name: PICK_ALL_TITLE }) as HTMLInputElement).indeterminate).toBe(false);
  });

  it("shows every tool included by Whole server and restores explicit picks when turned off", async () => {
    await toCalls();
    await tick("write-file");
    await userEvent.click(screen.getByRole("switch", { name: WHOLE_SERVER }));
    for (const box of screen.getAllByRole("checkbox") as HTMLInputElement[]) {
      expect(box.checked).toBe(true);
      expect(box.disabled).toBe(true);
      expect(box.indeterminate).toBe(false);
    }
    expect(textOf("[data-picked-count]")).toBe("All tools, including tools added later.");
    expect(screen.queryByRole("button", { name: removePick("write-file") })).toBeNull();
    await userEvent.click(screen.getByRole("switch", { name: WHOLE_SERVER }));
    expect(textOf("[data-picked-count]")).toBe(pickedCount(1, 7));
    for (const t of tools) {
      const box = screen.getByRole("checkbox", { name: t.name }) as HTMLInputElement;
      expect(box.checked).toBe(t.name === "write-file");
      expect(box.disabled).toBe(false);
    }
    expect((screen.getByRole("checkbox", { name: PICK_ALL_TITLE }) as HTMLInputElement).indeterminate).toBe(true);
  });

  it("keeps Whole server unrestricted by today's tool names in the policy document", async () => {
    await toCalls();
    await tick("write-file");
    await userEvent.click(screen.getByRole("switch", { name: WHOLE_SERVER }));
    await click(NEXT);
    await heading(HOW_Q);
    await click(NEXT);
    await screen.findByRole("textbox", { name: REVIEW_NAME });
    expect(yaml()).toContain("apps: [demo-tools]");
    expect(yaml()).not.toContain("toolNames:");
    expect(yaml()).not.toContain("write-file");
  });

  it("opens How on the sponsor and two minutes, with the reason already written", async () => {
    await toCalls();
    await tick("write-file");
    await tick("delete-file");
    await click(NEXT);
    await heading(HOW_Q);
    expect(screen.getByRole("radio", { name: WHO_SPONSOR }).getAttribute("aria-checked")).toBe("true");
    expect((screen.getByRole("spinbutton", { name: HOW_HOLD }) as HTMLInputElement).value).toBe("2");
    expect(screen.getByRole("combobox", { name: HOW_HOLD + " unit" }).textContent).toContain("minutes");
    expect((screen.getByRole("textbox", { name: REASON }) as HTMLInputElement).value).toBe(reasonApprove(SUBJECT, false));
  });

  it.each([
    ["one tool, whose name keeps its case", false, "write-file on demo-tools will need approval. Say who decides, and for how long."],
    ["the whole server, which opens with a word", true, "Every tool on demo-tools will need approval. Say who decides, and for how long."],
  ])("opens the How step's lede on %s", async (_, whole, lede) => {
    await toCalls();
    await tick("write-file");
    if (whole) await userEvent.click(screen.getByRole("switch", { name: WHOLE_SERVER }));
    await click(NEXT);
    await heading(HOW_Q);
    expect(screen.getByText(lede)).toBeTruthy();
  });

  it("reads a window of one in the singular", async () => {
    await toCalls();
    await tick("write-file");
    await click(NEXT);
    await heading(HOW_Q);
    await pick(HOW_TICKET);
    expect(screen.getByRole("combobox", { name: HOW_TICKET + " unit" }).textContent).toBe(unitWord(1, "days"));
  });

  it("warns when an approver team is paged on a hold under two minutes", async () => {
    await toCalls();
    await tick("write-file");
    await click(NEXT);
    await heading(HOW_Q);
    await pick(WHO_TEAM);
    const box = screen.getByRole("spinbutton", { name: HOW_HOLD });
    await userEvent.clear(box);
    await userEvent.type(box, "90");
    await userEvent.click(screen.getByRole("combobox", { name: HOW_HOLD + " unit" }));
    await userEvent.click(await screen.findByRole("option", { name: "seconds" }));
    await waitFor(() => expect(textOf("[data-w1]")).toBe(W1(2, 90)));
  });

  it("suggests the name and checks it against the stored ones", async () => {
    await toReview();
    const box = screen.getByRole("textbox", { name: REVIEW_NAME }) as HTMLInputElement;
    expect(box.value).toBe("dev-tools-approvals");
    expect(screen.getByText(NAME_FREE)).toBeTruthy();

    await userEvent.clear(box);
    await userEvent.type(box, "dev-guardrails");
    await waitFor(() => expect(attr("[data-name-state]", "data-name-state")).toBe("taken"));
    expect(screen.getByText(NAME_TAKEN)).toBeTruthy();

    await userEvent.clear(box);
    await userEvent.type(box, "Bad Name");
    await waitFor(() => expect(attr("[data-name-state]", "data-name-state")).toBe("bad"));
    expect(screen.getByText(NAME_BAD)).toBeTruthy();
  });

  it("writes the document the journey stores", async () => {
    await toReview();
    await waitFor(() => expect(yaml()).toBe(WRITTEN));
  });

  it("offers the set that already matches the role, and adds the rule to it", async () => {
    await toReview();
    expect(textOf("[data-existing]")).toContain(existingSet("dev-tools-access", "dev-tools"));
    await click(addToExisting("dev-tools-access"));
    await waitFor(() => expect(getPolicy).toHaveBeenCalledWith("dev-tools-access"));
    await waitFor(() => expect(yaml()).toContain("# the gate the role page wrote"));
    expect(yaml()).toContain("- id: approve-demo-tools-write-file-delete-file");
    expect(yaml()).toContain("name: dev-tools-access");
  });

  it("asks the server to check the document and reads its verdict back", async () => {
    await toReview();
    await waitFor(() => expect(validatePolicy).toHaveBeenCalledWith(WRITTEN), { timeout: 2000 });
    await waitFor(() => expect(textOf('[data-verdict="ok"]')).toContain(validWords({ rules: 1, matchRoles: ["dev-tools"] }, VALID_NOW)));
  });

  it("saves a new set to the working draft, stores no set and opens the draft", async () => {
    await toReview();
    await waitFor(() => expect(yaml()).toBe(WRITTEN));
    await click(SAVE_DRAFT);
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["39"]));
    expect(createDraft).toHaveBeenCalledWith({ items: [{ kind: "PolicySet", name: "dev-tools-approvals", op: "put", doc: WRITTEN }], working: true });
    expect(applyPolicy).not.toHaveBeenCalled();
    expect(activatePolicy).not.toHaveBeenCalled();
  });

  it("saves rules added to a set that exists as that set's saved edit, as its page does", async () => {
    await toReview();
    await click(addToExisting("dev-tools-access"));
    await waitFor(() => expect(yaml()).toContain("name: dev-tools-access"));
    await click(SAVE_DRAFT);
    await waitFor(() => expect(applyPolicy).toHaveBeenCalledWith(yaml()));
    expect(createDraft).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith("policies", ["dev-tools-access"]);
  });

  it("opens the publish dialog from the review", async () => {
    await toReview();
    await waitFor(() => expect(yaml()).toBe(WRITTEN));
    await userEvent.click(foot().getByRole("button", { name: SAVE_PUBLISH }));
    expect(await screen.findByText(publishTitle("dev-tools-approvals"))).toBeTruthy();
  });
});
