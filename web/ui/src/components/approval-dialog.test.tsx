import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DecideRefusal } from "./approval-decide";
import { ApprovalDialog, type BeforeSetup } from "./approval-dialog";
import { TooltipProvider } from "@/components/ui/tooltip";
import * as SW from "@/self-service/words";
import { ApiError, type ApprovalRow, getPolicy } from "@/lib/api";
import { notify } from "@/lib/notify";
import {
  ABOUT_APPROVE,
  ABOUT_DENY,
  APPROVE,
  APPROVE_REQUEST,
  CARD_PARAMS,
  CLOSE,
  DECIDE_BY_HELP_HOLD,
  DECIDE_BY_HELP_HOOK,
  DECIDE_BY_HELP_TICKET,
  DENY,
  DENY_REQUEST,
  DETAILS,
  FACT_DECIDE_BY,
  FACT_WHO,
  IF_APPROVE,
  IF_DENY,
  KIND_SAYS,
  OPEN_RULE,
  PARAMS_HINT_COMMAND,
  PARAMS_HINT_NONE,
  PARAMS_HINT_STORED,
  PARAMS_NONE,
  REASON_LABEL,
  REASON_PLACEHOLDER,
  SAYS_NONE,
  SAYS_OWN,
  SHOW_JSON,
  UNVERIFIED,
  type Verdict,
  WINDOW_CLOSED,
  alreadyDecided,
  approveDoes,
  approvedToast,
  decideHelp,
  decidedWhere,
  deniedToast,
  denyDoes,
  doesLine,
  expiredLine,
  notYours,
  openRole,
  reasonOf,
  requestTitle,
  signedWords,
  sponsoredWords,
  whoLine,
} from "@/lib/approval-words";
import { refused } from "@/lib/say";
import { absTime } from "@/lib/words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  getPolicy: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

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

// send is the queue's decide lane, get its single read after a 409, decided
// the record handed back and closed the dialog's own close.
const send = vi.fn();
const getOne = vi.fn();
const decided = vi.fn();
const closed = vi.fn();
type Lane = { get?: ((id: string) => Promise<ApprovalRow | null>) | null; needsDevice?: boolean; beforeSetup?: BeforeSetup };
const mount = (row: ApprovalRow, isMine = true, isStuck = false, holders: Record<string, number> = {}, lane: Lane = {}) => render(
  <TooltipProvider>
    <ApprovalDialog
      row={row} open onOpenChange={closed} seat={seat} holders={holders} isMine={isMine} isStuck={isStuck}
      needsDevice={lane.needsDevice} decide={send} get={lane.get === undefined ? getOne : lane.get} onDecided={decided}
      beforeSetup={lane.beforeSetup}
    />
  </TooltipProvider>,
);
// setUp is the page's setup, and SETUP the page's hook before any device of
// the reader is set up, in the self-service page's own words.
const setUp = vi.fn();
const SETUP: BeforeSetup = { onSetUp: setUp, words: { row: SW.SETUP_TO_ANSWER, line: SW.ANSWER_NEEDS_DEVICE, button: SW.SETUP_BROWSER } };
const dialog = () => screen.getByRole("dialog");
const one = (selector: string) => document.querySelector(selector) as HTMLElement;
const fact = (label: string) => one('[data-fact="' + label + '"]');
const foot = () => within(one("[data-slot=dialog-footer]"));
const press = (verdict: Verdict) => userEvent.click(foot().getByRole("button", { name: verdict === "approve" ? APPROVE : DENY }));

