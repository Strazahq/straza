import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PolicyPage } from "./policy-page";
import { TooltipProvider } from "@/components/ui/tooltip";
import {
  ApiError, type AuditRow, type PolicyDoc, type RoleRow, activatePolicy, applyPolicy, deactivatePolicy, deletePolicy, validatePolicy,
  eventSupport, getPolicy, listApps, listAudit, listPolicies, listRoles, listSessions, listUsers, simulate,
} from "@/lib/api";
import { checkDraft, createDraft, listDrafts, publishDraft } from "@/lib/drafts-api";
import { navigate, setLeaveGuard } from "@/lib/router";
import { SAVE_PUBLISH } from "@/lib/save-words";
import { downloadText } from "@/lib/utils";
import {
  ADD_RULE, APPLY_TO_PAGE, BUCKET_WORD, CHECK_RULE, CLOSE_SHEET, DELETE, DELETE_LIVE_TITLE, DISCARD, DRAFT, EVERYONE, FACT, LEAVE, LEAVE_TITLE,
  LIVE, MENU, MISSING_TITLE, MORE, NEED, NOT_PARSED_PAGE, NO_DECISIONS, ON_BODY, OPEN_IN_AUDIT, PUBLISH_DRAFT_NOTE, REASON,
  EDITED, RULE_ID, SAVE, STAY, TURN_OFF, TURN_ON, WHERE_CHOICE, onTitle, openRule, stillNeeded,
} from "@/lib/policy-words";
import { SUMMARY, VERDICT } from "@/test/drafts-fixture";

const repo = (rel: string) => fileURLToPath(new URL("../../../../" + rel, import.meta.url));
const seed = readFileSync(repo("web/ui/src/test/testdata/dev-guardrails.yaml"), "utf8");

// The draft in these suites has one rule that tickets and one that denies,
// so the two outcomes and the draft-only Delete are both exercised.
const draftText = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: release-window
  description: Deploys during the release window need a security ticket.
spec:
  priority: 300
  match:
    roles: [dev-tools, sre-tools]
  rules:
    - id: release-deploy-ticket
      tools: [shell.exec]
      command:
        denyPatterns: ["./deploy*"]
      effect: allow
      mode: approve
      approve:
        roles: [sec-approvers]
        class: ticket
      reason: "Straza: deploys in the release window need a security ticket"
    - id: release-no-force-push
      tools: [shell.exec]
      command:
        denyPatterns: ["git push --force*"]
      effect: deny
      reason: "Straza: no force push in the release window"
`;

// One rule the cards never edit, for the sheet that reads it out instead.
const checkedText = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: release-window
spec:
  rules:
    - id: checked-first
      tools: [mcp.call]
      apps: [ledger-mcp]
      toolNames:
        allow: [post_journal]
      effect: allow
      mode: serverCheck
`;

const liveSet: PolicyDoc = {
  id: "p1",
  name: "dev-guardrails",
  status: "active",
  priority: 150,
  drift: false,
  updated_at: new Date(Date.now() - 3600_000).toISOString(),
  yaml: seed,
  summary: { name: "dev-guardrails", rules: 11, priority: 150, matchRoles: ["dev-demo-tools", "dev-midpoint"], capture: "verbatim", postures: { deny: 2, hold: 4, ticket: 1, allow: 4 } },
};

const draftSet: PolicyDoc = {
  id: "p2",
  name: "release-window",
  status: "draft",
  priority: 300,
  updated_at: new Date(Date.now() - 7200_000).toISOString(),
  yaml: draftText,
  summary: { name: "release-window", rules: 2, priority: 300, matchRoles: ["dev-tools", "sre-tools"], postures: { deny: 1, ticket: 1 } },
};

const roles: RoleRow[] = [
  { id: "r1", name: "dev-demo-tools", kind: "application", holder_count: 2 },
  { id: "r4", name: "dev-midpoint", kind: "application", holder_count: 2 },
  { id: "r2", name: "sre-tools", kind: "application", holder_count: 3 },
  { id: "r3", name: "sec-approvers", kind: "approver", holder_count: 2 },
];

