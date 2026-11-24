import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftPublish } from "./draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type Draft, type DraftFinding, type DraftPublished, type DraftVerdict } from "@/lib/api";
import { publishDraft } from "@/lib/drafts-api";
import { changeCounts } from "@/lib/drafts-model";
import { ACK_MISSING, AGENT_LOOSENS, AGENT_WIDENS, BLOCKED, NOT_PUBLISHED, agentWrote, goLive } from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { snapshot } from "@/lib/session";
import { ALICE, COVERS_DETAIL, CUT_RISK, DETAIL, DRAFT_86, DRAFT_87, DRAFT_91, ELEVENLABS_TOOLS, HOST_RISK, OWN_REACH_RISK, REACH_RISK, VERDICT, oldServer, role } from "@/test/drafts-fixture";

// The publish dialog: one confirm block per kind of risk, each a title
// unless another block has the same, the objects its lines name, the facts
// the Checks table draws for them or the server's sentence, and its tick or
// field, Cancel first, Publish off until every block is confirmed, the body
// echoing each line's key and own typed text from the verdict on screen,
// and every refusal shown as the server answered it.

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
const tags = (b: HTMLElement) => [...b.querySelectorAll("[data-block-objects] > *")].map((t) => t.textContent);
const facts = (b: HTMLElement) => [...b.querySelectorAll("[data-block-fact]")].map((f) => f.getAttribute("data-block-fact"));
const titleOf = (b: HTMLElement) => b.querySelector("[data-block-title]")?.textContent || null;
const sign = () => document.querySelector("[data-sign]")?.textContent ?? null;
const lead = () => document.querySelector("[data-slot=dialog-description]")?.textContent ?? null;
const undo = () => document.querySelector("[data-undo]")?.textContent ?? null;
// LEAD is what publishing the fixture's draft does to its objects, which the
// lead says whatever its lines widen.
const LEAD = "Publishing adds 1 MCP server, 1 role and 1 policy, changes 1 MCP server, 2 roles and 1 policy and removes 1 MCP server.";
// tickOf is the box whose label holds text, which a tick's label must say.
const tickOf = (text: string) => within(dialog()).getByRole("checkbox", { name: (n) => n.includes(text) }) as HTMLInputElement;

