import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftPublish } from "./draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type Draft, type DraftFinding, type DraftPublished, type DraftVerdict } from "@/lib/api";
import { publishDraft } from "@/lib/drafts-api";
import { changeCounts } from "@/lib/drafts-model";
import { ACK_MISSING, AGENT_LOOSENS, BLOCKED, NOBODY_UNTIL, NOT_PUBLISHED, agentWrote, goLive } from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { snapshot } from "@/lib/session";
import { ALICE, CUT_RISK, DETAIL, REACH_RISK, VERDICT } from "@/test/drafts-fixture";

// The publish dialog: one confirm block per kind of risk, Cancel first,
// Publish off until every block is confirmed, the body echoing each line's
// key and own typed text from the verdict on screen, and every refusal
// shown as the server answered it.

vi.mock("@/lib/drafts-api", async (orig) => ({ ...(await orig<typeof import("@/lib/drafts-api")>()), publishDraft: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));

const PUBLISHED: DraftPublished = {
  draft: { ...DETAIL.draft, state: "published" },
  snapshot: "3be0a1ff",
  servers: [{ name: "github", change: "created", status: "starting" }],
  next: ["Each person runs straza connect github."],
};

// The risks of a newer server, which groups them. The object type is an
// invented one, so a noun the console hardcoded would not match.
const risk = (key: string, code: string, object: string, over: Partial<DraftFinding> = {}): DraftFinding =>
  ({ code, class: "risk", ack: "tick", object, key, sentence: key + " widens access.", ...over });
const REMOVAL = risk("k-rm", "server.removal", "App/legacy-desk", {
  ack: "typed", typed: "legacy-desk",
  sentence: "Publishing removes legacy-desk: its access rows, its stored secrets and every person's connection go, with the roles it owns and its admin role.",
});
const set = (i: number) => risk("k-set-" + i, "policy.guardrail-changed", "PolicySet/desk-" + i + "-access", { ack: "typed", typed: "desk-" + i + "-access", group: "policy.guardrail-changed" });
const SETS = Array.from({ length: 20 }, (_, i) => set(i + 1));
const wider = (r: string) => risk("k-wide-" + r, "role.covers-wider", "Role/" + r, { group: "role.covers-wider", sentence: r + " will reach 2 more tickets on desk: T-1 and T-2." });
const LOOSEN = risk("k-loosen", "agent.guardrail", "PolicySet/agent-guardrails", {
  ack: "typed", typed: "agent-guardrails",
  sentence: "This draft turns off agent-guardrails, so joe-java-developer-agent deploys with no ticket.",
});

function mount(verdict: DraftVerdict = VERDICT, draft: Draft = DETAIL.draft) {
  const onPublished = vi.fn();
  const onVerdict = vi.fn();
  const onOpenChange = vi.fn();
  const onRefused = vi.fn();
  const ui = (v: DraftVerdict) => (
    <TooltipProvider>
      <DraftPublish draft={draft} verdict={v} open onOpenChange={onOpenChange} onPublished={onPublished} onVerdict={onVerdict} onRefused={onRefused} />
    </TooltipProvider>
  );
  const { rerender } = render(ui(verdict));
  return { onPublished, onVerdict, onOpenChange, onRefused, reverdict: (v: DraftVerdict) => rerender(ui(v)) };
}

const dialog = () => screen.getByRole("dialog");
const publishButton = () => within(dialog()).getByRole("button", { name: "Publish" }) as HTMLButtonElement;
const field = (name: string) => within(dialog()).getByRole("textbox", { name });
const blockWith = (text: string) => [...document.querySelectorAll<HTMLElement>("[data-block]")].find((el) => el.textContent?.includes(text)) as HTMLElement;
const sign = () => (document.querySelector("[data-sign]") as HTMLElement).textContent;

describe("the publish dialog", () => {
  beforeEach(() => {
    vi.mocked(publishDraft).mockReset();
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: null });
  });

  // An older server sends no group, so every line is its own block.
  it("asks line by line on a verdict with no groups, says what publishing does, and lands its focus on Cancel", () => {
    mount();
    expect(within(dialog()).getByRole("heading", { name: "Publish draft 41?" })).toBeTruthy();
    expect(dialog().textContent).toContain("Publishing widens access through 1 MCP server and widens 1 role, in one step, or nothing changes. " + NOBODY_UNTIL);
    expect(within(blockWith(REACH_RISK.sentence)).getByRole("checkbox", { name: REACH_RISK.sentence })).toBeTruthy();
    expect(field("Type api.githubcopilot.com to confirm").getAttribute("placeholder")).toBe("api.githubcopilot.com");
    expect(document.querySelectorAll("[data-block]").length).toBe(2);
    expect(document.activeElement).toBe(within(dialog()).getByRole("button", { name: "Cancel" }));
  });

  // The dialog opens from a mouse press on Save and publish, and a browser
  // draws no focus-visible ring on a focus that follows a mouse press, so
  // Cancel carries its ring on any focus.
  it("rings Cancel when it takes the focus the dialog opens with", () => {
    mount();
    const cancel = within(dialog()).getByRole("button", { name: "Cancel" });
    expect(document.activeElement).toBe(cancel);
    expect(cancel.className.split(" ")).toEqual(expect.arrayContaining(["focus:border-ring", "focus:ring-[3px]", "focus:ring-ring/50"]));
  });

  it("tells the agent's sponsor that another admin publishes it while second-person publishing is on", () => {
    mount();
    expect(sign()).toBe("You sponsor joe-java-developer-agent. When second-person publishing is on, another admin must publish this. The publish record names you.");
    expect(within(dialog()).getByRole("button", { name: "Help: Who the publish records" })).toBeTruthy();
  });

  it("keeps Publish off until every block is confirmed, says what is left and marks a match", async () => {
    mount();
    expect(publishButton().disabled).toBe(true);
    expect(screen.getByText(ACK_MISSING)).toBeTruthy();
    await userEvent.type(field("Type api.githubcopilot.com to confirm"), "API.githubcopilot.com");
    expect(blockWith("api.githubcopilot.com").querySelector("[data-matched]")).toBeTruthy();
    expect(publishButton().disabled).toBe(true);
    await userEvent.click(within(blockWith(REACH_RISK.sentence)).getByRole("checkbox"));
    expect(publishButton().disabled).toBe(false);
    expect(screen.queryByText(ACK_MISSING)).toBeNull();
  });

  it("asks one tick for a lone widening line, with the line as its label", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    const line = wider("desk-sales");
    mount({ ...VERDICT, risks: [line] });
    expect(dialog().textContent).toContain("Publishing widens 1 role, or nothing changes.");
    await userEvent.click(within(dialog()).getByRole("checkbox", { name: line.sentence }));
    await userEvent.click(publishButton());
    expect(publishDraft).toHaveBeenCalledWith("41", { revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-wide-desk-sales"], typed: {} });
  });

  it("types a removal by its name, a group by its count once, and ticks a widening group once", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    const roles = ["desk-a", "desk-b", "desk-c"].map(wider);
    mount({ ...VERDICT, risks: [...roles, ...SETS, REMOVAL] });
    expect(dialog().textContent).toContain("Publishing removes 1 MCP server, loosens 20 policy sets and widens 3 roles, in one step, or nothing changes.");
    const [rm, sets, wide] = [...document.querySelectorAll<HTMLElement>("[data-block]")];
    expect(within(rm).getByText("Removes 1 MCP server")).toBeTruthy();
    expect(rm.textContent).toContain(REMOVAL.sentence);
    expect(rm.getAttribute("data-loud")).toBe("true");
    expect(within(sets).getByText("Loosens 20 policy sets")).toBeTruthy();
    expect(within(sets).getByText("Show the 20 policy sets")).toBeTruthy();
    expect(sets.querySelectorAll("li").length).toBe(20);
    expect(within(wide).getByText("Show the 3 roles")).toBeTruthy();

    await userEvent.type(field("Type legacy-desk to confirm"), "legacy-desk");
    await userEvent.type(field("Type 20 to confirm"), "21");
    expect(sets.querySelector("[data-typed-wrong]")?.textContent).toBe("That is not 20. Type 20 to confirm.");
    await userEvent.click(within(wide).getByRole("checkbox", { name: "I read the 3 lines." }));
    expect(publishButton().disabled).toBe(true);
    await userEvent.clear(field("Type 20 to confirm"));
    await userEvent.type(field("Type 20 to confirm"), "20");
    expect(sets.querySelector("[data-typed-wrong]")).toBeNull();
    expect(sets.querySelector("[data-matched]")).toBeTruthy();
    await userEvent.click(publishButton());
    expect(publishDraft).toHaveBeenCalledWith("41", {
      revision: 2, risk_digest: VERDICT.risk_digest,
      ticked: [...roles, ...SETS, REMOVAL].map((f) => f.key),
      typed: Object.fromEntries([...SETS, REMOVAL].map((f) => [f.key, f.typed])),
    });
  });

  it("types a group of one line by that line's name", () => {
    mount({ ...VERDICT, risks: [set(7)] });
    expect(field("Type desk-7-access to confirm")).toBeTruthy();
    expect(within(dialog()).queryByRole("textbox", { name: "Type 1 to confirm" })).toBeNull();
  });

  it("keeps Publish off for a block with a line this reader cannot read, and says who can publish", async () => {
    mount({ ...VERDICT, risks: [REACH_RISK, CUT_RISK, { ...set(2), typed: undefined }, set(3)] });
    await userEvent.click(within(blockWith(REACH_RISK.sentence)).getByRole("checkbox"));
    const blocked = [...document.querySelectorAll("[data-blocked]")];
    expect(blocked.map((p) => p.textContent)).toEqual([BLOCKED, BLOCKED]);
    expect(within(dialog()).queryByRole("textbox")).toBeNull();
    expect(publishButton().disabled).toBe(true);
    expect(screen.queryByText(ACK_MISSING)).toBeNull();
    await userEvent.click(publishButton());
    expect(publishDraft).not.toHaveBeenCalled();
  });

  it("shows an agent's self-loosening loud, accepted by the set's name, and names the sponsor who accepts it", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    mount({ ...VERDICT, risks: [LOOSEN] });
    expect(dialog().textContent).toContain("Publishing loosens 1 policy set, or nothing changes.");
    const loud = blockWith(AGENT_LOOSENS);
    expect(loud.getAttribute("data-loud")).toBe("true");
    expect(loud.textContent).toContain(agentWrote("joe-java-developer-agent"));
    expect(agentWrote("joe-java-developer-agent")).toBe("joe-java-developer-agent wrote this draft to loosen its own rules.");
    expect(loud.textContent).toContain(LOOSEN.sentence);
    expect(sign()).toBe("You sponsor joe-java-developer-agent. When second-person publishing is on, another admin must publish this. The publish record names you as the person who accepted it.");
    await userEvent.type(field("Type agent-guardrails to accept it"), "agent-guardrail-x");
    expect(loud.querySelector("[data-typed-wrong]")?.textContent).toBe("That is not agent-guardrails. Type agent-guardrails to accept it.");
    await userEvent.clear(field("Type agent-guardrails to accept it"));
    await userEvent.type(field("Type agent-guardrails to accept it"), "agent-guardrails");
    await userEvent.click(publishButton());
    expect(publishDraft).toHaveBeenCalledWith("41", { revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-loosen"], typed: { "k-loosen": "agent-guardrails" } });
  });

  it("never says the publisher accepted a self-loosening this reader cannot type", () => {
    mount({ ...VERDICT, risks: [{ ...LOOSEN, typed: undefined }] });
    expect(blockWith(AGENT_LOOSENS).querySelector("[data-blocked]")).toBeTruthy();
    expect(sign()).toBe("You sponsor joe-java-developer-agent. When second-person publishing is on, another admin must publish this. The publish record names you.");
  });

  // The server reads a draft any agent revised as an agent's draft and bars
  // the sponsor of every agent author, so the dialog reads every author.
  it("names the agent that revised a person's draft and tells its sponsor", () => {
    const carol = { ...ALICE, user_id: "u-carol", username: "carol" };
    mount({ ...VERDICT, risks: [LOOSEN] }, { ...DETAIL.draft, authors: [carol, ...DETAIL.draft.authors] });
    expect(blockWith(AGENT_LOOSENS).textContent).toContain(agentWrote("joe-java-developer-agent"));
    expect(sign()).toBe("You sponsor joe-java-developer-agent. When second-person publishing is on, another admin must publish this. The publish record names you as the person who accepted it.");
  });

  it("asks for both fields of two groups with the same count", async () => {
    const hosts = ["a", "b"].map((s) => risk("k-http-" + s, "server.plain-http", "App/desk-" + s, { ack: "typed", typed: "desk-" + s + ".example", group: "server.plain-http" }));
    mount({ ...VERDICT, risks: [set(1), set(2), ...hosts] });
    const fields = within(dialog()).getAllByRole("textbox", { name: "Type 2 to confirm" });
    expect(fields.length).toBe(2);
    await userEvent.type(fields[0], "2");
    expect(publishButton().disabled).toBe(true);
    await userEvent.type(fields[1], "2");
    expect(publishButton().disabled).toBe(false);
  });

  it("asks for a removed server and a loosened set of the same name each in its own field", async () => {
    const removal = risk("k-rm-desk", "server.removal", "App/desk", { ack: "typed", typed: "desk" });
    mount({ ...VERDICT, risks: [removal, { ...LOOSEN, object: "PolicySet/desk", typed: "desk" }] });
    await userEvent.type(field("Type desk to confirm"), "desk");
    expect(publishButton().disabled).toBe(true);
    await userEvent.type(field("Type desk to accept it"), "desk");
    expect(publishButton().disabled).toBe(false);
  });

  it("names a publisher who sponsors nobody plainly", () => {
    mount({ ...VERDICT, risks: [LOOSEN] }, { ...DETAIL.draft, authors: [ALICE] });
    expect(sign()).toBe("The publish record names you as the person who accepted it.");
    expect(blockWith(AGENT_LOOSENS).textContent).toContain(agentWrote(null));
  });

  it("says rows are hidden instead of claiming nobody gains access", () => {
    mount({ ...VERDICT, info: [{ code: "info.gains-hidden", class: "info", key: "k-h", sentence: "2 rows under Access after publishing are on servers you cannot read, so this view leaves them out. Ask an administrator for the scope apps:read to see them." }] });
    expect(dialog().textContent).not.toContain(NOBODY_UNTIL);
  });

  it("says what each kind of item does when no line needs confirming, a removal never as going live", () => {
    mount({ ...VERDICT, risks: [] }, { ...DETAIL.draft, items: [DETAIL.draft.items[6]] });
    expect(dialog().textContent).toContain(goLive(changeCounts([DETAIL.draft.items[6]])));
    expect(dialog().textContent).toContain("Publishing removes 1 MCP server in one step, or nothing changes.");
    expect(dialog().textContent).not.toContain("live");
    expect(publishButton().disabled).toBe(false);
  });

  it("sends the revision, the digest and every key as the verdict gave them, and hands the answer on", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    const { onPublished } = mount();
    await userEvent.type(field("Type api.githubcopilot.com to confirm"), "Api.GithubCopilot.com");
    await userEvent.click(within(blockWith(REACH_RISK.sentence)).getByRole("checkbox"));
    await userEvent.click(publishButton());
    expect(publishDraft).toHaveBeenCalledWith("41", { revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-host", "k-reach"], typed: { "k-host": "api.githubcopilot.com" } });
    await waitFor(() => expect(onPublished).toHaveBeenCalledWith(PUBLISHED));
  });

  const refusals: [string, number, unknown][] = [
    ["the second-person rule", 409, { error: "You changed this draft, and this deployment needs a second person to publish a change that widens access (admin.secondPerson). Ask another administrator with standing over every object in it to review and publish it.", code: "second_person" }],
    ["the standing of an item", 403, { error: "You cannot publish draft 41: this draft also changes policy sets, which needs the scope policy:write." }],
    ["an agent", 403, { error: "This identity is an agent, and an agent cannot publish a draft, even with an administrator role. A person with standing over every object in the draft publishes it on the console or with strazactl." }],
  ];
  it.each(refusals)("shows the refusal of %s as the server answered it", async (_name, status, body) => {
    const sentence = (body as { error: string }).error;
    vi.mocked(publishDraft).mockRejectedValue(new ApiError(sentence, status, false, 0, body));
    const { onPublished, onRefused } = mount({ ...VERDICT, risks: [REACH_RISK] });
    await userEvent.click(within(blockWith(REACH_RISK.sentence)).getByRole("checkbox"));
    await userEvent.click(publishButton());
    expect((await screen.findByRole("alert")).textContent).toBe(NOT_PUBLISHED + " " + sentence);
    expect(onPublished).not.toHaveBeenCalled();
    expect(onRefused).toHaveBeenCalledTimes(1);
    expect((onRefused.mock.calls[0][0] as ApiError).body).toEqual(body);
  });

  it("hands a 409's new verdict to the page, and asks again for a group that gained a line", async () => {
    const roles = ["desk-a", "desk-b"].map(wider);
    const next: DraftVerdict = { ...VERDICT, risks: [...roles, wider("desk-new")], risk_digest: "e".repeat(64) };
    const sentence = "Publish refused: desk-new will reach 2 more tickets on desk: T-1 and T-2. Confirm it, then publish again.";
    vi.mocked(publishDraft).mockRejectedValue(new ApiError(sentence, 409, false, 0, { error: sentence, verdict: next }));
    const { onVerdict, reverdict } = mount({ ...VERDICT, risks: roles });
    await userEvent.click(within(dialog()).getByRole("checkbox", { name: "I read the 2 lines." }));
    await userEvent.click(publishButton());
    await waitFor(() => expect(onVerdict).toHaveBeenCalledWith(next));
    reverdict(next);
    expect((within(dialog()).getByRole("checkbox", { name: "I read the 3 lines." }) as HTMLInputElement).checked).toBe(false);
    expect(publishButton().disabled).toBe(true);
    expect((await screen.findByRole("alert")).textContent).toBe(NOT_PUBLISHED + " " + sentence);
  });

  it("takes an editor's title, lists the warnings under the lead, and leaves the toast to the editor when quiet", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    const onPublished = vi.fn();
    render(
      <TooltipProvider>
        <DraftPublish draft={DETAIL.draft} verdict={{ ...VERDICT, risks: [] }} open onOpenChange={vi.fn()} onPublished={onPublished} title="Publish this change to demo-tools?" warnings quiet />
      </TooltipProvider>,
    );
    expect(within(dialog()).getByRole("heading", { name: "Publish this change to demo-tools?" })).toBeTruthy();
    const warned = dialog().querySelector("[data-warnings]") as HTMLElement;
    expect([...warned.querySelectorAll("p")].map((p) => p.textContent)).toEqual(["Warnings", ...VERDICT.warnings.map((f) => f.sentence)]);
    expect(warned.querySelector("p")?.className).not.toContain("uppercase");
    await userEvent.click(publishButton());
    await waitFor(() => expect(onPublished).toHaveBeenCalledWith(PUBLISHED));
    expect(notify.ok).not.toHaveBeenCalled();
  });

  it("keeps its own title, no warnings and its own toast by default", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    vi.mocked(notify.ok).mockClear();
    mount({ ...VERDICT, risks: [] });
    expect(dialog().querySelector("[data-warnings]")).toBeNull();
    expect(sign()).toBe("You sponsor joe-java-developer-agent, which proposed this draft. The publish record names you.");
    await userEvent.click(publishButton());
    await waitFor(() => expect(notify.ok).toHaveBeenCalledWith("Draft 41 is live."));
  });
});