const ce = (data: Record<string, unknown>, type = "straza.audit.tool") => JSON.stringify({ type, time: new Date().toISOString(), data });
const records: AuditRow[] = [
  { seq: 42, ce: ce({ effect: "deny", ruleId: "no-rm-rf", setName: "dev-guardrails", tool: "shell.exec", command: "rm -rf build/", reason: "Straza: destructive command blocked", session: "s1", user: "joe" }), username: "joe" },
  { seq: 41, ce: ce({ effect: "approve", ruleId: "dev-mcp-env-ticket", setName: "dev-guardrails", tool: "mcp.call", app: "demo-tools", toolName: "get-env", session: "s1", user: "joe" }, "straza.audit.mcp"), username: "joe" },
];

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  getPolicy: vi.fn(),
  listRoles: vi.fn(),
  listApps: vi.fn(),
  eventSupport: vi.fn(),
  applyPolicy: vi.fn(),
  activatePolicy: vi.fn(),
  deactivatePolicy: vi.fn(),
  deletePolicy: vi.fn(),
  validatePolicy: vi.fn(),
  listAudit: vi.fn(),
  listPolicies: vi.fn(),
  listSessions: vi.fn(),
  listUsers: vi.fn(),
  simulate: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn(), setLeaveGuard: vi.fn() }));
// Save and publish runs through the drafts: a check, a draft of its own and
// its publish. The origin line reads the drafts list.
// A full grant reads every draft, so the origin line names the saved edit.
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), adminAreas: () => null }));
vi.mock("@/lib/drafts-api", async (orig) => ({ ...(await orig<typeof import("@/lib/drafts-api")>()), checkDraft: vi.fn(), createDraft: vi.fn(), publishDraft: vi.fn(), listDrafts: vi.fn() }));
vi.mock("@/lib/utils", async (orig) => ({ ...(await orig<typeof import("@/lib/utils")>()), downloadText: vi.fn() }));

const CLEAN = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const DRAFT71 = { id: "71", revision: 1, state: "open" as const, door: "console" as const, authors: [], items: [], title: "Change approval set dev-guardrails", created_at: "", updated_at: "" };

const mount = (name = "dev-guardrails", tab = "rules") =>
  render(<TooltipProvider><PolicyPage name={name} tab={tab} /></TooltipProvider>);

const head = () => document.querySelector("[data-page-head]") as HTMLElement;
const fact = (key: string) => document.querySelector('[data-fact="' + key + '"]') as HTMLElement;
const rows = (bucket: string) => document.querySelectorAll('[data-group="' + bucket + '"]');
const bar = () => document.querySelector("[data-unpublished]") as HTMLElement | null;
const row = (id: string) => screen.getByRole("button", { name: openRule(id) });
const sheet = () => document.querySelector("[data-rule-sheet]") as HTMLElement;

const openMore = async () => {
  await userEvent.click(screen.getByRole("button", { name: MORE }));
  return screen.findByRole("menu");
};

// The shortest edit the page takes: the Applies to sheet, ticked to
// Everyone, which puts one unpublished change on the bar.
const editApplies = async () => {
  await waitFor(() => expect(fact("applies")).toBeTruthy());
  await userEvent.click(within(fact("applies")).getByRole("button", { name: FACT.change }));
  const box = await screen.findByRole("dialog");
  await userEvent.click(within(box).getByRole("checkbox", { name: EVERYONE }));
  await userEvent.click(within(box).getByRole("button", { name: APPLY_TO_PAGE }));
  await waitFor(() => expect(bar()).toBeTruthy());
};

