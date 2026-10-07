import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftPage } from "./draft-page";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type DraftDetail, type DraftPublished } from "@/lib/api";
import { contactDraftServer, discardDraft, getDraft, publishDraft, rebaseDraft, revertDraft } from "@/lib/drafts-api";
import {
  CHECK_AGAIN,
  DISCARD,
  DISCARD_GO,
  DISCARD_KEEP_LIVE,
  DOCS_MASKED,
  KEEP_DRAFT_TEXT,
  KEEP_LIVE_TEXT,
  MAY_PUBLISH_ALL,
  MISSING_TITLE,
  NEXT_TITLE,
  OPEN_DRAFTS,
  PICKS_GO,
  PUBLISH_OPEN,
  SAYS_AGENT_HELP,
  STALE_TITLE,
  UNDO,
  WAITS_WHOLE,
  ackBar,
  checkedLine,
  discardKeep,
  discardedLine,
  docsFold,
  keepDraft,
  keepLive,
  publishedLine,
  reasonLine,
  revisionLine,
  saysTitle,
  serverLine,
  subjectDraft,
} from "@/lib/drafts-words";
import { liveSince } from "@/lib/policy-words";
import { navigate } from "@/lib/router";
import { readFailed } from "@/lib/say";
import { snapshot } from "@/lib/session";
import { absTime } from "@/lib/words";
import { ALICE, DETAIL, STALE, VERDICT, detailWith } from "@/test/drafts-fixture";

// The review page of one draft: read
// top to bottom, the same for every door, with the bar that publishes and
// discards, Check again when live state moved, and Undo once published.

vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  getDraft: vi.fn(),
  rebaseDraft: vi.fn(),
  discardDraft: vi.fn(),
  revertDraft: vi.fn(),
  contactDraftServer: vi.fn(),
  publishDraft: vi.fn(),
}));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const mount = (id = "41") => render(<TooltipProvider><DraftPage id={id} /></TooltipProvider>);
const title = () => screen.findByRole("heading", { level: 1, name: "Draft 41" });
const fact = (label: string) => document.querySelector('[data-fact="' + label + '"]') as HTMLElement;
const bar = () => document.querySelector("[data-publish-bar]") as HTMLElement | null;

