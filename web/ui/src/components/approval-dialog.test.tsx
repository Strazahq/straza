import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApprovalDialog } from "./approval-dialog";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type ApprovalRow, getPolicy } from "@/lib/api";
import {
  APPROVE,
  CARD_PARAMS,
  CLOSE,
  DENY,
  DETAILS,
  FACT_WHO,
  GRANT_UNKNOWN,
  IF_APPROVE,
  IF_DENY,
  KIND_SAYS,
  OPEN_RULE,
  PARAMS_COMMAND,
  PARAMS_HINT_COMMAND,
  PARAMS_HINT_NONE,
  PARAMS_HINT_STORED,
  PARAMS_NONE,
  REQUEST_TITLE,
  SAYS_NONE,
  SAYS_NONE_HOOK,
  SAYS_OWN,
  SHOW_JSON,
  UNVERIFIED,
  WINDOW_CLOSED,
  approveDoes,
  decidedWhere,
  denyDoes,
  doesLine,
  expiredLine,
  openRole,
  signedWords,
  sponsoredWords,
  whoLine,
} from "@/lib/approval-words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  getPolicy: vi.fn(),
}));

const at = (s: number) => new Date(Date.now() + s * 1000).toISOString();
const seat = { user: "alice", roles: ["straza-admin"] };
const YAML = [
  "apiVersion: straza.dev/v1beta1", "kind: PolicySet", "metadata:", "  name: dev-guardrails", "spec:", "  rules:",
  "    - id: dev-mcp-env-ticket", "      tools: [mcp.call]", "      effect: allow", "      mode: approve",
  "      approve:", "        class: ticket", "        grantTTLSeconds: 7200", "",
].join("\n");

const base: ApprovalRow = {
  id: "apr_hold", state: "pending", createdAt: "", expiresAt: "", decidedAt: null,
  user: "u-joe", username: "joe-java-developer-agent", session: "0199c1a2-7e3f-7000-8000-000000000001",
  rule: "dev-mcp-sum-approval-showcase", set: "dev-guardrails", lane: "gateway",
  summary: "mcp.call demo-tools:get-sum", justification: "Adding the two build durations the user asked about.",
  approverRoles: [], approverUsers: ["alice"], selfApproval: false, mode: "approve",
  decidedBy: "", decidedByName: "",
};

let hold: ApprovalRow;
let ticket: ApprovalRow;
let onHook: ApprovalRow;
let denied: ApprovalRow;
let gone: ApprovalRow;

const decide = vi.fn();
const mount = (row: ApprovalRow, isMine = true, isStuck = false, holders: Record<string, number> = {}) => render(
  <TooltipProvider>
    <ApprovalDialog row={row} open onOpenChange={() => undefined} seat={seat} holders={holders} isMine={isMine} isStuck={isStuck} onDecide={decide} />
  </TooltipProvider>,
);
const dialog = () => screen.getByRole("dialog");
const one = (selector: string) => document.querySelector(selector) as HTMLElement;
const fact = (label: string) => one('[data-fact="' + label + '"]');
const foot = () => within(one("[data-slot=dialog-footer]"));