beforeEach(() => {
  vi.mocked(getPolicy).mockResolvedValue(liveSet);
  vi.mocked(listRoles).mockResolvedValue(roles);
  vi.mocked(listApps).mockResolvedValue([]);
  vi.mocked(eventSupport).mockResolvedValue({ events: [] });
  vi.mocked(applyPolicy).mockResolvedValue(liveSet);
  vi.mocked(activatePolicy).mockResolvedValue({});
  vi.mocked(deactivatePolicy).mockResolvedValue({});
  vi.mocked(deletePolicy).mockResolvedValue({ status: "deleted" });
  vi.mocked(validatePolicy).mockResolvedValue({ ok: true });
  vi.mocked(listAudit).mockResolvedValue(records);
  vi.mocked(listPolicies).mockResolvedValue({ items: [] });
  vi.mocked(listSessions).mockResolvedValue({ items: [], next_cursor: "" });
  vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
  vi.mocked(simulate).mockResolvedValue({ active: { effect: "allow" }, subject: {}, snapshot: "7c1e9a2b" });
  vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: CLEAN });
  vi.mocked(createDraft).mockResolvedValue({ draft: DRAFT71, verdict: CLEAN });
  vi.mocked(publishDraft).mockResolvedValue({ draft: { ...DRAFT71, state: "published" }, snapshot: "3be0a1ff", servers: [], next: [] });
  vi.mocked(listDrafts).mockResolvedValue({ items: [], next_cursor: "" });
  vi.mocked(navigate).mockClear();
  vi.mocked(downloadText).mockClear();
});

describe("a policy's page, as it reads", () => {
  it("names the policy, its state and the stored counts", async () => {
    mount();
    await screen.findByRole("heading", { name: "dev-guardrails" });
    expect(within(head()).getByText(LIVE)).toBeTruthy();
    expect(head().querySelector("[data-rec]")).toBeTruthy();
    const meta = (head().querySelector("[data-meta]") as HTMLElement).textContent || "";
    expect(meta).toContain("11 rules: 2 denied, 5 need approval, 4 allowed");
    expect(meta).toContain("updated");
    // Applies to and priority live in the facts row, so the head says
    // neither twice.
    expect(meta).not.toContain("priority");
    expect(meta).not.toContain("dev-demo-tools");
  });

  it("marks a live policy a draft edits, and names that draft on the origin line", async () => {
    vi.mocked(getPolicy).mockResolvedValue({ ...liveSet, drift: true });
    vi.mocked(listDrafts).mockImplementation(async (q: string) => ({ items: q.includes("state=open") ? [{ ...SUMMARY, id: "70", policy_edit: "dev-guardrails", items: [{ kind: "PolicySet" as const, name: "dev-guardrails", op: "put" as const }] }] : [], next_cursor: "" }));
    mount();
    await screen.findByRole("heading", { name: "dev-guardrails" });
    expect(head().querySelector("[data-drift]")?.textContent).toBe(EDITED);
    await waitFor(() => expect(document.querySelector("[data-origin]")?.textContent).toBe("Draft 70 changes dev-guardrails and waits under Drafts.Review draft 70"));
    expect(listDrafts).toHaveBeenCalledWith("state=open&object=PolicySet%2Fdev-guardrails&limit=20");
  });

  it("opens the Rules tab with the three facts", async () => {
    mount();
    await waitFor(() => expect(fact("applies")).toBeTruthy());
    expect(fact("applies").textContent).toContain("dev-demo-tools");
    expect(fact("applies").textContent).toContain("dev-midpoint");
    expect(fact("recording").textContent).toContain("word for word");
    expect(fact("priority").textContent).toContain("150");
  });

  it("puts every rule on the table under the outcome it produces", async () => {
    mount();
    await waitFor(() => expect(rows("deny").length).toBe(2));
    expect(rows("hum").length).toBe(5);
    expect(rows("allow").length).toBe(4);
    expect(document.querySelector("[data-rules]")?.getAttribute("data-rules")).toBe("11");
  });

  it("leads an MCP rule's row with its server and its tools", async () => {
    mount();
    const line = await waitFor(() => row("dev-mcp-env-ticket"));
    const text = line.textContent || "";
    expect(text).toContain("demo-tools");
    expect(text).toContain("get-env");
    expect(text).toContain(BUCKET_WORD.hum);
    expect(text).toContain("dev-mcp-env-ticket");
  });

  it("leads a shell rule's row with the lane and its patterns", async () => {
    mount();
    const line = await waitFor(() => row("no-rm-rf"));
    const text = line.textContent || "";
    expect(text).toContain("shell");
    expect(text).toContain("rm -rf *");
    expect(text).toContain(BUCKET_WORD.deny);
  });

  it("opens a rule in the sheet and closes it again, with the table in place", async () => {
    mount();
    await waitFor(() => expect(rows("deny").length).toBe(2));
    await userEvent.click(row("no-rm-rf"));
    await screen.findByRole("dialog");
    expect(sheet().getAttribute("data-rule-sheet")).toBe("no-rm-rf");
    expect(document.querySelector('[data-rule-editor="no-rm-rf"]')).toBeTruthy();
    expect(document.querySelector('[data-rule-open="no-rm-rf"]')).toBeTruthy();
    await userEvent.click(within(sheet()).getByRole("button", { name: CLOSE_SHEET }));
    await waitFor(() => expect(document.querySelector("[data-rule-sheet]")).toBeNull());
    expect(rows("deny").length).toBe(2);
  });

  it("sends a rule the cards cannot edit to the text, in the same sheet", async () => {
    vi.mocked(getPolicy).mockResolvedValue({ ...draftSet, yaml: checkedText, summary: { name: "release-window", rules: 1, postures: { serverCheck: 1 } } });
    mount("release-window");
    await waitFor(() => expect(row("checked-first")).toBeTruthy());
    await userEvent.click(row("checked-first"));
    await screen.findByRole("dialog");
    expect(sheet().textContent).toContain(CHECK_RULE);
    expect(document.querySelector("[data-rule-editor]")).toBeNull();
  });

  it("keeps the head on the stored counts while the page has edits", async () => {
    mount();
    await editApplies();
    const meta = (head().querySelector("[data-meta]") as HTMLElement).textContent || "";
    expect(meta).toContain("11 rules: 2 denied, 5 need approval, 4 allowed");
  });
});

