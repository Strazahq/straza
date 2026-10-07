import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PolicyPublish, changeOf } from "./policy-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { DraftSaveOptions, SaveNote } from "@/components/use-draft-save";
import { ApiError, type DraftPublished, type PolicySetRow, type SessionRow, type UserRow, listPolicies, listSessions, listUsers, simulate } from "@/lib/api";
import type { SaveItem } from "@/lib/draft-save";
import { PUBLISH, PROBE_ORIGIN, PUBLISH_DRAFT_NOTE, STRIP, VERDICT, blastSentence, publishTitle, rulesStrip, savedToast, whatChanges } from "@/lib/policy-words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listPolicies: vi.fn(),
  listSessions: vi.fn(),
  listUsers: vi.fn(),
  simulate: vi.fn(),
}));
// The publish runs the shared saves, whose own suite pins the flow and its
// refusals; here they stand in so this suite pins the one PolicySet item
// the dialog hands them and what a publish does next.
const save = vi.hoisted(() => ({ opts: null as DraftSaveOptions | null, note: null as SaveNote | null, publish: vi.fn<(items: SaveItem[]) => Promise<void>>() }));
vi.mock("@/components/use-draft-save", async (orig) => ({
  ...(await orig<typeof import("@/components/use-draft-save")>()),
  useDraftSave: (o: DraftSaveOptions) => { save.opts = o; return { busy: null, note: save.note, saveDraft: vi.fn(), saveAndPublish: save.publish, dialog: null }; },
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/components/policy-test", () => ({
  PolicyTest: ({ open, draft }: { open: boolean; draft?: { name: string; yaml: string } | null }) =>
    open ? <div data-test-sheet={draft ? draft.name : ""}>{draft ? draft.yaml : ""}</div> : null,
}));

const rule = (id: string, seconds: number) => `    - id: ${id}
      tools: [mcp.call]
      apps: [demo-tools]
      toolNames:
        allow: [write-file]
      effect: allow
      mode: approve
      approve:
        deciders: [sponsor]
        timeoutSeconds: ${seconds}
`;

const doc = (rules: string) => `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: dev-tools-approvals
spec:
  priority: 150
  match:
    roles: [dev-tools]
  rules:
${rules}`;

const NEW_TEXT = doc(rule("approve-demo-tools-write-file", 120));
const BASE_TEXT = doc(rule("keep-one", 90) + rule("goes-away", 90));
const REPLACEMENT = doc(rule("keep-one", 120) + rule("approve-demo-tools-write-file", 120));

const sets: PolicySetRow[] = [
  { name: "org-baseline", status: "active", summary: { matchRoles: [], postures: { deny: 3 } } },
  { name: "dev-guardrails", status: "active", summary: { matchRoles: ["dev-tools"], postures: { deny: 3, hold: 5, ticket: 1, allow: 4 } } },
  { name: "release-window", status: "draft", summary: { matchRoles: ["dev-tools"], postures: { ticket: 1 } } },
];

const session = (id: string, user: string): SessionRow =>
  ({ id, user_id: "u-" + user, username: user, harness: "claude", attestation: "none", status: "active", started_at: "", last_seen: "" });
const sessions: SessionRow[] = [session("s1", "joe"), session("s2", "agent-sam"), session("s3", "kim"), session("s4", "raj")];
const holders = [{ id: "u-joe", username: "joe" }, { id: "u-agent-sam", username: "agent-sam" }] as UserRow[];

const probe = { event: { kind: "tool.pre", tool: "mcp.call", app: "demo-tools", toolName: "write-file" }, user: "joe", label: "write-file on demo-tools by joe" };

const onDone = vi.fn();

function mount(props: Partial<React.ComponentProps<typeof PolicyPublish>> = {}) {
  return render(
    <TooltipProvider>
      <PolicyPublish
        open
        onOpenChange={() => undefined}
        name="dev-tools-approvals"
        text={NEW_TEXT}
        baseText={null}
        wasLive={false}
        roles={["dev-tools"]}
        probe={probe}
        onDone={onDone}
        {...props}
      />
    </TooltipProvider>,
  );
}