describe("the request dialog", () => {
  beforeEach(() => {
    decide.mockClear();
    vi.mocked(getPolicy).mockResolvedValue({ name: "dev-guardrails", yaml: YAML } as never);
    hold = { ...base, createdAt: at(-28), expiresAt: at(92), argsPreview: '{\n  "a": 2,\n  "b": 40\n}', argvHashPrefix: "2f9c1a7e", bindingScope: "call" };
    ticket = { ...base, id: "apr_ticket", class: "ticket", rule: "dev-mcp-env-ticket", summary: "mcp.call demo-tools:get-env", createdAt: at(-60), expiresAt: at(86340), argsPreview: '{"user":"jdoe","options":{"recompute":true},"notify":["a@x","b@x"]}', argvHashPrefix: "c04d77a1", bindingScope: "call" };
    onHook = { ...base, id: "apr_held", createdAt: at(-75), expiresAt: at(45), lane: "hook", summary: "shell.exec: printf lf-q4-x", rule: "hold-printf", set: "lf-q4-hold", justification: "", argsPreview: "printf lf-q4-x", approverUsers: undefined, approverRoles: ["sec-approvers"] };
    denied = { ...base, id: "apr_denied", state: "denied", createdAt: at(-3720), expiresAt: at(-3600), decidedAt: at(-120), decidedBy: "u-alice", decidedByName: "alice", channel: "console", decidedReason: "walk cleanup", argsPreview: "{}", argvHashPrefix: "7d1e5b90" };
    gone = { ...onHook, id: "apr_expired", state: "expired", createdAt: at(-7320), expiresAt: at(-7200) };
  });

  it("opens in the middle with focus on Close and no tooltip of its own", async () => {
    mount(hold);
    expect(dialog().getAttribute("data-request-dialog")).toBe("apr_hold");
    await waitFor(() => expect(document.activeElement?.textContent).toBe(CLOSE));
    expect(screen.queryByRole("tooltip")).toBe(null);
  });

  it("is 740 px wide above the sm breakpoint, keeps its margins on a narrower page and scrolls its body", () => {
    mount(hold);
    const box = dialog();
    expect(box.className).toContain("sm:max-w-[min(740px,calc(100%-2rem))]");
    // Under the breakpoint the kit's own cap stands, so a phone keeps the full-width dialog.
    expect(box.className).toContain("max-w-[calc(100%-2rem)]");
    expect(box.className).toContain("max-h-[90vh]");
    expect(one("[data-call-card]").parentElement!.className).toContain("overflow-y-auto");
  });

  it("titles the dialog Approval request alone and keeps the call in its name for a screen reader", () => {
    mount(hold);
    const title = within(dialog()).getByRole("heading");
    expect(title.textContent).toBe(REQUEST_TITLE + " get-sum");
    expect((title.querySelector("span") as HTMLElement).className).toContain("sr-only");
    expect((title.querySelector("span") as HTMLElement).textContent).toBe(" get-sum");
    expect(title.querySelector("[data-phase]")).toBe(null);
  });

  it.each([
    ["a tool on a server", () => hold],
    ["a command from the hook", () => onHook],
  ])("sets %s at title size in the mono face, the captions at caption size and the sentences at body size", (_what, pick) => {
    mount(pick());
    const tool = one("[data-card-tool]");
    for (const cls of ["font-mono", "text-xl", "font-semibold"]) expect(tool.className).toContain(cls);
    const captions = [...document.querySelectorAll("[data-outcomes] .uppercase")] as HTMLElement[];
    expect(captions.map((c) => c.textContent)).toEqual([IF_APPROVE, IF_DENY]);
    for (const caption of captions) expect(caption.className).toContain("text-[13px]");
    expect(one("[data-if=approve] p").className).toContain("text-[15px]");
    // Nothing in the dialog is set under the 12 px floor.
    expect(dialog().querySelector('[class*="text-[11px]"], [class*="text-[10px]"]')).toBe(null);
  });

  it.each([
    ["a gateway call", () => hold, "joe-java-developer-agent wants to call", "get-sum", "demo-tools"],
    ["a command from the hook", () => onHook, "joe-java-developer-agent wants to run", "printf lf-q4-x", null],
    ["a decided request", () => denied, "joe-java-developer-agent asked to call", "get-sum", "demo-tools"],
  ])("says who asks for %s and shows the call once, in its own box", (_what, pick, ask, call, server) => {
    mount(pick(), false);
    expect(one("[data-ask]").textContent).toBe(ask);
    expect(one("[data-card-tool]").textContent).toBe(call);
    expect(document.querySelectorAll("[data-card-tool]").length).toBe(1);
    const chip = document.querySelector("[data-card-server]");
    expect(chip ? chip.textContent : null).toBe(server);
  });

  it.each([
    ["a hold", () => hold, "hold", "text-warn", /^1 m 3\d s left$/, "then the call is denied", true],
    ["a ticket", () => ticket, "ticket", "text-teal", /^23 h 5\d min left$/, "then the request is denied", false],
  ])("says in a strip that %s waits, how long is left and what happens at zero", (_what, pick, kind, tone, left, zero, bar) => {
    mount(pick());
    const strip = one("[data-strip]");
    const chip = strip.querySelector("[data-kind]") as HTMLElement;
    expect(chip.textContent).toBe(kind);
    expect(chip.className).toContain(tone);
    expect(one("[data-kind-says]").textContent).toBe(KIND_SAYS[kind as "hold" | "ticket"]);
    expect(one("[data-time-left]").textContent).toMatch(left);
    expect(one("[data-at-zero]").textContent).toBe(zero);
    const drain = document.querySelector("[data-drain]") as HTMLElement | null;
    expect(!!drain).toBe(bar);
    if (drain) {
      expect(drain.getAttribute("aria-hidden")).toBe("true");
      // The hold asked 28 s ago and runs out in 92 s, so 92 of 120 seconds are left.
      expect((drain.firstElementChild as HTMLElement).style.width).toBe("77%");
    }
  });

  it("says the window closed, and stops saying what a yes does, once the time has run out with the dialog open", () => {
    const late = { ...hold, createdAt: at(-125), expiresAt: at(-5) };
    mount(late);
    expect(one("[data-window-closed]").textContent).toBe(WINDOW_CLOSED);
    expect(document.querySelector("[data-kind-says]")).toBe(null);
    expect(one("[data-time-left]").textContent).toBe("expired");
    expect(document.querySelector("[data-at-zero]")).toBe(null);
    expect(document.querySelector("[data-outcomes]")).toBe(null);
    expect((one("[data-drain]").firstElementChild as HTMLElement).style.width).toBe("0%");
    expect(document.getElementById(dialog().getAttribute("aria-describedby")!)?.textContent).toBe(WINDOW_CLOSED);
  });

  it.each([
    ["two parameters", () => hold, "2 values"],
    ["nested parameters", () => ticket, "3 values"],
    ["a command line", () => onHook, PARAMS_HINT_COMMAND],
    ["a stored value that is no object", () => ({ ...hold, argsPreview: "[1,2]" }), PARAMS_HINT_STORED],
    ["no stored parameters", () => ({ ...hold, argsPreview: undefined }), PARAMS_HINT_NONE],
  ])("always shows the Parameters line, folded, with a hint for %s", (_what, pick, hint) => {
    mount(pick());
    const fold = one("[data-params-fold]") as HTMLDetailsElement;
    expect(fold.open).toBe(false);
    expect(within(fold.querySelector("summary") as HTMLElement).getByText(CARD_PARAMS)).toBeTruthy();
    expect(one("[data-params-hint]").textContent).toBe(" · " + hint + " ");
  });

  it("opens the parameters on a click, as a table with the JSON and the hash one more click away", async () => {
    mount(hold);
    const fold = one("[data-params-fold]") as HTMLDetailsElement;
    await userEvent.click(within(fold).getByText(CARD_PARAMS));
    expect(fold.open).toBe(true);
    const rows = [...fold.querySelectorAll("[data-param]")].map((el) => el.getAttribute("data-param"));
    expect(rows).toEqual(["a", "b"]);
    expect(within(one("[data-param='b']")).getByText("40")).toBeTruthy();
    const json = within(fold).getByText(SHOW_JSON).parentElement as HTMLDetailsElement;
    expect(json.open).toBe(false);
    expect(one("[data-hash]").textContent).toContain("sha256 2f9c1a7e");
  });

  it("flattens nested parameters into dotted names and words their values", () => {
    mount(ticket);
    const rows = [...document.querySelectorAll("[data-param]")].map((el) => el.getAttribute("data-param"));
    expect(rows).toEqual(["user", "options.recompute", "notify"]);
    expect(within(one("[data-param='options.recompute']")).getByText("yes")).toBeTruthy();
    expect(within(one("[data-param='notify']")).getByText("a@x, b@x")).toBeTruthy();
  });

  it("shows a hook call as a command with no parameter table, and a stored value that is no object as it is", () => {
    const view = mount(onHook, false, true);
    expect(document.querySelector("[data-params]")).toBe(null);
    expect(one("[data-params-none]").textContent).toBe(PARAMS_COMMAND);
    view.unmount();
    const raw = mount({ ...hold, argsPreview: "[1,2]" });
    expect(one("[data-params-none]").textContent).toBe("[1,2]");
    raw.unmount();
    mount({ ...hold, argsPreview: undefined });
    expect(one("[data-params-none]").textContent).toBe(PARAMS_NONE);
    expect(screen.queryByText(SHOW_JSON)).toBe(null);
  });

  it("says in one line where the call runs, when it was asked and who sponsors the agent", () => {
    const view = mount(hold);
    expect(one("[data-meta]").textContent).toBe("through the MCP gateway · asked 28 s ago · sponsored by you");
    view.unmount();
    const other = mount({ ...hold, approverUsers: ["marta"] }, false);
    expect(one("[data-meta]").textContent).toBe("through the MCP gateway · asked 28 s ago · sponsored by marta");
    other.unmount();
    mount(onHook, false, true);
    expect(one("[data-meta]").textContent).toBe("on its own machine · asked 1 m ago");
  });

  it("quotes the agent's reason as its own unchecked words, and says in one quiet line when it gave none", () => {
    const view = mount(hold);
    expect(one("[data-justification]").textContent).toBe("“Adding the two build durations the user asked about.”");
    expect(one("[data-says-own]").textContent).toBe(SAYS_OWN);
    expect(screen.queryByText(UNVERIFIED)).toBe(null);
    view.unmount();
    const hook = mount(onHook, false, true);
    expect(one("[data-justification-none]").textContent).toBe(SAYS_NONE_HOOK);
    expect(document.querySelector("[data-says]")).toBe(null);
    hook.unmount();
    mount({ ...hold, justification: "" });
    expect(one("[data-justification-none]").textContent).toBe(SAYS_NONE);
  });

  it("says in two boxes what a yes and a no do, and reads the ticket's grant window from the rule", async () => {
    const view = mount(hold);
    expect(one("[data-if=approve] p").textContent).toBe("The call runs now, once.");
    expect(one("[data-if=deny] p").textContent).toBe("The call does not run. The agent is told who said no.");
    view.unmount();
    mount(ticket);
    await waitFor(() => expect(one("[data-if=approve] p").textContent).toBe("Its next try of this exact call runs, once, within 2 hours."));
    expect(one("[data-if=deny] p").textContent).toBe("Nothing runs. The agent is told who said no.");
    expect(getPolicy).toHaveBeenCalledWith("dev-guardrails");
  });

  it("falls back to the rule's own words when the policy cannot be read", async () => {
    vi.mocked(getPolicy).mockRejectedValue(new Error("no"));
    mount(ticket);
    await waitFor(() => expect(getPolicy).toHaveBeenCalled());
    expect(one("[data-if=approve] p").textContent).toBe("Its next try of this exact call runs, once, within " + GRANT_UNKNOWN + ".");
  });

  it("speaks to the reader about who can decide, and points a stuck request at the role and the rule", () => {
    const view = mount(hold);
    expect(one("[data-who-line]").textContent).toContain(FACT_WHO);
    expect(one("[data-who]").textContent).toBe("You (alice), as joe-java-developer-agent's sponsor. Nobody else.");
    view.unmount();
    const other = mount(hold, false);
    expect(one("[data-who]").textContent).toBe("alice, as joe-java-developer-agent's sponsor. Not you.");
    other.unmount();
    const role = mount({ ...onHook, state: "pending" }, true, false, { "sec-approvers": 3 });
    expect(one("[data-who]").textContent).toBe("You, as one of 3 people holding sec-approvers. The first to answer decides.");
    role.unmount();
    mount(onHook, false, true);
    expect(one("[data-who]").textContent).toBe(whoLine(onHook, seat, {}, false, true));
    expect(within(one("[data-who-line]")).getByRole("button", { name: openRole("sec-approvers") })).toBeTruthy();
    expect(within(one("[data-who-line]")).getByRole("button", { name: OPEN_RULE })).toBeTruthy();
  });

  it("folds the record rows under Details while the request waits, with the deadline and what happens at zero", () => {
    mount(hold);
    const details = one("[data-details]") as HTMLDetailsElement;
    expect(details.open).toBe(false);
    expect(within(details.querySelector("summary") as HTMLElement).getByText(DETAILS)).toBeTruthy();
    const labels = [...details.querySelectorAll("[data-fact]")].map((el) => el.getAttribute("data-fact"));
    expect(labels).toEqual(["Requested by", "Session", "When", "How it came in", "Decide by", "Policy rule"]);
    expect(fact("Requested by").textContent).toContain(sponsoredWords("alice"));
    expect(one("[data-decide-by]").textContent || "").toMatch(/left · until .* · then the call is denied$/);
  });

  it("opens Details on a decided request and leads with the decision, who decided, their reason and what signed it", () => {
    mount(denied, false);
    expect((one("[data-details]") as HTMLDetailsElement).open).toBe(true);
    expect(one("[data-strip] [data-phase]").textContent).toBe("Denied");
    expect(one("[data-decided-line]").textContent).toMatch(/^Denied by alice from the console, 2 m ago\. Their reason: “walk cleanup”\.$/);
    expect(fact("Decided by").textContent).toContain(decidedWhere(denied));
    expect(within(fact("Their reason")).getByText("“walk cleanup”")).toBeTruthy();
    expect(within(fact("Signed with")).getByText(signedWords(denied))).toBeTruthy();
    expect(document.querySelector("[data-outcomes]")).toBe(null);
    expect(document.querySelector("[data-kind-says]")).toBe(null);
    expect(document.querySelector("[data-decide-by]")).toBe(null);
  });

  it("says nobody decided an expired request and names the role it was routed to", () => {
    mount(gone, false, true);
    expect(one("[data-strip] [data-phase]").textContent).toBe("Expired");
    expect(one("[data-expired-line]").textContent).toBe(expiredLine(gone, true));
    expect(one("[data-expired-line]").textContent).toContain("sec-approvers");
  });

  it("puts Close on the left and Deny beside Approve on the right when the request is yours, Close alone otherwise", async () => {
    const view = mount(hold);
    const buttons = foot().getAllByRole("button").map((b) => b.textContent);
    expect(buttons).toEqual([CLOSE, DENY, APPROVE]);
    await userEvent.click(foot().getByRole("button", { name: APPROVE }));
    expect(decide).toHaveBeenCalledWith(hold, "approve");
    await userEvent.click(foot().getByRole("button", { name: DENY }));
    expect(decide).toHaveBeenCalledWith(hold, "deny");
    view.unmount();
    mount(hold, false);
    expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE]);
  });
});