describe("adding a rule from the sheet", () => {
  const openNew = async () => {
    await waitFor(() => expect(rows("deny").length).toBe(2));
    await userEvent.click(screen.getByRole("button", { name: ADD_RULE }));
    await screen.findByRole("dialog");
    expect(sheet().getAttribute("data-rule-sheet")).toBe("new");
  };

  it("refuses the rule until where, calls, what happens and a reason are answered", async () => {
    mount();
    await openNew();
    const apply = within(sheet()).getByRole("button", { name: APPLY_TO_PAGE });
    expect(apply.getAttribute("aria-disabled")).toBe("true");
    expect((sheet().querySelector("[data-still-needed]") as HTMLElement).textContent)
      .toBe(stillNeeded([NEED.where, NEED.what]));
    await userEvent.click(apply);
    expect(bar()).toBeNull();

    // Where the call goes decides what else the rule still needs, so the
    // line only asks for the calls once the lane is known.
    await userEvent.click(within(sheet()).getByRole("radio", { name: WHERE_CHOICE.shell }));
    await userEvent.click(within(sheet()).getByRole("button", { name: BUCKET_WORD.deny }));
    await waitFor(() => expect((sheet().querySelector("[data-still-needed]") as HTMLElement).textContent)
      .toBe(stillNeeded([NEED.calls, NEED.reason])));
    expect(bar()).toBeNull();
  });

  it("adds the rule the sheet builds, under the id typed over the proposal", async () => {
    mount();
    await openNew();
    await userEvent.click(within(sheet()).getByRole("radio", { name: WHERE_CHOICE.net }));
    await userEvent.click(within(sheet()).getByRole("button", { name: BUCKET_WORD.deny }));
    const reason = await within(sheet()).findByLabelText(REASON);
    fireEvent.change(reason, { target: { value: "Straza: no outbound fetch from this role" } });
    fireEvent.blur(reason);
    const id = within(sheet()).getByLabelText(RULE_ID) as HTMLInputElement;
    await waitFor(() => expect(id.value).toBe("deny-net"));
    fireEvent.change(id, { target: { value: "no-egress" } });
    await waitFor(() => expect(sheet().querySelector("[data-still-needed]")).toBeNull());
    await userEvent.click(within(sheet()).getByRole("button", { name: APPLY_TO_PAGE }));

    await waitFor(() => expect(bar()).toBeTruthy());
    expect(bar()?.textContent).toContain("no-egress");
    const line = row("no-egress");
    expect(line.getAttribute("data-mark")).toBe("new");
    expect(line.textContent).toContain("Straza: no outbound fetch from this role");
    expect(document.querySelector("[data-rule-sheet]")).toBeNull();
  });
});

