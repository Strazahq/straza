import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftPublish } from "./draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type Draft, type DraftPublished, type DraftVerdict } from "@/lib/api";
import { publishDraft } from "@/lib/drafts-api";
import { changeCounts } from "@/lib/drafts-model";
import { ACK_MISSING, CUT_TYPED, NOBODY_UNTIL, NOT_PUBLISHED, goLive, signLine, typeLabel, typedWrong } from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { snapshot } from "@/lib/session";
import { CUT_RISK, DETAIL, HOST_RISK, REACH_RISK, VERDICT } from "@/test/drafts-fixture";

// The publish dialog: one line per risk,
// Cancel first, Publish always clickable and holding back while a line is
// left, the body echoing the verdict on screen, and every refusal shown as
// the server answered it.

vi.mock("@/lib/drafts-api", async (orig) => ({ ...(await orig<typeof import("@/lib/drafts-api")>()), publishDraft: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));

const PUBLISHED: DraftPublished = {
  draft: { ...DETAIL.draft, state: "published" },
  snapshot: "3be0a1ff",
  servers: [{ name: "github", change: "created", status: "starting" }],
  next: ["Each person runs straza connect github."],
};

function mount(verdict: DraftVerdict = VERDICT, draft: Draft = DETAIL.draft) {
  const onPublished = vi.fn();
  const onVerdict = vi.fn();
  const onOpenChange = vi.fn();
  const onRefused = vi.fn();
  render(
    <TooltipProvider>
      <DraftPublish draft={draft} verdict={verdict} open onOpenChange={onOpenChange} onPublished={onPublished} onVerdict={onVerdict} onRefused={onRefused} />
    </TooltipProvider>,
  );
  return { onPublished, onVerdict, onOpenChange, onRefused };
}

const dialog = () => screen.getByRole("dialog");
const publishButton = () => within(dialog()).getByRole("button", { name: "Publish" });
const line = (key: string) => document.querySelector('[data-ack="' + key + '"]') as HTMLElement;

describe("the publish dialog", () => {
  beforeEach(() => {
    vi.mocked(publishDraft).mockReset();
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: null });
  });

  it("asks one question per risk, says what goes live, and lands its focus on Cancel", () => {
    mount();
    expect(within(dialog()).getByRole("heading", { name: "Publish draft 41?" })).toBeTruthy();
    expect(dialog().textContent).toContain(goLive(changeCounts(DETAIL.draft.items)) + " " + NOBODY_UNTIL);
    expect(within(line("k-reach")).getByRole("checkbox", { name: REACH_RISK.sentence })).toBeTruthy();
    expect(within(line("k-host")).getByRole("textbox", { name: typeLabel("api.githubcopilot.com") })).toBeTruthy();
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

  it("names the agent the publisher sponsors and says the publish records who did it", () => {
    mount();
    expect(dialog().textContent).toContain(signLine("joe-java-developer-agent"));
  });

  it("holds a publish while a line is left, outlines the first one and focuses it", async () => {
    mount();
    await userEvent.click(publishButton());
    expect(publishDraft).not.toHaveBeenCalled();
    expect(screen.getByText(ACK_MISSING)).toBeTruthy();
    expect(line("k-host").getAttribute("data-need")).toBe("true");
    expect(document.activeElement).toBe(within(line("k-host")).getByRole("textbox"));
    await userEvent.type(within(line("k-host")).getByRole("textbox"), "api.githubcopilot.com");
    await userEvent.click(publishButton());
    expect(publishDraft).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(within(line("k-reach")).getByRole("checkbox"));
  });

  it("does not take a host typed wrong, and says what it must read", async () => {
    mount();
    await userEvent.type(within(line("k-host")).getByRole("textbox"), "api.github.com");
    await userEvent.click(within(line("k-reach")).getByRole("checkbox"));
    await userEvent.click(publishButton());
    expect(publishDraft).not.toHaveBeenCalled();
    expect(line("k-host").getAttribute("data-need")).toBe("true");
    expect(line("k-host").textContent).toContain(typedWrong("App/github", "api.githubcopilot.com"));
    expect(screen.queryByText(ACK_MISSING)).toBeNull();
  });

  it("says rows are hidden instead of claiming nobody gains access", () => {
    mount({ ...VERDICT, info: [{ code: "info.gains-hidden", class: "info", key: "k-h", sentence: "2 rows of who gains what are on servers you cannot read, so this view leaves them out. Ask an administrator for the scope apps:read to see them." }] });
    expect(dialog().textContent).not.toContain(NOBODY_UNTIL);
  });

  it("says a removal removes, never that it goes live", () => {
    mount({ ...VERDICT, risks: [] }, { ...DETAIL.draft, items: [DETAIL.draft.items[6]] });
    expect(dialog().textContent).toContain("Publishing removes 1 MCP server in one step, or nothing changes.");
    expect(dialog().textContent).not.toContain("live");
  });

  it("sends the revision, the digest and every key as the verdict gave them, and hands the answer on", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    const { onPublished } = mount();
    await userEvent.type(within(line("k-host")).getByRole("textbox"), "Api.GithubCopilot.com");
    await userEvent.click(within(line("k-reach")).getByRole("checkbox"));
    await userEvent.click(publishButton());
    expect(publishDraft).toHaveBeenCalledWith("41", { revision: 2, risk_digest: VERDICT.risk_digest, ticked: ["k-host", "k-reach"], typed: { "k-host": "Api.GithubCopilot.com" } });
    await waitFor(() => expect(onPublished).toHaveBeenCalledWith(PUBLISHED));
  });

  it("offers no input for a typed line whose words were cut for this reader, and shows the server's answer", async () => {
    const sentence = "Publish refused: draft 41 holds a risk on something you cannot read, so you cannot type the text that acknowledges it. Ask someone who can read everything the draft names to publish it.";
    vi.mocked(publishDraft).mockRejectedValue(new ApiError(sentence, 409, false, 0, { error: sentence }));
    mount({ ...VERDICT, risks: [CUT_RISK] });
    expect(within(line("k-cut")).queryByRole("textbox")).toBeNull();
    expect(line("k-cut").textContent).toContain(CUT_TYPED);
    await userEvent.click(publishButton());
    expect(publishDraft).toHaveBeenCalledWith("41", { revision: 2, risk_digest: VERDICT.risk_digest, ticked: [], typed: {} });
    const refusal = await screen.findByRole("alert");
    expect(refusal.textContent).toBe(NOT_PUBLISHED + " " + sentence);
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
    await userEvent.click(within(line("k-reach")).getByRole("checkbox"));
    await userEvent.click(publishButton());
    expect((await screen.findByRole("alert")).textContent).toBe(NOT_PUBLISHED + " " + sentence);
    expect(onPublished).not.toHaveBeenCalled();
    expect(onRefused).toHaveBeenCalledTimes(1);
    expect((onRefused.mock.calls[0][0] as ApiError).body).toEqual(body);
  });

  it("hands a 409's new verdict to the page, so a new line shows before the next try", async () => {
    const next: DraftVerdict = { ...VERDICT, risks: [HOST_RISK, REACH_RISK, { ...REACH_RISK, key: "k-new", sentence: "A new line appeared." }], risk_digest: "e".repeat(64) };
    const sentence = "Publish refused: A new line appeared. Acknowledge it, and publish again.";
    vi.mocked(publishDraft).mockRejectedValue(new ApiError(sentence, 409, false, 0, { error: sentence, verdict: next }));
    const { onVerdict } = mount();
    await userEvent.type(within(line("k-host")).getByRole("textbox"), "api.githubcopilot.com");
    await userEvent.click(within(line("k-reach")).getByRole("checkbox"));
    await userEvent.click(publishButton());
    await waitFor(() => expect(onVerdict).toHaveBeenCalledWith(next));
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
    await userEvent.click(publishButton());
    await waitFor(() => expect(onPublished).toHaveBeenCalledWith(PUBLISHED));
    expect(notify.ok).not.toHaveBeenCalled();
  });

  it("keeps its own title, no warnings and its own toast by default", async () => {
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    vi.mocked(notify.ok).mockClear();
    mount({ ...VERDICT, risks: [] });
    expect(dialog().querySelector("[data-warnings]")).toBeNull();
    await userEvent.click(publishButton());
    await waitFor(() => expect(notify.ok).toHaveBeenCalledWith("Draft 41 is live."));
  });
});