describe("what a yes and a no do", () => {
  const row = (patch: Partial<ApprovalRow>): ApprovalRow => ({ ...base, ...patch });
  const shell = { lane: "hook", summary: "shell.exec: make deploy ENV=staging" };

  it.each([
    ["a hold releases the one call", row({ bindingScope: "call" }), "1 hour", "The call runs now, once."],
    ["a hold says the same whatever the rule binds", row({ bindingScope: "tool_identity" }), null, "The call runs now, once."],
    ["a ticket bound to the call covers that exact call", row({ class: "ticket", bindingScope: "call" }), "2 hours", "Its next try of this exact call runs, once, within 2 hours."],
    ["a ticket bound to a command covers that exact command", row({ class: "ticket", bindingScope: "call", ...shell }), "1 hour", "Its next try of this exact command runs, once, within 1 hour."],
    ["a ticket bound to the tool covers every call of it", row({ class: "ticket", bindingScope: "tool_identity" }), "2 hours", "Its next call of this tool runs, once, within 2 hours, whatever the parameters."],
    ["a ticket with no binding scope on the record claims neither", row({ class: "ticket" }), "2 hours", "Its next matching call runs, once, within 2 hours."],
    ["a command with no binding scope claims neither", row({ class: "ticket", ...shell }), "1 hour", "Its next matching call runs, once, within 1 hour."],
    ["an unread grant window is named as the rule's", row({ class: "ticket", bindingScope: "call" }), null, "Its next try of this exact call runs, once, within " + GRANT_UNKNOWN + "."],
  ])("%s", (_what, r, grant, sentence) => {
    expect(approveDoes(r, grant)).toBe(sentence);
  });

  it.each([
    ["a denied hold stops the call on the line", row({}), "The call does not run. The agent is told who said no."],
    ["a denied ticket runs nothing", row({ class: "ticket" }), "Nothing runs. The agent is told who said no."],
  ])("%s", (_what, r, sentence) => {
    expect(denyDoes(r)).toBe(sentence);
  });

  it("joins both outcomes into the one line a screen reader hears", () => {
    const r = row({ bindingScope: "call" });
    expect(doesLine(r, null)).toBe("If you approve: The call runs now, once. If you deny: The call does not run. The agent is told who said no.");
  });
});