describe("saving and publishing a policy", () => {
  it("saves a draft of the text as the set's saved edit, publishing nothing", async () => {
    mount();
    await editApplies();
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: SAVE }));
    await waitFor(() => expect(vi.mocked(applyPolicy).mock.calls.length).toBe(1));
    expect(vi.mocked(validatePolicy)).toHaveBeenCalled();
    expect(vi.mocked(activatePolicy)).not.toHaveBeenCalled();
    expect(createDraft).not.toHaveBeenCalled();
    expect(publishDraft).not.toHaveBeenCalled();
  });

  it("names the stored text in the bar once it differs from the version that runs", async () => {
    vi.mocked(getPolicy).mockResolvedValue({ ...liveSet, drift: true });
    mount();
    await editApplies();
    const count = (bar() as HTMLElement).querySelector("[data-live-postures]");
    expect(count?.getAttribute("data-live-postures")).toBe("stored");
    expect(count?.textContent).toMatch(/^stored, not live:/);
  });

  it("quotes the server's own sentence when a save is refused", async () => {
    vi.mocked(validatePolicy).mockRejectedValue(new ApiError("match.roles names dev, a business role", 400));
    mount();
    await editApplies();
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: SAVE }));
    const block = await waitFor(() => {
      const el = document.querySelector("[data-refused-error]");
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(block.textContent).toContain(SAVE);
    expect(block.textContent).toContain("match.roles names dev, a business role");
    expect(vi.mocked(applyPolicy)).not.toHaveBeenCalled();
  });

  it("publishes a live policy through the dialog as one PolicySet put on a draft of its own", async () => {
    mount();
    await editApplies();
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: SAVE_PUBLISH }));
    await waitFor(() => expect(document.querySelector("[data-publish-dialog]")).toBeTruthy());
    expect(document.querySelector("[data-turn-on-note]")).toBeNull();
    await userEvent.click(document.querySelector("[data-publish]") as HTMLElement);
    await waitFor(() => expect(publishDraft).toHaveBeenCalledWith("71", { revision: 1, risk_digest: "", ticked: [], typed: {} }));
    const items = vi.mocked(createDraft).mock.calls[0][0].items || [];
    expect(items.map((i) => [i.kind, i.name, i.op])).toEqual([["PolicySet", "dev-guardrails", "put"]]);
    expect(items[0].doc).toContain("name: dev-guardrails");
    expect(vi.mocked(createDraft).mock.calls[0][0].working).toBeUndefined();
    expect(vi.mocked(applyPolicy)).not.toHaveBeenCalled();
    expect(vi.mocked(activatePolicy)).not.toHaveBeenCalled();
  });

  it("publishes a policy that is off and turns it on, saying so in the dialog", async () => {
    vi.mocked(getPolicy).mockResolvedValue(draftSet);
    mount("release-window");
    await editApplies();
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: SAVE_PUBLISH }));
    await waitFor(() => expect(document.querySelector("[data-publish-dialog]")).toBeTruthy());
    expect((document.querySelector("[data-turn-on-note]") as HTMLElement).textContent).toBe(PUBLISH_DRAFT_NOTE);
    await userEvent.click(document.querySelector("[data-publish]") as HTMLElement);
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect((vi.mocked(createDraft).mock.calls[0][0].items || [])[0].op).toBe("put");
    expect(vi.mocked(activatePolicy)).not.toHaveBeenCalled();
  });

  it("drops every edit on Discard", async () => {
    mount();
    await editApplies();
    await userEvent.click(screen.getByRole("button", { name: DISCARD }));
    await waitFor(() => expect(bar()).toBeNull());
    expect(fact("applies").textContent).toContain("dev-demo-tools");
    expect(fact("applies").textContent).toContain("dev-midpoint");
  });

  it("sends Show the change to the YAML tab", async () => {
    mount();
    await editApplies();
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: "Show the change" }));
    expect(vi.mocked(navigate)).toHaveBeenCalledWith("policies", ["dev-guardrails", "yaml"], true);
  });
});