describe("the request dialog", () => {
  beforeEach(() => {
    send.mockReset();
    getOne.mockReset();
    decided.mockClear();
    closed.mockClear();
    setUp.mockClear();
    vi.mocked(notify.ok).mockClear();
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

  it("titles the dialog with who asks and for what, and keeps the call in its name for a screen reader", () => {
    mount(hold);
    const title = within(dialog()).getByRole("heading");
    expect(title.textContent).toBe("joe-java-developer-agent wants to call a tool get-sum");
    expect(title.textContent).toBe(requestTitle(hold) + " get-sum");
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
    for (const cls of ["font-mono", "text-title", "font-semibold"]) expect(tool.className).toContain(cls);
    const captions = [...document.querySelectorAll("[data-outcomes] .uppercase")] as HTMLElement[];
    expect(captions.map((c) => c.textContent)).toEqual([IF_APPROVE, IF_DENY]);
    for (const caption of captions) expect(caption.className).toContain("text-caption");
    expect(one("[data-if=approve] p").className).toContain("text-body");
    // Nothing in the dialog is set under the 12 px floor.
    expect(dialog().querySelector('[class*="text-[11px]"], [class*="text-[10px]"]')).toBe(null);
  });

  it.each([
    ["a gateway call", () => hold, "joe-java-developer-agent wants to call a tool", "get-sum", "demo-tools"],
    ["a command from the hook", () => onHook, "joe-java-developer-agent wants to run a command", "printf lf-q4-x", null],
    ["a decided request", () => denied, "joe-java-developer-agent asked to call a tool", "get-sum", "demo-tools"],
  ])("says who asks for %s in the title and shows the call once, in its own box", (_what, pick, ask, call, server) => {
    mount(pick(), false);
    expect(within(dialog()).getByRole("heading").firstChild!.textContent).toBe(ask);
    // The title says who asks, so no line over the call repeats it.
    expect(document.querySelector("[data-ask]")).toBe(null);
    expect(one("[data-card-tool]").textContent).toBe(call);
    expect(document.querySelectorAll("[data-card-tool]").length).toBe(1);
    const chip = document.querySelector("[data-card-server]");
    expect(chip ? chip.textContent : null).toBe(server);
    // A server is named as a tag with the server's own icon.
    if (chip) expect(chip.querySelector("svg")).not.toBe(null);
  });

  it.each([
    ["a hold", () => hold, "hold", "text-warn", /^1 m 3\d s left$/, "then the call is denied", true],
    ["a ticket", () => ticket, "ticket", "text-foreground", /^23 h 5\d min left$/, "then the request is denied", false],
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
    // The record still reads pending, so the reason field and both buttons stay until the server answers.
    expect(screen.getByLabelText(REASON_LABEL)).toBeTruthy();
    expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE, DENY, APPROVE]);
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
    expect(document.querySelector("[data-params-none]")).toBe(null);
    view.unmount();
    const raw = mount({ ...hold, argsPreview: "[1,2]" });
    expect(one("[data-params-none]").textContent).toBe("[1,2]");
    raw.unmount();
    mount({ ...hold, argsPreview: undefined });
    expect(one("[data-params-none]").textContent).toBe(PARAMS_NONE);
    expect(screen.queryByText(SHOW_JSON)).toBe(null);
  });

  it.each([
    ["a gateway hold says the agent waits on the line", () => hold, "The agent is waiting for your answer."],
    ["a hook hold says the agent was told to retry", () => onHook, "The agent was told to retry the call after your answer."],
    ["a hook hold on an MCP call says the same", () => ({ ...hold, lane: "hook" }), "The agent was told to retry the call after your answer."],
    ["a ticket says the agent moved on, whatever the lane", () => ({ ...ticket, lane: "hook" }), "The agent moved on. It runs this only after a yes."],
  ])("words the strip by the lane: %s", (_what, pick, sentence) => {
    mount(pick());
    expect(one("[data-kind-says]").textContent).toBe(sentence);
  });

  it.each([
    ["an MCP call through the gateway", () => hold, "through the MCP gateway"],
    ["an MCP call the hook held", () => ({ ...hold, lane: "hook" }), "on its own machine"],
    ["a command the hook held", () => onHook, "on its own machine"],
    ["a command an approval_request ticket asked for through the gateway", () => ({ ...ticket, lane: "gateway", summary: "shell.exec: make release" }), "on its own machine"],
  ])("says where %s runs", (_what, pick, where) => {
    mount(pick(), false);
    expect(one("[data-meta]").textContent!.split(" · ")[0]).toBe(where);
  });

  it("says in one line where the call runs, when it was asked and who sponsors the agent", () => {
    const view = mount(hold);
    expect(one("[data-meta]").textContent).toBe("through the MCP gateway · asked 28 s ago · you sponsor it");
    view.unmount();
    const other = mount({ ...hold, approverUsers: ["marta"] }, false);
    expect(one("[data-meta]").textContent).toBe("through the MCP gateway · asked 28 s ago · sponsored by marta");
    other.unmount();
    mount(onHook, false, true);
    expect(one("[data-meta]").textContent).toBe("on its own machine · asked 1 m ago");
  });

  it("quotes the requester's reason in a dashed box headed with its name and an unverified chip, and says in one quiet line when it gave none", () => {
    const view = mount(hold);
    const box = one("[data-says]");
    expect(box.className).toContain("border-dashed");
    expect(one("[data-says-head]").textContent).toBe(reasonOf("joe-java-developer-agent"));
    expect(one("[data-says-head]").textContent).toBe("joe-java-developer-agent's reason");
    const chip = one("[data-unverified]");
    expect(chip.textContent).toBe(UNVERIFIED);
    expect(chip.getAttribute("data-tone")).toBe("plain");
    expect(chip.getAttribute("title")).toBe(SAYS_OWN);
    expect(one("[data-justification]").textContent).toBe("“Adding the two build durations the user asked about.”");
    expect(document.querySelector("[data-says-own]")).toBe(null);
    view.unmount();
    const hook = mount(onHook, false, true);
    expect(one("[data-justification-none]").textContent).toBe(SAYS_NONE);
    expect(SAYS_NONE).toBe("The agent gave no reason.");
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
    await waitFor(() => expect(one("[data-if=approve] p").textContent).toBe("The agent's next try of this exact call runs once within 2 hours."));
    expect(one("[data-if=deny] p").textContent).toBe("Nothing runs. The agent is told who said no.");
    expect(getPolicy).toHaveBeenCalledWith("dev-guardrails");
  });

  it("leaves the window out when the policy cannot be read", async () => {
    vi.mocked(getPolicy).mockRejectedValue(new Error("no"));
    mount(ticket);
    await waitFor(() => expect(getPolicy).toHaveBeenCalled());
    expect(one("[data-if=approve] p").textContent).toBe("The agent's next try of this exact call runs once.");
  });

  it("speaks to the reader about who can decide, and points a stuck request at the role and the rule", () => {
    const view = mount(hold);
    expect(one("[data-who-line]").textContent).toContain(FACT_WHO);
    expect(one("[data-who]").textContent).toBe("You, as joe-java-developer-agent's sponsor. Nobody else.");
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
    expect(labels).toEqual(["Requested by", "Session", "Asked", "How it came in", "Decide by", "Policy rule"]);
    expect(fact("Requested by").textContent).toContain(sponsoredWords("alice"));
    expect(one("[data-decide-by]").textContent || "").toMatch(/left · until .* · then the call is denied$/);
  });

  it("says a way in it does not know in words, never the raw value", () => {
    mount({ ...hold, lane: "carrier-pigeon" });
    expect(fact("How it came in").textContent).toContain("another way in");
    expect(fact("How it came in").textContent).not.toContain("carrier-pigeon");
  });

  it("opens Details on a decided request and leads with the decision, who decided, their reason and what signed it", () => {
    mount(denied, false);
    expect((one("[data-details]") as HTMLDetailsElement).open).toBe(true);
    expect(one("[data-strip] [data-phase]").textContent).toBe("denied");
    expect(one("[data-decided-line]").textContent).toMatch(/^Denied by alice from the console, 2 m ago\. Their reason: “walk cleanup”\.$/);
    expect(fact("Decided by").textContent).toContain(decidedWhere(denied));
    expect(within(fact("Their reason")).getByText("“walk cleanup”")).toBeTruthy();
    expect(within(fact("Signed with")).getByText(signedWords(denied))).toBeTruthy();
    expect(document.querySelector("[data-outcomes]")).toBe(null);
    expect(document.querySelector("[data-kind-says]")).toBe(null);
    expect(document.querySelector("[data-decide-by]")).toBe(null);
  });

  it("says a stamp older than a day once in Asked and in Decided by, as days beside the date", () => {
    const old = { ...denied, createdAt: at(-3 * 86400 - 600), expiresAt: at(-3 * 86400), decidedAt: at(-2 * 86400 - 60) };
    mount(old, false);
    const stamp = /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/g;
    expect(fact("Asked").textContent).toMatch(/^Asked3 d ago · \d{4}-/);
    expect((fact("Asked").textContent || "").match(stamp)).toHaveLength(1);
    expect(fact("Decided by").textContent).toMatch(/ · 2 d ago$/);
    expect((fact("Decided by").textContent || "").match(stamp)).toHaveLength(1);
  });

  it("says nobody decided an expired request and names the role it was routed to", () => {
    mount(gone, false, true);
    expect(one("[data-strip] [data-phase]").textContent).toBe("expired");
    expect(one("[data-expired-line]").textContent).toBe(expiredLine(gone, true));
    expect(one("[data-expired-line]").textContent).toContain("sec-approvers");
  });

  it("puts a quiet Close on the left and a red outline Deny beside a primary Approve on the right, in one row on every width", () => {
    mount(hold);
    expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE, DENY, APPROVE]);
    expect(foot().getByRole("button", { name: CLOSE }).getAttribute("data-variant")).toBe("ghost");
    const deny = foot().getByRole("button", { name: DENY });
    expect(deny.getAttribute("data-variant")).toBe("outline");
    expect(deny.className).toContain("border-danger");
    expect(foot().getByRole("button", { name: APPROVE }).getAttribute("data-variant")).toBe("default");
    const row = one("[data-slot=dialog-footer]").className;
    expect(row).toContain("flex-row");
    expect(row).not.toContain("flex-col-reverse");
  });

  it("shows the reason field under Details on a request the reader may decide", () => {
    mount(hold);
    const field = one("[data-reason-field]");
    expect(one("[data-details]").compareDocumentPosition(field) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    const box = screen.getByLabelText(REASON_LABEL) as HTMLTextAreaElement;
    expect(box.placeholder).toBe(REASON_PLACEHOLDER);
  });

  it.each([
    ["someone else's request", () => mount(hold, false)],
    ["a stuck request", () => mount(onHook, false, true)],
    ["the reader's own request on a lane that cannot sign it", () => mount(hold, true, false, {}, { needsDevice: true })],
    ["a decided request", () => mount(denied, false)],
  ])("shows no reason field and no Deny or Approve on %s", (_what, arrange) => {
    arrange();
    expect(screen.queryByLabelText(REASON_LABEL)).toBe(null);
    expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE]);
  });

  it.each([
    ["the sponsor's request", () => hold, "You, as joe-java-developer-agent's sponsor. Nobody else."],
    ["a request routed to a role the reader holds", () => ({ ...onHook, state: "pending" }), "You, as a holder of sec-approvers. The first to answer decides."],
  ])("reads the who line of %s before setup as the reader's to decide, and offers the setup in place of the answer", async (_what, pick, who) => {
    const r = pick();
    mount(r, true, false, {}, { beforeSetup: SETUP });
    expect(one("[data-who]").textContent).toBe(who);
    expect(one("[data-who]").textContent).toBe(whoLine(r, seat, {}, true, false));
    expect(screen.queryByLabelText(REASON_LABEL)).toBe(null);
    expect(document.querySelector("[data-reason-field]")).toBe(null);
    const line = one("[data-needs-device]");
    expect(line.textContent).toBe(SW.ANSWER_NEEDS_DEVICE);
    expect(line.className).toContain("text-text-2");
    expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE, SW.SETUP_BROWSER]);
    const setUpButton = foot().getByRole("button", { name: SW.SETUP_BROWSER });
    expect(setUpButton.getAttribute("data-variant")).toBe("default");
    await userEvent.click(setUpButton);
    expect(closed).toHaveBeenCalledWith(false);
    expect(setUp).toHaveBeenCalledTimes(1);
    expect(closed.mock.invocationCallOrder[0]).toBeLessThan(setUp.mock.invocationCallOrder[0]);
    expect(send).not.toHaveBeenCalled();
  });

  it.each([
    ["someone else's request", () => mount(hold, false, false, {}, { beforeSetup: SETUP })],
    ["a decided request", () => mount(denied, true, false, {}, { beforeSetup: SETUP })],
  ])("offers no setup on %s before setup", (_what, arrange) => {
    arrange();
    expect(document.querySelector("[data-needs-device]")).toBe(null);
    expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE]);
  });

  it.each([
    ["approves with no reason", "approve" as Verdict, "", ""],
    ["denies with the reason typed, trimmed", "deny" as Verdict, "  walk cleanup  ", "walk cleanup"],
  ])("decides from the footer at once: %s", async (_what, verdict, typed, sent) => {
    const answer = { ...hold, state: verdict === "approve" ? "approved" : "denied" };
    send.mockResolvedValue(answer);
    mount(hold);
    if (typed) await userEvent.type(screen.getByLabelText(REASON_LABEL), typed);
    await press(verdict);
    await waitFor(() => expect(send).toHaveBeenCalledWith(hold, verdict, sent));
    expect(send).toHaveBeenCalledTimes(1);
    // A decision that landed says so in a toast, hands the record back and closes the dialog.
    await waitFor(() => expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(verdict === "approve" ? approvedToast(hold) : deniedToast(hold)));
    expect(decided).toHaveBeenCalledWith(answer);
    expect(closed).toHaveBeenCalledWith(false);
    expect(screen.queryByRole("alert")).toBe(null);
  });

  it("disables both buttons and Close while the answer is out, with the spinner on the one pressed", async () => {
    let land: (r: ApprovalRow) => void = () => undefined;
    send.mockReturnValue(new Promise<ApprovalRow>((ok) => { land = ok; }));
    mount(hold);
    await press("deny");
    const deny = foot().getByRole("button", { name: DENY });
    const approve = foot().getByRole("button", { name: APPROVE });
    expect((deny as HTMLButtonElement).disabled).toBe(true);
    expect((approve as HTMLButtonElement).disabled).toBe(true);
    expect((foot().getByRole("button", { name: CLOSE }) as HTMLButtonElement).disabled).toBe(true);
    expect(deny.getAttribute("aria-busy")).toBe("true");
    expect(deny.querySelector("[data-spinner]")).not.toBe(null);
    expect(approve.querySelector("[data-spinner]")).toBe(null);
    // Escape does not close the dialog on an answer that is still out.
    await userEvent.keyboard("{Escape}");
    expect(closed).not.toHaveBeenCalled();
    land({ ...hold, state: "denied" });
    await waitFor(() => expect(closed).toHaveBeenCalledWith(false));
  });

  it.each([
    ["the server did not answer", "approve" as Verdict, new ApiError("unreachable", 0, true), APPROVE_REQUEST + " not confirmed. Straza did not answer. The change may have been saved. Reload to check before trying again."],
    ["the server failed", "deny" as Verdict, new ApiError("the approval store is unavailable", 500), DENY_REQUEST + " refused: the approval store is unavailable. Fix what it names, then try again."],
    ["the lane worded a refusal", "approve" as Verdict, new DecideRefusal("The reason is longer than 500 bytes. Shorten it."), APPROVE_REQUEST + " refused. The reason is longer than 500 bytes. Shorten it."],
  ])("keeps the dialog open with the refusal above the footer when %s, and the buttons work again", async (_what, verdict, err, text) => {
    send.mockRejectedValue(err);
    mount(hold);
    await press(verdict);
    const block = await screen.findByRole("alert");
    expect(block.textContent).toBe(text);
    // The refusal sits outside the scrolling body, just above the footer.
    expect(block.closest("[data-slot=dialog-footer]")).toBe(null);
    expect(one("[data-slot=dialog-footer]").previousElementSibling!.contains(block)).toBe(true);
    expect(closed).not.toHaveBeenCalled();
    expect(decided).not.toHaveBeenCalled();
    expect(screen.getByLabelText(REASON_LABEL)).toBeTruthy();
    expect((foot().getByRole("button", { name: APPROVE }) as HTMLButtonElement).disabled).toBe(false);
    expect((foot().getByRole("button", { name: DENY }) as HTMLButtonElement).disabled).toBe(false);
    // A second press starts clean.
    send.mockResolvedValue({ ...hold, state: "approved" });
    await press("approve");
    await waitFor(() => expect(closed).toHaveBeenCalledWith(false));
  });

  it("says nothing when the session ended, since the session module signs the person out", async () => {
    send.mockRejectedValue(new ApiError("session expired", 401));
    mount(hold);
    await press("approve");
    await waitFor(() => expect(send).toHaveBeenCalled());
    expect(screen.queryByRole("alert")).toBe(null);
    expect(document.querySelector("[data-final]")).toBe(null);
    expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE, DENY, APPROVE]);
  });

  // A stamp older than a day reads as a date, so the sentence the table builds now still matches when the case runs.
  const elsewhere: ApprovalRow = { ...base, state: "denied", decidedAt: "2026-10-01T09:00:00Z", decidedBy: "u-alice", decidedByName: "alice", channel: "phone", decidedReason: "not now" };
  it.each([
    ["it was decided elsewhere first and the lane re-reads it", "approve" as Verdict, new ApiError("already decided", 409), {}, alreadyDecided(elsewhere), true],
    ["it was decided elsewhere first and the lane cannot re-read", "deny" as Verdict, new ApiError("already decided", 409), { get: null }, refused(new ApiError("already decided", 409)), false],
    ["the window closed", "approve" as Verdict, new ApiError("the approval window closed", 410), {}, WINDOW_CLOSED, false],
    ["it is no longer the reader's", "deny" as Verdict, new ApiError("you are not an approver for this request.", 403), {}, notYours("you are not an approver for this request"), false],
    ["the lane says it is settled", "approve" as Verdict, new DecideRefusal("This request is gone, so there is nothing left to decide here.", true), {}, "This request is gone, so there is nothing left to decide here.", false],
  ])("ends the decision with a final answer and Close alone when %s", async (_what, verdict, err, lane, sentence, handsBack) => {
    send.mockRejectedValue(err);
    getOne.mockResolvedValue(elsewhere);
    mount(hold, true, false, {}, lane as Lane);
    await press(verdict);
    const strip = await waitFor(() => one("[data-final]"));
    expect(strip.textContent).toBe(sentence);
    expect(strip.textContent).not.toMatch(/^Not (approved|denied)/);
    expect(screen.queryByLabelText(REASON_LABEL)).toBe(null);
    expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE]);
    expect(screen.queryByRole("alert")).toBe(null);
    expect(closed).not.toHaveBeenCalled();
    if (handsBack) expect(decided).toHaveBeenCalledWith(elsewhere);
    else expect(decided).not.toHaveBeenCalled();
  });

  it.each([
    ["approving a gateway hold", () => hold, "approve" as Verdict, ABOUT_APPROVE],
    ["denying a gateway hold", () => hold, "deny" as Verdict, ABOUT_DENY],
    ["approving a hook hold", () => onHook, "approve" as Verdict, ABOUT_APPROVE],
    ["approving a ticket", () => ticket, "approve" as Verdict, ABOUT_APPROVE],
    ["denying a ticket", () => ticket, "deny" as Verdict, ABOUT_DENY],
  ])("explains what %s does behind the help of its box", async (_what, pick, verdict, label) => {
    const r = pick();
    mount(r, false);
    const b = within(one("[data-if=" + verdict + "]")).getByRole("button", { name: "Help: " + label });
    await userEvent.hover(b);
    await waitFor(() => {
      const id = b.getAttribute("aria-describedby");
      expect(id).toBeTruthy();
      expect(document.getElementById(id as string)?.textContent).toBe(decideHelp(verdict, r));
    });
  });
});