describe("who can decide when the rule names the sponsor and a role", () => {
  const waits: ApprovalRow = { ...base, username: "ines-release-engineer-agent", approverUsers: ["tomas"], approverRoles: ["sec-approvers"] };
  const decided: ApprovalRow = { ...waits, state: "approved" };

  it.each([
    ["a holder of the role who is not the sponsor reads as a holder and is told who the sponsor is", waits, { user: "alice", roles: ["sec-approvers"] }, true, "You, as a holder of sec-approvers, or tomas, the sponsor. The first to answer decides."],
    ["the sponsor reads as the sponsor and is told the role", waits, { user: "tomas", roles: [] }, true, "You, as the sponsor, or anyone holding sec-approvers. The first to answer decides."],
    ["a sponsor who also holds the role reads as the sponsor", waits, { user: "tomas", roles: ["sec-approvers"] }, true, "You, as the sponsor, or anyone holding sec-approvers. The first to answer decides."],
    ["a reader who is neither is told who decides and that it is not them", waits, { user: "marta", roles: [] }, false, "tomas, as ines-release-engineer-agent's sponsor, or anyone holding sec-approvers. Not you."],
    ["a decided request names the sponsor and the role and speaks to no reader", decided, { user: "alice", roles: ["sec-approvers"] }, false, "tomas, as ines-release-engineer-agent's sponsor, or anyone holding sec-approvers."],
  ])("%s", (_what, r, reader, isMine, sentence) => {
    expect(whoLine(r, reader, {}, isMine, false)).toBe(sentence);
  });
});