describe("the YAML tab and the page it feeds", () => {
  it("shows the stored text with its line numbers", async () => {
    mount("dev-guardrails", "yaml");
    const editor = await screen.findByRole("textbox");
    expect((editor as HTMLTextAreaElement).value).toBe(seed);
    expect(document.querySelector('[aria-label="Line numbers"]')?.textContent?.startsWith("12")).toBe(true);
  });

  it("moves the rows when the text changes, and names the text in the bar", async () => {
    const { rerender } = mount("dev-guardrails", "yaml");
    const editor = await screen.findByRole("textbox");
    fireEvent.change(editor, { target: { value: seed.replace("priority: 150", "priority: 999") } });
    fireEvent.blur(editor);
    await waitFor(() => expect(bar()?.textContent).toContain("text"));
    rerender(<TooltipProvider><PolicyPage name="dev-guardrails" tab="rules" /></TooltipProvider>);
    await waitFor(() => expect(fact("priority").textContent).toContain("999"));
  });

  it("says the text does not parse and stops the publish", async () => {
    const { rerender } = mount("dev-guardrails", "yaml");
    const editor = await screen.findByRole("textbox");
    fireEvent.change(editor, { target: { value: "apiVersion: [unclosed\n" } });
    fireEvent.blur(editor);
    await waitFor(() => expect(bar()).toBeTruthy());
    rerender(<TooltipProvider><PolicyPage name="dev-guardrails" tab="rules" /></TooltipProvider>);
    await waitFor(() => expect(document.querySelector("[data-not-parsed]")).toBeTruthy());
    expect((document.querySelector("[data-not-parsed]") as HTMLElement).textContent).toContain(NOT_PARSED_PAGE);
    const publish = screen.getByRole("button", { name: SAVE_PUBLISH });
    expect(publish.getAttribute("aria-disabled")).toBe("true");
    expect(publish.getAttribute("title")).toBeTruthy();
    await userEvent.click(publish);
    expect(document.querySelector("[data-publish-dialog]")).toBeNull();
    // Add rule cannot act on a text the cards cannot read either, and says
    // the same reason on hover.
    const add = screen.getByRole("button", { name: ADD_RULE });
    expect(add.getAttribute("aria-disabled")).toBe("true");
    expect(add.getAttribute("title")).toBe(publish.getAttribute("title"));
    await userEvent.click(add);
    expect(document.querySelector("[data-rule-sheet]")).toBeNull();
  });
});