describe("the review page", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: null });
    vi.mocked(getDraft).mockResolvedValue(DETAIL);
  });

  it("opens with the draft's state, the server's title and who drafted it through which door", async () => {
    mount();
    await title();
    expect(document.querySelector("[data-state-badge]")?.textContent).toBe("Ready to publish");
    expect(screen.getByText(DETAIL.draft.title)).toBeTruthy();
    expect(fact("Drafted by").textContent).toContain("joe-java-developer-agent");
    expect(fact("Drafted by").textContent).toContain("an agent, sponsored by you");
    expect(fact("How it came in").textContent).toContain("the straza app, from claude-code");
    expect(fact("Revisions").textContent).toContain(revisionLine(2, "joe-java-developer-agent", "straza-app", absTime(DETAIL.revisions[1].created_at), false));
    expect(fact("Checked").textContent).toContain(checkedLine(absTime(VERDICT.checked_at), "7f3a91c2"));
    expect(fact("Publishing needs").textContent).toContain("the server github needs the scope apps:write or the role straza-global-mcp-admin.");
    expect(fact("Publishing needs").textContent).toContain(MAY_PUBLISH_ALL);
  });

  it("shows the agent's note as unverified text, never as markup or a link", async () => {
    mount();
    await title();
    const says = document.querySelector("[data-says]") as HTMLElement;
    expect(says.textContent).toContain(saysTitle(true, "joe-java-developer-agent"));
    expect(says.querySelector("[data-unverified]")?.textContent).toBe("unverified");
    expect(says.querySelector("[data-note]")?.textContent).toBe(DETAIL.draft.note);
    expect(says.querySelector("a, b")).toBeNull();
    expect(within(says).getByRole("button", { name: "Help: " + saysTitle(true, "joe-java-developer-agent") })).toBeTruthy();
    expect(SAYS_AGENT_HELP).toContain("Written by the model");
  });

  it("names a person who wrote the note", async () => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { authors: [{ ...ALICE, username: "carol" }], door: "console" } }));
    mount();
    await title();
    expect((document.querySelector("[data-says]") as HTMLElement).textContent).toContain(saysTitle(false, "carol"));
  });

  it("lays out the strip, what changes, who gains what, the checks and the documents", async () => {
    mount();
    await title();
    expect(document.querySelector("[data-verdict-strip]")).not.toBeNull();
    for (const name of ["What changes", "Who gains what", "Checks", "The documents"]) expect(screen.getByRole("heading", { level: 2, name })).toBeTruthy();
    expect(screen.getByText(docsFold(8, 2))).toBeTruthy();
    expect(screen.getByText(DOCS_MASKED)).toBeTruthy();
  });

  it("says how many lines need an acknowledgment and opens the publish dialog", async () => {
    mount();
    await title();
    expect(bar()?.textContent).toContain(ackBar(2, 2));
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }));
    expect(await screen.findByRole("heading", { name: "Publish draft 41?" })).toBeTruthy();
  });

  it("gives a reader who may not publish the server's sentence and no Publish", async () => {
    const refusal = "You cannot publish draft 41: this draft also changes policy sets, which needs the scope policy:write.";
    vi.mocked(getDraft).mockResolvedValue(detailWith({ may_publish: false, publish_refusal: refusal }));
    mount();
    await title();
    expect(bar()?.textContent).toContain(refusal + " " + WAITS_WHOLE);
    expect(within(bar() as HTMLElement).queryByRole("button", { name: PUBLISH_OPEN })).toBeNull();
    expect(within(bar() as HTMLElement).getByRole("button", { name: DISCARD })).toBeTruthy();
  });

  it("publishes and shows the answer: the proof, the servers and the next steps", async () => {
    const answer: DraftPublished = {
      draft: { ...DETAIL.draft, state: "published", decided_at: "2026-09-24T10:51:00Z", decided_by: ALICE, snapshot: "3be0a1ff00" },
      snapshot: "3be0a1ff00",
      servers: [{ name: "github", change: "created", status: "starting" }],
      next: ["Each person runs straza connect github."],
    };
    vi.mocked(getDraft).mockResolvedValueOnce(detailWith({ verdict: { risks: [] } })).mockResolvedValue({ ...DETAIL, draft: answer.draft });
    vi.mocked(publishDraft).mockResolvedValue(answer);
    mount();
    await title();
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Publish" }));
    const proof = await waitFor(() => {
      const p = document.querySelector("[data-published]") as HTMLElement | null;
      if (!p) throw new Error("no proof yet");
      return p;
    });
    expect(proof.textContent).toContain(publishedLine("alice", absTime("2026-09-24T10:51:00Z"), "console", "41"));
    expect(proof.textContent).toContain(serverLine("github", "created", "starting"));
    expect(screen.getByText(NEXT_TITLE)).toBeTruthy();
    expect(screen.getByText("Each person runs straza connect github.")).toBeTruthy();
    expect(bar()).toBeNull();
  });

  // publishNow opens the page on an open draft, publishes it through the
  // dialog, and waits for the proof the publish answer draws.
  const publishNow = async () => {
    const answer: DraftPublished = { draft: { ...DETAIL.draft, state: "published", decided_at: "2026-09-24T10:51:00Z", decided_by: ALICE, snapshot: "3be0a1ff00" }, snapshot: "3be0a1ff00", servers: [], next: [] };
    vi.mocked(publishDraft).mockResolvedValue(answer);
    mount();
    await title();
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Publish" }));
    await waitFor(() => expect(document.querySelector("[data-published]")).not.toBeNull());
    return answer;
  };
  const lede = () => (document.querySelector("[data-checks-lede]") as HTMLElement).textContent;

  it("reads the draft again after a publish before it says anything about its stored check", async () => {
    let finish: (d: DraftDetail) => void = () => undefined;
    vi.mocked(getDraft).mockResolvedValueOnce(detailWith({ verdict: { risks: [] } })).mockImplementationOnce(() => new Promise((r) => { finish = r; }));
    const answer = await publishNow();
    expect(lede()).toBe("Reading draft 41.");
    expect(fact("Checked").textContent).toContain("Reading draft 41.");
    expect(document.body.textContent).not.toContain("stored no check");
    finish(detailWith({ draft: answer.draft, verdict: { ...NO_LINES, checked_at: CHECKED_AT, snapshot: "7f3a91c2d4e5" }, checks: { refused: 0, risks: 0, warnings: 0, unchecked: 0, revision: 2, checked_at: CHECKED_AT }, may_publish: false }));
    await waitFor(() => expect(lede()).toBe("Revision 2 was checked against the live state at " + absTime(CHECKED_AT) + ": nothing to acknowledge. Straza does not check a draft again once it is published."));
  });

  it("says the draft was published and could not be read again when the read after a publish fails", async () => {
    vi.mocked(getDraft).mockResolvedValueOnce(detailWith({ verdict: { risks: [] } })).mockRejectedValueOnce(new ApiError("strazad did not answer", 0, true));
    await publishNow();
    await waitFor(() => expect(lede()).toBe("Draft 41 is published. This page could not read it again, so its stored check is not shown. Reload to try again."));
    expect(fact("Checked").textContent).toContain("Draft 41 is published.");
    expect(document.body.textContent).toContain(readFailed(subjectDraft("41"), new ApiError("strazad did not answer", 0, true)));
    expect(document.body.textContent).not.toContain("stored no check");
  });

  // A draft that is not open is never checked again: the server answers a
  // verdict with no line and the counts of the check it stored for the
  // revision, so the page shows those counts and no strip, gains or lines.
  const NO_LINES = { refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
  const CHECKED_AT = "2026-09-24T10:44:00Z";
  const closedLeft = () => {
    expect(document.querySelector("[data-verdict-strip]")).toBeNull();
    expect(document.querySelector("[data-checks]")).toBeNull();
    expect(fact("Publishing needs")).toBeNull();
    expect(screen.queryByRole("heading", { level: 2, name: "Who gains what" })).toBeNull();
    for (const said of ["No object in it needs a grant to publish.", "Nothing refused.", "every read", "No role gains or loses a tool"]) expect(document.body.textContent).not.toContain(said);
  };

  it("shows a published draft's proof and the counts of its stored check, never a check run now, and undoes it into a new draft", async () => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({
      draft: { state: "published", decided_at: "2026-09-24T10:51:00Z", decided_by: ALICE, snapshot: "3be0a1ff00" },
      verdict: { ...NO_LINES, checked_at: CHECKED_AT, snapshot: "7f3a91c2d4e5" },
      checks: { refused: 0, risks: 2, warnings: 1, unchecked: 0, revision: 2, checked_at: CHECKED_AT },
      may_publish: false,
    }));
    vi.mocked(revertDraft).mockResolvedValue({ draft: { ...DETAIL.draft, id: "42", reverts: "41" }, verdict: VERDICT });
    mount();
    await title();
    const proof = document.querySelector("[data-published]") as HTMLElement;
    expect(proof.textContent).toContain(liveSince(absTime("2026-09-24T10:51:00Z"), "3be0a1ff"));
    expect((document.querySelector("[data-checks-lede]") as HTMLElement).textContent).toBe(
      "Revision 2 was checked against the live state at " + absTime(CHECKED_AT) + ": 2 widen access, 1 warning. Straza does not check a draft again once it is published.",
    );
    expect(fact("Checked").textContent).toContain(checkedLine(absTime(CHECKED_AT), "7f3a91c2"));
    closedLeft();
    await userEvent.click(within(proof).getByRole("button", { name: UNDO }));
    expect(revertDraft).toHaveBeenCalledWith("41", "");
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["42"]));
  });

  it.each(["discarded", "expired"] as const)("says when the server stored no check of the revision of a draft now %s", async (state) => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { state, decided_at: "2026-09-24T11:00:00Z", decided_by: ALICE }, verdict: { ...NO_LINES, checked_at: "", snapshot: "" }, may_publish: false }));
    mount();
    await title();
    expect(fact("Checked").textContent).toContain("Straza stored no check of revision 2.");
    expect((document.querySelector("[data-checks-lede]") as HTMLElement).textContent).toBe(
      "Straza stored no check of revision 2. Straza does not check a draft again once it is " + state + ".",
    );
    closedLeft();
  });

  // Being live is the one trust state among the closed drafts, so only the
  // proof of a publish wears the ok tone, and it wears it as a fill.
  it.each([
    ["published", "[data-published]", true],
    ["discarded", "[data-decided]", false],
    ["expired", "[data-decided]", false],
  ] as const)("fills the banner of a draft now %s in the ok tone only when it is the proof of a publish", async (state, banner, filled) => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { state, decided_at: "2026-09-24T11:00:00Z", decided_by: ALICE }, verdict: { ...NO_LINES, checked_at: "", snapshot: "" }, may_publish: false }));
    mount();
    await title();
    const cls = (document.querySelector(banner) as HTMLElement).className;
    expect([cls.includes("bg-ok-bg"), cls.includes("border-ok/40")]).toEqual([filled, filled]);
    expect(cls).not.toContain("border-l-ok");
  });

  it("leads the proof of a publish with the line that says the draft is live, at section size, on a strip with no left edge", async () => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { state: "published", decided_at: "2026-09-24T10:51:00Z", decided_by: ALICE, snapshot: "3be0a1ff00" }, verdict: { ...NO_LINES, checked_at: "", snapshot: "" }, may_publish: false }));
    mount();
    await title();
    const proof = document.querySelector("[data-published]") as HTMLElement;
    expect(proof.getAttribute("role")).toBe("status");
    expect(proof.className).toContain("text-base");
    expect(proof.className).not.toContain("border-l-");
    const lead = proof.querySelector("b") as HTMLElement;
    expect(lead.textContent).toBe(publishedLine("alice", absTime("2026-09-24T10:51:00Z"), "console", "41"));
    expect(lead.className).toContain("text-lg");
  });

  it("shows a discarded draft with who discarded it and why, and no bar", async () => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { state: "discarded", decided_at: "2026-09-24T11:00:00Z", decided_by: ALICE, decided_reason: "Not needed." } }));
    mount();
    await title();
    expect(document.querySelector("[data-decided]")?.textContent).toContain(discardedLine("alice", absTime("2026-09-24T11:00:00Z")));
    expect(document.querySelector("[data-decided]")?.textContent).toContain(reasonLine("Not needed."));
    expect(bar()).toBeNull();
  });

  it("discards with the reason and the revision the person read", async () => {
    vi.mocked(discardDraft).mockResolvedValue({ draft: { ...DETAIL.draft, state: "discarded" } });
    mount();
    await title();
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: DISCARD }));
    await userEvent.type(await screen.findByRole("textbox", { name: "Reason" }), "Not needed.");
    await userEvent.click(screen.getByRole("button", { name: DISCARD_GO }));
    expect(discardDraft).toHaveBeenCalledWith("41", "Not needed.", 2);
    await waitFor(() => expect(getDraft).toHaveBeenCalledTimes(2));
  });

  const fileDraft = (items: DraftDetail["draft"]["items"]) => detailWith({ draft: { door: "apps-directory", source: "/etc/straza/apps/demo-tools.yaml", items, authors: [{ user_id: "strazad", username: "strazad", agent: false, via: "file", client: "" }] } });

  it("names the apps directory's discard for what it keeps", async () => {
    vi.mocked(getDraft).mockResolvedValue(fileDraft([DETAIL.draft.items[1]]));
    mount();
    await title();
    expect(within(bar() as HTMLElement).getByRole("button", { name: DISCARD_KEEP_LIVE })).toBeTruthy();
    expect(fact("Drafted by").textContent).toContain("the file demo-tools.yaml");
    expect(fact("How it came in").textContent).toBe("How it came inthe apps directory, /etc/straza/apps/demo-tools.yaml");
  });

  it("names the server a file's removal keeps when it is discarded", async () => {
    vi.mocked(getDraft).mockResolvedValue(fileDraft([DETAIL.draft.items[6]]));
    mount();
    await title();
    expect(within(bar() as HTMLElement).getByRole("button", { name: discardKeep("old-tools") })).toBeTruthy();
  });

  it("contacts a proposed server and reads the draft again", async () => {
    vi.mocked(contactDraftServer).mockResolvedValue({ object: "App/github", host: "api.githubcopilot.com", contacted_at: "2026-09-24T10:47:00Z", tools: [] });
    mount();
    await title();
    await userEvent.click(screen.getByRole("button", { name: "Contact it now" }));
    expect(contactDraftServer).toHaveBeenCalledWith("41", "App/github");
    await waitFor(() => expect(getDraft).toHaveBeenCalledTimes(2));
  });

  describe("a draft whose live state moved", () => {
    beforeEach(() => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { refused: [STALE] } }));
    });

    it("says it is out of date, holds Publish, and checks it again", async () => {
      vi.mocked(rebaseDraft).mockResolvedValue({ draft: DETAIL.draft, verdict: VERDICT });
      mount();
      await title();
      expect(document.querySelector("[data-state-badge]")?.textContent).toBe("Out of date");
      const stale = document.querySelector("[data-stale]") as HTMLElement;
      expect(stale.textContent).toContain(STALE_TITLE);
      expect(stale.textContent).toContain(STALE.sentence);
      expect(within(bar() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }).getAttribute("aria-disabled")).toBe("true");
      await userEvent.click(within(stale).getByRole("button", { name: CHECK_AGAIN }));
      expect(rebaseDraft).toHaveBeenCalledWith("41", { revision: 2 });
      await waitFor(() => expect(getDraft).toHaveBeenCalledTimes(2));
    });

    it("asks for a pick where both sides changed a field, and sends it keyed by object and field", async () => {
      const conflicts = [{ object: "App/demo-tools", field: "straza.limits.rps", base: "5", draft: "20", live: "10" }];
      const sentence = "Draft 41 and live state both changed App/demo-tools straza.limits.rps since the draft was checked. Pick which value to keep, then check again.";
      vi.mocked(rebaseDraft).mockRejectedValueOnce(new ApiError(sentence, 409, false, 0, { error: sentence, conflicts })).mockResolvedValue({ draft: DETAIL.draft, verdict: VERDICT });
      mount();
      await title();
      await userEvent.click(within(document.querySelector("[data-stale]") as HTMLElement).getByRole("button", { name: CHECK_AGAIN }));
      const picks = await screen.findByRole("dialog");
      await userEvent.click(within(picks).getByRole("button", { name: PICKS_GO }));
      expect(rebaseDraft).toHaveBeenCalledTimes(1);
      await userEvent.click(within(picks).getByRole("radio", { name: keepDraft("20") }));
      await userEvent.click(within(picks).getByRole("button", { name: PICKS_GO }));
      expect(rebaseDraft).toHaveBeenLastCalledWith("41", { revision: 2, picks: { "App/demo-tools straza.limits.rps": "draft" } });
      await waitFor(() => expect(getDraft).toHaveBeenCalledTimes(2));
    });

    it("words an unset value and keeps a set's text in a fold under its choice", async () => {
      const conflicts = [
        { object: "App/demo-tools", field: "straza.limits.timeoutSeconds", base: "", draft: "30", live: "" },
        { object: "PolicySet/demo-tools-readers-access", field: "text", base: "a: 1\n", draft: "a: 2\nb: 3\n", live: "a: 1\nb: 4\n" },
      ];
      const sentence = "Draft 41 and live state both changed two fields since the draft was checked. Pick which value to keep, then check again.";
      vi.mocked(rebaseDraft).mockRejectedValue(new ApiError(sentence, 409, false, 0, { error: sentence, conflicts }));
      mount();
      await title();
      await userEvent.click(within(document.querySelector("[data-stale]") as HTMLElement).getByRole("button", { name: CHECK_AGAIN }));
      const picks = await screen.findByRole("dialog");
      const unset = picks.querySelector('[data-pick="App/demo-tools straza.limits.timeoutSeconds"]') as HTMLElement;
      expect(unset.textContent).toContain("It was unset when the draft was checked.");
      expect(within(unset).getByRole("radio", { name: keepLive("") })).toBeTruthy();
      const text = picks.querySelector('[data-pick="PolicySet/demo-tools-readers-access text"]') as HTMLElement;
      expect(within(text).getByRole("radio", { name: KEEP_DRAFT_TEXT })).toBeTruthy();
      expect(within(text).getByRole("radio", { name: KEEP_LIVE_TEXT })).toBeTruthy();
      expect([...text.querySelectorAll("pre")].map((p) => p.textContent)).toEqual(["a: 1\n", "a: 2\nb: 3\n", "a: 1\nb: 4\n"]);
      expect([...text.querySelectorAll("label")].every((l) => !l.textContent?.includes("b: 3"))).toBe(true);
    });

    it("shows a refused Check again in the server's words", async () => {
      const sentence = "Nothing that draft 41 holds changed on live state since it was checked, so there is nothing to check again.";
      vi.mocked(rebaseDraft).mockRejectedValue(new ApiError(sentence, 409, false, 0, { error: sentence }));
      mount();
      await title();
      await userEvent.click(within(document.querySelector("[data-stale]") as HTMLElement).getByRole("button", { name: CHECK_AGAIN }));
      expect((await screen.findByRole("alert")).textContent).toContain(sentence);
    });
  });

  it("says a draft is not here in the server's words", async () => {
    const sentence = "There is no draft 9. List the drafts with strazactl drafts list, or open Drafts on the console.";
    vi.mocked(getDraft).mockRejectedValue(new ApiError(sentence, 404));
    mount("9");
    expect(await screen.findByText(MISSING_TITLE)).toBeTruthy();
    expect(screen.getByText(sentence)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: OPEN_DRAFTS }));
    expect(navigate).toHaveBeenCalledWith("drafts");
  });

  it("says why a draft could not be read", async () => {
    const err = new ApiError("Straza could not read the drafts. Try again, and read the strazad log if it keeps failing.", 500);
    vi.mocked(getDraft).mockRejectedValue(err);
    mount();
    expect((await screen.findByRole("status")).textContent).toContain(readFailed(subjectDraft("41"), err));
  });
});