describe("accessible approval outcomes", () => {
  it("describes a waiting request by what a yes and a no do", () => {
    const waits = { ...base, createdAt: at(-28), expiresAt: at(92) };
    mount(waits);
    const id = dialog().getAttribute("aria-describedby");
    expect(document.getElementById(id!)?.textContent).toBe(doesLine(waits, null));
  });

  it.each([
    [{ state: "approved", class: "hold" }, /^Approved by alice from the console\.$/],
    [{ state: "denied", channel: "slack", decidedReason: "walk cleanup" }, /^Denied by alice from Slack\. Their reason: “walk cleanup”\.$/],
    [{ state: "approved", class: "hold", decidedReason: "Looks fine." }, /^Approved by alice from the console\. Their reason: “Looks fine\.”$/],
    [{ state: "approved", class: "ticket", grantExpiresAt: "2099-01-01T00:00:00Z" }, /^Approved by alice from the console\. The same call within .+ runs\.$/],
    [{ state: "approved", class: "ticket", consumedAt: "2026-01-01T00:00:00Z", consumedBy: "session-used" }, /^Used by session \S+, .+\.$/],
    [{ state: "approved", class: "ticket", grantExpiresAt: "2020-01-01T00:00:00Z" }, /^Expired: approved by alice, never used\.$/],
  ] as const)("announces the stored outcome and grant phase %j as whole sentences", (patch, sentence) => {
    mount({ ...base, ...patch, decidedBy: "alice" });
    const id = dialog().getAttribute("aria-describedby");
    const text = document.getElementById(id!)?.textContent || "";
    expect(text).toMatch(sentence);
    expect(text).not.toMatch(/\. [a-z]/);
    expect(text).not.toContain(" · ");
    expect(text).not.toContain("nobody decided");
    expect(text).not.toContain("so the call was denied");
  });
});