describe("turning a policy off and on, and deleting one", () => {
  it("says what stops, then turns it off", async () => {
    mount();
    await waitFor(() => expect(rows("deny").length).toBe(2));
    await openMore();
    await userEvent.click(await screen.findByRole("menuitem", { name: MENU.off }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Its 5 approval gates and 2 denials stop governing the moment you confirm.");
    expect(dialog.textContent).toContain("Recording of dev-demo-tools and dev-midpoint sessions stops.");
    expect(dialog.textContent).toContain("The policy stays stored and reads Off.");
    await userEvent.click(within(dialog).getByRole("button", { name: TURN_OFF }));
    await waitFor(() => expect(vi.mocked(deactivatePolicy)).toHaveBeenCalledWith("dev-guardrails"));
  });

  it("keeps the confirm open and quotes the server when a turn off is refused", async () => {
    vi.mocked(deactivatePolicy).mockRejectedValue(new ApiError("the policy is referenced by an open approval", 409));
    mount();
    await waitFor(() => expect(rows("deny").length).toBe(2));
    await openMore();
    await userEvent.click(await screen.findByRole("menuitem", { name: MENU.off }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: TURN_OFF }));
    await waitFor(() => expect(dialog.textContent).toContain("the policy is referenced by an open approval"));
    expect(screen.getByRole("alertdialog")).toBeTruthy();
  });

  it("says what starts, then turns a draft on", async () => {
    vi.mocked(getPolicy).mockResolvedValue(draftSet);
    mount("release-window");
    await waitFor(() => expect(rows("deny").length).toBe(1));
    await openMore();
    await userEvent.click(await screen.findByRole("menuitem", { name: MENU.on }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain(onTitle("release-window"));
    expect(dialog.textContent).toContain(ON_BODY);
    await userEvent.click(within(dialog).getByRole("button", { name: TURN_ON }));
    await waitFor(() => expect(vi.mocked(activatePolicy)).toHaveBeenCalledWith("release-window"));
  });

  it("keeps the confirm open and quotes the server when a turn on is refused", async () => {
    vi.mocked(getPolicy).mockResolvedValue(draftSet);
    vi.mocked(activatePolicy).mockRejectedValue(new ApiError("match.roles names dev, a business role", 400));
    mount("release-window");
    await waitFor(() => expect(rows("deny").length).toBe(1));
    await openMore();
    await userEvent.click(await screen.findByRole("menuitem", { name: MENU.on }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: TURN_ON }));
    await waitFor(() => expect(dialog.textContent).toContain("match.roles names dev, a business role"));
  });

  it("refuses Delete on a live policy and says why on hover", async () => {
    mount();
    await waitFor(() => expect(rows("deny").length).toBe(2));
    await openMore();
    const item = await screen.findByRole("menuitem", { name: MENU.delete });
    expect(item.getAttribute("aria-disabled")).toBe("true");
    expect(item.getAttribute("title")).toBe(DELETE_LIVE_TITLE);
  });

  it("deletes a draft after the confirm names it", async () => {
    vi.mocked(getPolicy).mockResolvedValue(draftSet);
    mount("release-window");
    await waitFor(() => expect(rows("deny").length).toBe(1));
    expect(within(head()).getByText(DRAFT)).toBeTruthy();
    await openMore();
    await userEvent.click(await screen.findByRole("menuitem", { name: MENU.delete }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Delete release-window?");
    await userEvent.click(within(dialog).getByRole("button", { name: DELETE }));
    await waitFor(() => expect(vi.mocked(deletePolicy)).toHaveBeenCalledWith("release-window"));
    expect(vi.mocked(navigate)).toHaveBeenCalledWith("policies");
  });

  it("hands the browser the stored text to save", async () => {
    mount();
    await waitFor(() => expect(rows("deny").length).toBe(2));
    await openMore();
    await userEvent.click(await screen.findByRole("menuitem", { name: MENU.export }));
    expect(vi.mocked(downloadText)).toHaveBeenCalledWith("dev-guardrails.yaml", seed, "application/yaml");
  });
});

describe("the Decisions tab", () => {
  it("lists what this policy decided, newest first", async () => {
    mount("dev-guardrails", "decisions");
    await waitFor(() => expect(document.querySelectorAll("[data-decision]").length).toBe(2));
    expect(vi.mocked(listAudit).mock.calls[0][0])
      .toBe("q=" + encodeURIComponent('"setName":"dev-guardrails"') + "&limit=100&order=desc");
    const denied = document.querySelector('[data-decision="42"]') as HTMLElement;
    expect(denied.textContent).toContain("joe");
    expect(denied.textContent).toContain("shell: rm -rf build/");
    expect(denied.textContent).toContain("Denied");
    expect(denied.textContent).toContain("no-rm-rf");
    const held = document.querySelector('[data-decision="41"]') as HTMLElement;
    expect(held.textContent).toContain("demo-tools / get-env");
    expect(held.textContent).toContain("Needs approval");
  });

  it("opens the record sheet on a row", async () => {
    mount("dev-guardrails", "decisions");
    await waitFor(() => expect(document.querySelectorAll("[data-decision]").length).toBe(2));
    await userEvent.click(screen.getByRole("button", { name: "Open 42" }));
    expect(await screen.findByRole("dialog")).toBeTruthy();
    expect(document.querySelector('[data-record-sheet="42"]')).toBeTruthy();
  });

  it("says so when nothing has been decided yet", async () => {
    vi.mocked(listAudit).mockResolvedValue([]);
    mount("dev-guardrails", "decisions");
    expect(await screen.findByText(NO_DECISIONS)).toBeTruthy();
  });

  it("says what could not be read and keeps the tab on screen", async () => {
    vi.mocked(listAudit).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount("dev-guardrails", "decisions");
    const block = await waitFor(() => {
      const el = document.querySelector("[data-fetch-error]");
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(block.textContent).toContain("The decisions");
    expect(block.textContent).toContain("strazad did not answer");
  });
});

describe("leaving the page with edits that are not published", () => {
  // The router asks the guard the page registers; the tests call it the
  // way the router would, with the next location and the departure.
  const lastGuard = () => {
    const calls = vi.mocked(setLeaveGuard).mock.calls.filter((c) => typeof c[0] === "function");
    return calls[calls.length - 1][0] as (next: { kind: "route"; key: "audit" | "policies"; rest: string[] }, go: () => void) => void;
  };

  it("registers a guard once an edit lands, asks before a departure, and goes on the answer", async () => {
    mount();
    await waitFor(() => expect(rows("deny").length).toBe(2));
    expect(vi.mocked(setLeaveGuard)).toHaveBeenLastCalledWith(null);
    await editApplies();
    const guard = lastGuard();
    const go = vi.fn();
    guard({ kind: "route", key: "audit", rest: [] }, go);
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain(LEAVE_TITLE);
    await userEvent.click(within(dialog).getByRole("button", { name: STAY }));
    expect(go).not.toHaveBeenCalled();
    guard({ kind: "route", key: "audit", rest: [] }, go);
    await userEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: LEAVE }));
    expect(go).toHaveBeenCalledTimes(1);
  });

  it("lets a tab change under the same policy pass without asking", async () => {
    mount();
    await editApplies();
    const go = vi.fn();
    lastGuard()({ kind: "route", key: "policies", rest: ["dev-guardrails", "yaml"] }, go);
    expect(go).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("goes straight through when nothing is unpublished", async () => {
    mount("dev-guardrails", "decisions");
    await userEvent.click(await screen.findByRole("button", { name: new RegExp(OPEN_IN_AUDIT) }));
    expect(vi.mocked(navigate)).toHaveBeenCalledWith("audit");
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});

describe("a policy that is not there", () => {
  it("says the policy does not exist and offers the list", async () => {
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("not found", 404));
    mount("gone");
    const empty = await waitFor(() => {
      const el = document.querySelector("[data-empty-state]");
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(empty.textContent).toContain(MISSING_TITLE);
    expect(empty.textContent).toContain("No policy is stored as gone.");
    await userEvent.click(screen.getByRole("button", { name: "Open Policies" }));
    expect(vi.mocked(navigate)).toHaveBeenCalledWith("policies");
  });

  it("says what could not be read and what to do next", async () => {
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("HTTP 503", 503));
    mount();
    const block = await waitFor(() => {
      const el = document.querySelector("[data-fetch-error]");
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(block.textContent).toContain("The policy");
    expect(block.textContent).toContain("HTTP 503");
    expect(block.textContent).toContain("Reload to try again.");
  });
});