describe("the publish dialog", () => {
  beforeEach(() => {
    vi.mocked(publishDraft).mockReset();
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: null });
  });

  // An older server sends no group, so every line is its own block, and no
  // row, so each block says its line in the server's sentence.
  it("asks line by line on a verdict with no groups, says what publishing does, and lands its focus on Cancel", () => {
    mount();
    expect(within(dialog()).getByRole("heading", { name: "Publish draft 41?" })).toBeTruthy();
    expect(lead()).toBe(LEAD);
    expect([...(document.querySelector("[data-nobody]") as HTMLElement).children].map((t) => t.textContent)).toEqual(["github-readers", "nobody yet", "github-writers", "nobody yet"]);
    expect(within(blockWith("github-readers")).getByRole("checkbox")).toBe(tickOf(REACH_RISK.sentence));
    expect(blockWith("api.githubcopilot.com").textContent).toContain(HOST_RISK.sentence);
    expect(field("Type api.githubcopilot.com to confirm").getAttribute("placeholder")).toBe("api.githubcopilot.com");
    expect(document.querySelectorAll("[data-block]").length).toBe(2);
    expect(document.querySelectorAll("[data-block-title]").length).toBe(0);
    expect(undo()).toBe("Publishing the old version again does not undo it.");
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
    expect(sign()).toBe("You sponsor joe-java-developer-agent. When second-person publishing is on, another admin must publish this.");
  });

  it("keeps Publish off until every block is confirmed, says what is left and marks a match", async () => {
    mount();
    expect(publishButton().disabled).toBe(true);
    expect(screen.getByText(ACK_MISSING)).toBeTruthy();
    await userEvent.type(field("Type api.githubcopilot.com to confirm"), "API.githubcopilot.com");
    expect(blockWith("api.githubcopilot.com").querySelector("[data-matched]")).toBeTruthy();
    expect(publishButton().disabled).toBe(true);
    await userEvent.click(within(blockWith("github-readers")).getByRole("checkbox"));
    expect(publishButton().disabled).toBe(false);
    expect(screen.queryByText(ACK_MISSING)).toBeNull();
  });

  it("asks one tick for a lone widening line, labelled with its object and its sentence, and says publishing the old version undoes it", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    const line = wider("desk-sales");
    mount({ ...VERDICT, risks: [line] });
    const b = blockWith("desk-sales");
    expect(titleOf(b)).toBeNull();
    expect(tags(b)).toEqual(["desk-sales"]);
    expect(tickOf(line.sentence)).toBe(within(b).getByRole("checkbox"));
    expect(undo()).toBe("Publishing the old version again undoes it.");
    await userEvent.click(tickOf(line.sentence));
    await userEvent.click(publishButton());
    expect(publishDraft).toHaveBeenCalledWith("41", { revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-wide-desk-sales"], typed: {} });
  });

  it("types a removal by its name, a group by its count once, and ticks a widening group once", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    const roles = ["desk-a", "desk-b", "desk-c"].map(wider);
    mount({ ...VERDICT, risks: [...roles, ...SETS, REMOVAL] });
    expect(lead()).toBe(LEAD);
    const [rm, sets, wide] = [...document.querySelectorAll<HTMLElement>("[data-block]")];
    expect(titleOf(rm)).toBeNull();
    expect(tags(rm)).toEqual(["legacy-desk"]);
    expect(rm.textContent).toContain(REMOVAL.sentence);
    expect(rm.getAttribute("data-loud")).toBe("true");
    expect(titleOf(sets)).toBe("Loosens 20 policies");
    expect(tags(sets)).toEqual(SETS.map((f) => (f.object as string).slice("PolicySet/".length)));
    expect(titleOf(wide)).toBe("Widens 3 roles");
    expect(tags(wide)).toEqual(["desk-a", "desk-b", "desk-c"]);

    await userEvent.type(field("Type legacy-desk to confirm"), "legacy-desk");
    await userEvent.type(field("Type 20 to confirm"), "21");
    expect(sets.querySelector("[data-typed-wrong]")?.textContent).toBe("That is not 20. Type 20 to confirm.");
    await userEvent.click(within(wide).getByRole("checkbox"));
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
    await userEvent.click(within(blockWith("github-readers")).getByRole("checkbox"));
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
    const loud = blockWith(AGENT_LOOSENS);
    expect(titleOf(loud)).toBe(AGENT_LOOSENS);
    expect(loud.getAttribute("data-loud")).toBe("true");
    expect(loud.textContent).toContain(agentWrote("joe-java-developer-agent"));
    expect(agentWrote("joe-java-developer-agent")).toBe("joe-java-developer-agent wrote this draft to loosen its own rules.");
    expect(loud.textContent).toContain(LOOSEN.sentence);
    expect(sign()).toBe("You sponsor joe-java-developer-agent. When second-person publishing is on, another admin must publish this.");
    await userEvent.type(field("Type agent-guardrails to accept it"), "agent-guardrail-x");
    expect(loud.querySelector("[data-typed-wrong]")?.textContent).toBe("That is not agent-guardrails. Type agent-guardrails to accept it.");
    await userEvent.clear(field("Type agent-guardrails to accept it"));
    await userEvent.type(field("Type agent-guardrails to accept it"), "agent-guardrails");
    await userEvent.click(publishButton());
    expect(publishDraft).toHaveBeenCalledWith("41", { revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-loosen"], typed: { "k-loosen": "agent-guardrails" } });
  });

  it("shows an agent's widening of its own reach loud, accepted by the server's name", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    mount({ ...VERDICT, risks: [OWN_REACH_RISK] });
    const loud = blockWith(AGENT_WIDENS);
    expect(loud.getAttribute("data-loud")).toBe("true");
    expect(loud.textContent).toContain("joe-java-developer-agent wrote this draft to widen its own reach.");
    expect(loud.textContent).toContain(OWN_REACH_RISK.sentence);
    expect(sign()).toBe("You sponsor joe-java-developer-agent. When second-person publishing is on, another admin must publish this.");
    expect(publishButton().disabled).toBe(true);
    await userEvent.type(field("Type github to accept it"), "github");
    await userEvent.click(publishButton());
    expect(publishDraft).toHaveBeenCalledWith("41", { revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-own-reach"], typed: { "k-own-reach": "github" } });
  });

  it("never says the publisher accepted a self-loosening this reader cannot type", () => {
    mount({ ...VERDICT, risks: [{ ...LOOSEN, typed: undefined }] });
    expect(blockWith(AGENT_LOOSENS).querySelector("[data-blocked]")).toBeTruthy();
    expect(sign()).toBe("You sponsor joe-java-developer-agent. When second-person publishing is on, another admin must publish this.");
  });

  // The server reads a draft any agent revised as an agent's draft and bars
  // the sponsor of every agent author, so the dialog reads every author.
  it("names the agent that revised a person's draft and tells its sponsor", () => {
    const carol = { ...ALICE, user_id: "u-carol", username: "carol" };
    mount({ ...VERDICT, risks: [LOOSEN] }, { ...DETAIL.draft, authors: [carol, ...DETAIL.draft.authors] });
    expect(blockWith(AGENT_LOOSENS).textContent).toContain(agentWrote("joe-java-developer-agent"));
    expect(sign()).toBe("You sponsor joe-java-developer-agent. When second-person publishing is on, another admin must publish this.");
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
    expect(sign()).toBeNull();
    expect(blockWith(AGENT_LOOSENS).textContent).toContain(agentWrote(null));
  });

  it("leaves out that nobody holds the roles for a reader the server hid rows from", () => {
    mount({ ...VERDICT, info: [{ code: "info.gains-hidden", class: "info", key: "k-h", sentence: "2 rows under Access after publishing are on servers you cannot read, so this view leaves them out. Ask an administrator for the scope apps:read to see them." }] });
    expect(document.querySelector("[data-nobody]")).toBeNull();
  });

  it("says what each kind of item does when no line needs confirming, a removal never as going live", () => {
    mount({ ...VERDICT, risks: [] }, { ...DETAIL.draft, items: [DETAIL.draft.items[6]] });
    expect(lead()).toBe(goLive(changeCounts([DETAIL.draft.items[6]])));
    expect(lead()).toBe("Publishing removes 1 MCP server.");
    expect(dialog().textContent).not.toContain("live");
    expect(publishButton().disabled).toBe(false);
  });

  it("sends the revision, the digest and every key as the verdict gave them, and hands the answer on", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    const { onPublished } = mount();
    await userEvent.type(field("Type api.githubcopilot.com to confirm"), "Api.GithubCopilot.com");
    await userEvent.click(within(blockWith("github-readers")).getByRole("checkbox"));
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
    await userEvent.click(within(blockWith("github-readers")).getByRole("checkbox"));
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
    await userEvent.click(within(dialog()).getByRole("checkbox"));
    await userEvent.click(publishButton());
    await waitFor(() => expect(onVerdict).toHaveBeenCalledWith(next));
    reverdict(next);
    expect((within(dialog()).getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
    expect(publishButton().disabled).toBe(true);
    expect((await screen.findByRole("alert")).textContent).toBe(NOT_PUBLISHED + " " + sentence);
  });

  // The three drafts of the fixture: a lead that counts the objects by
  // what publishing does to them, each block a title, the objects as tags,
  // the rows the Checks table draws for its lines and the field, and none
  // of the server's sentences, which the rows say.
  const mocks: [string, typeof DRAFT_91, string, [string, string[], string[]][]][] = [
    ["91", DRAFT_91, "Publishing turns off 1 policy.", [["Loosens 1 policy", ["demo-tools-readers-access", "turned off"], []], ["Widens 2 roles", ["demo-tools-readers", "operator"], ["demo-tools/get-env", "demo-tools/get-sum"]]]],
    ["86", DRAFT_86, "Publishing adds 1 role and 1 policy.", [["Widens access through 1 policy", ["ux-walk-audio-access"], ["elevenlabs/" + ELEVENLABS_TOOLS.join(",")]]]],
    ["87", DRAFT_87, "Publishing changes 1 policy.", [["Loosens 1 policy", ["agent-guardrails", "applies to you"], ["deploy-ticket", "no-rm-rf"]]]],
  ];
  it.each(mocks)("draws the blocks of draft %s as a title under a lead of counts, the objects as tags, the rows of the Checks table and the field, with no sentence of the server", (_id, d, wantLead, want) => {
    render(
      <TooltipProvider>
        <DraftPublish draft={d.draft} verdict={d.verdict} live={d.live} open onOpenChange={vi.fn()} onPublished={vi.fn()} />
      </TooltipProvider>,
    );
    expect(lead()).toBe(wantLead);
    const blocks = [...document.querySelectorAll<HTMLElement>("[data-block]")];
    expect(blocks.map((b) => [titleOf(b), tags(b), facts(b)])).toEqual(want);
    for (const f of d.verdict.risks) expect(dialog().textContent).not.toContain(f.sentence);
  });

  it("draws a row's outcome now and after publishing with the gate's facts, and a covers line as the objects that join", () => {
    const { unmount } = render(
      <TooltipProvider>
        <DraftPublish draft={DRAFT_91.draft} verdict={DRAFT_91.verdict} live={DRAFT_91.live} open onOpenChange={vi.fn()} onPublished={vi.fn()} />
      </TooltipProvider>,
    );
    const env = document.querySelector('[data-block-fact="demo-tools/get-env"]') as HTMLElement;
    expect([...env.children].map((c) => c.textContent)).toEqual(["get-env", "on", "demo-tools", "needs approval", "the agent's sponsor", "ticket", "a day", "", "allowed"]);
    unmount();
    mount({ ...COVERS_DETAIL.verdict }, COVERS_DETAIL.draft);
    const covers = document.querySelector('[data-block-fact="tracker-triage/tracker"]') as HTMLElement;
    expect(covers.textContent).toContain("Tickets");
    expect([...covers.querySelectorAll("[data-added]")].map((e) => e.getAttribute("data-name"))).toEqual(["T-2", "T-3"]);
    expect(covers.querySelectorAll("[data-name]").length).toBe(2);
    expect(dialog().textContent).not.toContain(COVERS_DETAIL.verdict.risks[0].sentence);
  });

  // A draft that adds one object and changes nothing else is titled by its
  // name, over an editor's title, and has no lead. Every other draft keeps
  // its title and a lead of counts with no tail.
  const NEW_GITHUB = DETAIL.draft.items[0];
  const titles: [string, Draft["items"], string | undefined, string, string | null][] = [
    ["adds one object", [NEW_GITHUB], "Publish this change to github?", "Publish github?", null],
    ["adds one object on the draft page", [NEW_GITHUB], undefined, "Publish github?", null],
    ["adds one object and changes another", [NEW_GITHUB, DETAIL.draft.items[1]], "Publish this change to github?", "Publish this change to github?", "Publishing adds 1 MCP server and changes 1 MCP server."],
    ["changes one object", [DETAIL.draft.items[1]], "Publish this change to demo-tools?", "Publish this change to demo-tools?", "Publishing changes 1 MCP server."],
    ["removes one object", [DETAIL.draft.items[6]], undefined, "Publish draft 41?", "Publishing removes 1 MCP server."],
  ];
  it.each(titles)("titles a draft that %s and says its lead only where the title names no added object", (_name, items, title, want, wantLead) => {
    render(
      <TooltipProvider>
        <DraftPublish draft={{ ...DETAIL.draft, items }} verdict={{ ...VERDICT, risks: [], gains: [] }} open onOpenChange={vi.fn()} onPublished={vi.fn()} title={title} />
      </TooltipProvider>,
    );
    expect(within(dialog()).getByRole("heading").textContent).toBe(want);
    expect(lead()).toBe(wantLead);
    expect(dialog().hasAttribute("aria-describedby")).toBe(wantLead !== null);
  });

  // The confirm of a new server with a shared secret at a new host: each
  // block says its own line, so the host is never typed blind, and the two
  // lines that would both read Widens access through 1 MCP server carry no
  // heading.
  it("says the fact of each block of a new server, types the host beside its line and ticks the box its line labels", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    const credential = risk("k-cred", "server.credential-host", "App/github", { ack: "typed", typed: "api.githubcopilot.com", group: "server.credential-host", sentence: "Straza will send github's secret to api.githubcopilot.com, a host github has not used before." });
    const host = risk("k-new-host", "server.new-host", "App/github", { group: "server.new-host", sentence: "github will be reached at api.githubcopilot.com, a host no server uses today." });
    mount({ ...VERDICT, risks: [credential, host], gains: [] }, { ...DETAIL.draft, items: [NEW_GITHUB] });
    expect(within(dialog()).getByRole("heading").textContent).toBe("Publish github?");
    expect(lead()).toBeNull();
    const [typed, ticked] = [...document.querySelectorAll<HTMLElement>("[data-block]")];
    expect([titleOf(typed), titleOf(ticked)]).toEqual([null, null]);
    expect(facts(typed)).toEqual(["server.credential-host"]);
    expect(typed.textContent).toContain(credential.sentence + "Type api.githubcopilot.com to confirm");
    expect(tickOf(host.sentence)).toBe(within(ticked).getByRole("checkbox"));
    expect(dialog().textContent).not.toContain("Tick the box");
    expect(undo()).toBeNull();
    await userEvent.type(field("Type api.githubcopilot.com to confirm"), "api.githubcopilot.com");
    await userEvent.click(tickOf(host.sentence));
    await userEvent.click(publishButton());
    expect(publishDraft).toHaveBeenCalledWith("41", { revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-cred", "k-new-host"], typed: { "k-cred": "api.githubcopilot.com" } });
  });

  // Two kinds of line on one role would both read Widens 1 role, so neither
  // block shows that heading and each says its own tools row.
  it("never shows two blocks with the same heading, and tells them apart by their rows", () => {
    const gain = (tool: string, before: "needs-approval" | "not-reachable", after: "runs" | "needs-approval") => ({ role: "dev", server: "demo-tools", tool, holders_count: 2, before, after });
    const ungated = risk("k-ungated", "access.ungated", "Role/dev", { ack: "typed", typed: "dev", group: "access.ungated", rows: [{ server: "demo-tools", tool: "get-env" }] });
    const gated = risk("k-gated", "access.gated", "Role/dev", { group: "access.gated", rows: [{ server: "demo-tools", tool: "get-sum" }] });
    const dev = { kind: "Role" as const, name: "dev", op: "put" as const, doc: role("dev", "demo-tools", ["get-env", "get-sum"]), existed: true };
    mount({ ...VERDICT, risks: [ungated, gated], gains: [gain("get-env", "needs-approval", "runs"), gain("get-sum", "not-reachable", "needs-approval")] }, { ...DETAIL.draft, items: [dev] });
    const blocks = [...document.querySelectorAll<HTMLElement>("[data-block]")];
    expect(blocks.map((b) => [titleOf(b), tags(b), facts(b)])).toEqual([[null, ["dev"], ["demo-tools/get-env"]], [null, ["dev"], ["demo-tools/get-sum"]]]);
    expect(tickOf("get-sum")).toBe(within(blocks[1]).getByRole("checkbox"));
    expect(dialog().textContent).not.toContain(ungated.sentence);
  });

  // Publishing the old version again only means something for an object
  // that exists today, so a draft that only adds objects leaves the line out.
  const undos: [string, typeof DRAFT_91, string | null][] = [
    ["adds a role and a policy", DRAFT_86, null],
    ["turns a policy off", DRAFT_91, "Publishing the old version again does not undo it."],
  ];
  it.each(undos)("says whether publishing the old version undoes a draft that %s", (_name, d, want) => {
    render(
      <TooltipProvider>
        <DraftPublish draft={d.draft} verdict={d.verdict} live={d.live} open onOpenChange={vi.fn()} onPublished={vi.fn()} />
      </TooltipProvider>,
    );
    expect(undo()).toBe(want);
  });

  it("names the proposer a changed policy applies to when someone else publishes it", () => {
    vi.mocked(snapshot).mockReturnValue({ user: "bob", roles: [], grants: "full", expiresIn: 300, sessionID: null });
    mount(DRAFT_87.verdict, DRAFT_87.draft);
    expect(document.querySelector("[data-applies]")?.textContent).toBe("applies to alice");
  });

  // What the dialog sends is the body publishBody built before the blocks
  // lost their sentences: each line's own key in the verdict's order, and
  // each typed line's own typed text, whatever the person typed for a
  // count. The expected bodies are written out by hand from the fixture.
  const bodies: [string, typeof DRAFT_91, () => Promise<void>, unknown][] = [
    ["91", DRAFT_91, async () => {
      await userEvent.type(field("Type demo-tools-readers-access to confirm"), "demo-tools-readers-access");
      await userEvent.type(field("Type 2 to confirm"), "2");
    }, { revision: 1, risk_digest: DRAFT_91.verdict.risk_digest, ticked: ["k91-guard", "k91-demo-tools-readers", "k91-operator"], typed: { "k91-guard": "demo-tools-readers-access", "k91-demo-tools-readers": "demo-tools-readers", "k91-operator": "operator" } }],
    ["86", DRAFT_86, async () => {
      await userEvent.click(within(dialog()).getByRole("checkbox"));
    }, { revision: 1, risk_digest: DRAFT_86.verdict.risk_digest, ticked: ["k86-local"], typed: {} }],
    ["87", DRAFT_87, async () => {
      await userEvent.type(field("Type agent-guardrails to confirm"), "Agent-Guardrails");
    }, { revision: 1, risk_digest: DRAFT_87.verdict.risk_digest, ticked: ["k87-guard"], typed: { "k87-guard": "agent-guardrails" } }],
  ];
  it.each(bodies)("sends the same publish body for draft %s, from a newer server and from an older one", async (_id, d, confirm, want) => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    for (const detail of [d, oldServer(d)]) {
      vi.mocked(publishDraft).mockClear();
      const { unmount } = render(
        <TooltipProvider>
          <DraftPublish draft={detail.draft} verdict={detail.verdict} live={detail.live} open onOpenChange={vi.fn()} onPublished={vi.fn()} />
        </TooltipProvider>,
      );
      await confirm();
      await userEvent.click(publishButton());
      expect(publishDraft).toHaveBeenCalledTimes(1);
      expect(publishDraft).toHaveBeenCalledWith(d.draft.id, want);
      unmount();
    }
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
    expect(sign()).toBe("You sponsor joe-java-developer-agent, which proposed this draft.");
    await userEvent.click(publishButton());
    await waitFor(() => expect(notify.ok).toHaveBeenCalledWith("Draft 41 is live."));
  });
});