describe("what a yes and a no do", () => {
  const row = (patch: Partial<ApprovalRow>): ApprovalRow => ({ ...base, ...patch });
  const shell = { lane: "hook", summary: "shell.exec: make deploy ENV=staging" };

  it.each([
    ["a hold releases the one call", row({ bindingScope: "call" }), "1 hour", "The call runs now, once."],
    ["a hold says the same whatever the rule binds", row({ bindingScope: "tool_identity" }), null, "The call runs now, once."],
    ["a hold the hook raised runs on the agent's retry", row({ bindingScope: "call", ...shell }), null, "The call runs once, when the agent retries it soon after your yes."],
    ["a hook hold on an MCP call runs on the retry too", row({ lane: "hook" }), null, "The call runs once, when the agent retries it soon after your yes."],
    ["a ticket bound to the call covers that exact call", row({ class: "ticket", bindingScope: "call" }), "2 hours", "The agent's next try of this exact call runs once within 2 hours."],
    ["a ticket bound to a command covers that exact command", row({ class: "ticket", bindingScope: "call", ...shell }), "1 hour", "The agent's next try of this exact command runs once within 1 hour."],
    ["a ticket bound to the tool covers every call of it", row({ class: "ticket", bindingScope: "tool_identity" }), "2 hours", "The agent's next call of this tool runs once within 2 hours, whatever the parameters."],
    ["a ticket with no binding scope on the record claims neither", row({ class: "ticket" }), "2 hours", "The agent's next matching call runs once within 2 hours."],
    ["a command with no binding scope claims neither", row({ class: "ticket", ...shell }), "1 hour", "The agent's next matching call runs once within 1 hour."],
    ["an unread grant window is left out", row({ class: "ticket", bindingScope: "call" }), null, "The agent's next try of this exact call runs once."],
  ])("%s", (_what, r, grant, sentence) => {
    expect(approveDoes(r, grant)).toBe(sentence);
  });

  it.each([
    ["a denied hold stops the call on the line", row({}), "The call does not run. The agent is told who said no."],
    ["a denied ticket runs nothing", row({ class: "ticket" }), "Nothing runs. The agent is told who said no."],
  ])("%s", (_what, r, sentence) => {
    expect(denyDoes(r)).toBe(sentence);
  });

  it.each([
    ["a denied hold refuses the one call, and the next try asks again", row({}),
      "A denied hold refuses this one call, and the same call later raises a new request. The audit chain records who denied it and the reason."],
    ["a denied ticket keeps the person's call refused in every session until its window closes", row({ class: "ticket", expiresAt: "2026-10-28T12:00:00Z" }),
      "A denied ticket keeps the same call from joe-java-developer-agent refused in every session until " + absTime("2026-10-28T12:00:00Z") +
      ", and no new request opens for it. The audit chain records who denied it and the reason."],
  ])("the deny help: %s", (_what, r, sentence) => {
    expect(decideHelp("deny", r)).toBe(sentence);
  });

  it.each([
    ["a gateway hold releases the one call", row({}),
      "Approving a hold releases this one call. The same call later raises a new request."],
    ["a hook hold lets one retry run inside the rule's retry window", row(shell),
      "The hook refused the call at once and told the agent to retry it after a decision. Your yes lets one retry of the same call from the same session run, if it comes within the retry window the policy rule sets. A retry after that raises a new request."],
  ])("the approve help: %s", (_what, r, sentence) => {
    expect(decideHelp("approve", r)).toBe(sentence);
  });

  it.each([
    ["a gateway hold runs now", row({}), "Approved. get-sum runs now for joe-java-developer-agent."],
    ["a hook hold runs on the agent's retry", row(shell), "Approved. make deploy ENV=staging runs for joe-java-developer-agent if the agent retries it soon."],
    ["a ticket may run once, whatever the lane", row({ class: "ticket", ...shell }), "Approved. make deploy ENV=staging may run once for joe-java-developer-agent."],
  ])("the approved toast: %s", (_what, r, sentence) => {
    expect(approvedToast(r)).toBe(sentence);
  });

  it.each([
    ["a gateway hold", row({ bindingScope: "call" }), "If you approve: The call runs now, once. If you deny: The call does not run. The agent is told who said no."],
    ["a hook hold", row({ bindingScope: "call", ...shell }), "If you approve: The call runs once, when the agent retries it soon after your yes. If you deny: The call does not run. The agent is told who said no."],
  ])("joins both outcomes of %s into the one line a screen reader hears", (_what, r, sentence) => {
    expect(doesLine(r, null)).toBe(sentence);
  });

  it.each([
    ["a gateway hold says the call waits on the line", row({ createdAt: at(-28), expiresAt: at(92) }), DECIDE_BY_HELP_HOLD],
    ["a hook hold says the hook refused it and the agent retries", row({ createdAt: at(-28), expiresAt: at(92), ...shell }), DECIDE_BY_HELP_HOOK],
    ["a ticket says the agent will call again", row({ createdAt: at(-28), expiresAt: at(86400), class: "ticket" }), DECIDE_BY_HELP_TICKET],
  ])("the Decide by help of %s", async (_what, r, sentence) => {
    mount(r);
    const b = screen.getByRole("button", { name: "Help: " + FACT_DECIDE_BY });
    await userEvent.hover(b);
    await waitFor(() => {
      const id = b.getAttribute("aria-describedby");
      expect(id).toBeTruthy();
      expect(document.getElementById(id as string)?.textContent).toBe(sentence);
    });
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
    [{ state: "approved", class: "ticket", grantExpiresAt: "2099-01-01T00:00:00Z" }, /^Approved by alice from the console\. .+ left for the same call\.$/],
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