const textOf = (selector: string) => (document.querySelector(selector) as HTMLElement).textContent || "";
const blast = () => textOf("[data-blast]");

describe("the publish dialog", () => {
  beforeEach(() => {
    vi.mocked(listPolicies).mockResolvedValue({ items: sets });
    vi.mocked(listSessions).mockResolvedValue({ items: sessions, next_cursor: "" });
    vi.mocked(listUsers).mockResolvedValue({ items: holders, next_cursor: "" });
    vi.mocked(simulate).mockResolvedValue({ active: { effect: "allow" }, draft: { effect: "approve" }, subject: {}, snapshot: "7c1e9a2bdeadbeef" });
    save.publish.mockReset().mockResolvedValue(undefined);
    save.note = null;
    save.opts = null;
    onDone.mockClear();
  });

  it("reads the rule diff by id", () => {
    expect(changeOf(null, NEW_TEXT)).toEqual({ added: 1, changed: [], removed: [] });
    expect(changeOf(BASE_TEXT, REPLACEMENT)).toEqual({ added: 1, changed: ["keep-one"], removed: ["goes-away"] });
  });

  it("says what a new policy changes", async () => {
    mount();
    expect(await screen.findByText(publishTitle("dev-tools-approvals"))).toBeTruthy();
    expect(screen.getByText(whatChanges(1, [], [], ["dev-tools"], true, false))).toBeTruthy();
  });

  it("says what a replacement changes", async () => {
    mount({ text: REPLACEMENT, baseText: BASE_TEXT, wasLive: true });
    expect(await screen.findByText(whatChanges(1, ["keep-one"], ["goes-away"], ["dev-tools"], false, true))).toBeTruthy();
  });

  it("reads the call before and after, and the rule counts for the role", async () => {
    mount();
    await waitFor(() => expect(textOf("[data-verdict-now]")).toBe(VERDICT.allow));
    expect(textOf("[data-verdict-after]")).toBe(VERDICT.hum);
    expect(textOf("[data-publish-strip]")).toContain(STRIP.rules + " dev-tools");
    expect(textOf("[data-publish-strip]")).toContain(PROBE_ORIGIN);
    await waitFor(() => expect(textOf("[data-rules-now]")).toBe(rulesStrip({ deny: 3, hum: 6, allow: 4, hold: 5, ticket: 1, confirm: 0, check: 0 })));
    expect(textOf("[data-rules-after]")).toBe(rulesStrip({ deny: 3, hum: 7, allow: 4, hold: 6, ticket: 1, confirm: 0, check: 0 }));
  });

  it("counts the live version of the set it replaces in the before column", async () => {
    mount({ text: NEW_TEXT, baseText: BASE_TEXT, wasLive: true });
    await waitFor(() => expect(textOf("[data-rules-now]")).toBe(rulesStrip({ deny: 3, hum: 8, allow: 4, hold: 7, ticket: 1, confirm: 0, check: 0 })));
    expect(textOf("[data-rules-after]")).toBe(rulesStrip({ deny: 3, hum: 7, allow: 4, hold: 6, ticket: 1, confirm: 0, check: 0 }));
  });

  it("leaves a stored draft out of the before column, since it runs nothing", async () => {
    mount({ text: NEW_TEXT, baseText: BASE_TEXT, wasLive: false });
    await waitFor(() => expect(textOf("[data-rules-now]")).toBe(rulesStrip({ deny: 3, hum: 6, allow: 4, hold: 5, ticket: 1, confirm: 0, check: 0 })));
  });

  it("counts the sessions that hold the role", async () => {
    mount();
    await waitFor(() => expect(blast()).toBe(blastSentence({ total: 4, covered: 2, names: ["joe", "agent-sam"], extra: 0, capped: false, unknown: false, holdersUnknown: false, roles: ["dev-tools"] })));
  });

  it("counts every session when the policy names no role", async () => {
    mount({ roles: [] });
    await waitFor(() => expect(blast()).toBe(blastSentence({ total: 4, covered: 0, names: [], extra: 0, capped: false, unknown: false, holdersUnknown: false, roles: [] })));
  });

  it("says when nobody holds the role", async () => {
    vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
    mount();
    await waitFor(() => expect(blast()).toBe(blastSentence({ total: 4, covered: 0, names: [], extra: 0, capped: false, unknown: false, holdersUnknown: false, roles: ["dev-tools"] })));
  });

  it("says the count is a floor when a page was capped", async () => {
    vi.mocked(listSessions).mockResolvedValue({ items: sessions, next_cursor: "more" });
    mount();
    await waitFor(() => expect(blast()).toBe(blastSentence({ total: 4, covered: 2, names: ["joe", "agent-sam"], extra: 0, capped: true, unknown: false, holdersUnknown: false, roles: ["dev-tools"] })));
  });

  it("says the number is unknown when the sessions could not be read", async () => {
    vi.mocked(listSessions).mockRejectedValue(new ApiError("strazad did not answer", 0, true));
    mount();
    await waitFor(() => expect(blast()).toBe(blastSentence({ total: 0, covered: 0, names: [], extra: 0, capped: false, unknown: true, holdersUnknown: false, roles: ["dev-tools"] })));
  });

  const published = { draft: { id: "61" }, snapshot: "3be0a1ff", servers: [], next: [] } as unknown as DraftPublished;
  const put = (text: string, op = "put") => [{ kind: "PolicySet", name: "dev-tools-approvals", op, doc: text }];

  it("publishes the document as one PolicySet put through the drafts, then hands back the version", async () => {
    mount();
    await screen.findByText(publishTitle("dev-tools-approvals"));
    await userEvent.click(screen.getByRole("button", { name: PUBLISH }));
    expect(save.publish).toHaveBeenCalledWith(put(NEW_TEXT));
    expect(save.opts?.name).toBe("dev-tools-approvals");
    expect(onDone).not.toHaveBeenCalled();
    save.opts?.onPublished(published);
    expect(onDone).toHaveBeenCalledWith({ name: "dev-tools-approvals", snapshot: "3be0a1ff" });
  });

  it("keeps the dialog open with the note of a refused or unanswered publish", async () => {
    save.note = { tone: "refused", lines: ["Nothing was saved.", "match.roles names dev, a business role."] };
    mount();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe("Nothing was saved.match.roles names dev, a business role.");
    expect(screen.getByText(publishTitle("dev-tools-approvals"))).toBeTruthy();
    expect(onDone).not.toHaveBeenCalled();
  });

  it("turns a set that is off on, and says so under the description", async () => {
    mount({ text: REPLACEMENT, baseText: BASE_TEXT, wasLive: false, turnOn: true });
    expect(await screen.findByText(PUBLISH_DRAFT_NOTE)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: PUBLISH }));
    expect(save.publish).toHaveBeenCalledWith(put(REPLACEMENT));
    expect(save.opts?.toast).toBeUndefined();
  });

  it("keeps a set that is off off when the page does not ask to turn it on", async () => {
    mount({ text: REPLACEMENT, baseText: BASE_TEXT, wasLive: false });
    await screen.findByText(publishTitle("dev-tools-approvals"));
    expect(screen.queryByText(PUBLISH_DRAFT_NOTE)).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: PUBLISH }));
    expect(save.publish).toHaveBeenCalledWith(put(REPLACEMENT, "off"));
    expect(save.opts?.toast).toBe(savedToast("dev-tools-approvals", false));
    expect(save.opts?.toast).toBe("dev-tools-approvals is saved. It is off, so it gates nothing until it is turned on.");
  });

  it("opens Test a call on the document that is about to be stored", async () => {
    mount();
    await screen.findByText(publishTitle("dev-tools-approvals"));
    await userEvent.click(screen.getByRole("button", { name: "Test a call first" }));
    await waitFor(() => expect(document.querySelector("[data-test-sheet]")).toBeTruthy());
    const sheet = document.querySelector("[data-test-sheet]") as HTMLElement;
    expect(sheet.getAttribute("data-test-sheet")).toBe("dev-tools-approvals");
    expect(sheet.textContent).toBe(NEW_TEXT);
  });
});
