import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftPage } from "./draft-page";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type DraftDetail, type DraftFinding, type DraftPrincipal, type DraftPublished, getConfig, listApps, listRoles, recheckApp, setSecret } from "@/lib/api";
import { contactDraftServer, discardDraft, getDraft, publishDraft, publishedFor, rebaseDraft, revertDraft } from "@/lib/drafts-api";
import {
  BAND_PILL,
  CANCEL,
  CHECK_AGAIN,
  DISCARD,
  DISCARD_GO,
  FILE_REFUSED,
  GAINS_HIDDEN,
  KEEP_DRAFT_TEXT,
  KEEP_LIVE_TEXT,
  MISSING_TITLE,
  NEXT_TITLE,
  OPEN_DRAFTS,
  PUBLISH_NOT_YET,
  PUBLISH_OPEN,
  SAYS_AGENT_HELP,
  SECRET_CHECKS_LINE,
  SECRET_LINE,
  STALE_BAR,
  STALE_TITLE,
  UNDO,
  UNDO_HELP,
  WAITS_WHOLE,
  checkedLine,
  discardUnlinks,
  expiresLine,
  holdersLose,
  keepDraft,
  keepLive,
  revisionLine,
  saysPersonHelp,
  serverLine,
  subjectDraft,
  GAINS_TITLE,
} from "@/lib/drafts-words";
import { REEVALUATE, liveSince } from "@/lib/policy-words";
import { navigate } from "@/lib/router";
import { readFailed } from "@/lib/say";
import { snapshot } from "@/lib/session";
import { absTime, agoWord, dayWord } from "@/lib/words";
import { ALICE, COVERS_DETAIL, CUT_RISK, DETAIL, DRAFT_86, DRAFT_87, DRAFT_91, GATE_LOST, LOOSEN_RISK, OWN_REACH_RISK, PUBLISHED_DETAIL, STALE, VERDICT, detailWith, readersSet } from "@/test/drafts-fixture";

// The review page of one draft, read top to bottom: the head with its row
// of pills and Details, the banners, the band that publishes and discards,
// In short, the note, the cards, access after publishing where a holder
// gains or loses, the checks, and the strip of a decided draft.

vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  getDraft: vi.fn(),
  rebaseDraft: vi.fn(),
  discardDraft: vi.fn(),
  revertDraft: vi.fn(),
  contactDraftServer: vi.fn(),
  publishDraft: vi.fn(),
  publishedFor: vi.fn(),
}));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), getConfig: vi.fn(), listApps: vi.fn(), listRoles: vi.fn(), setSecret: vi.fn(), recheckApp: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const mount = (id = "41") => render(<TooltipProvider><DraftPage id={id} /></TooltipProvider>);
const title = (name = DETAIL.draft.title) => screen.findByRole("heading", { level: 1, name });
const fact = (label: string) => document.querySelector('[data-fact="' + label + '"]') as HTMLElement | null;
const band = () => document.querySelector("[data-publish-band]") as HTMLElement | null;
const meta = () => document.querySelector("[data-meta]") as HTMLElement;
const details = () => userEvent.click(screen.getByRole("button", { name: "Details" }));
const pillTexts = (el: Element | null) => [...(el?.querySelectorAll("span.rounded-pill") || [])].map((p) => p.textContent);
const checksHead = () => document.querySelector("[data-checks-head]") as HTMLElement;
// READABLE is the fixture's draft without its withheld item, so In short
// can read every item.
const READABLE = () => detailWith({ draft: { items: DETAIL.draft.items.slice(0, 7) } });
const NO_LINES = { refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const CHECKED_AT = "2026-09-24T10:44:00Z";

describe("the review page", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: null });
    vi.mocked(getDraft).mockResolvedValue(DETAIL);
    vi.mocked(listApps).mockResolvedValue([]);
    vi.mocked(listRoles).mockResolvedValue([]);
    vi.mocked(publishedFor).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(getConfig).mockResolvedValue({ apps: { default_remote_rps: 25 } });
  });

  describe("the head", () => {
    it("heads the page with the server's title, its state, a linked Drafts crumb, and a row of pills", async () => {
      mount();
      await title();
      expect(document.querySelector("[data-state-badge]")?.textContent).toBe("Ready");
      expect(document.querySelector("[data-draft-id]")).toBeNull();
      const crumb = screen.getByRole("navigation", { name: "Breadcrumb" });
      expect(crumb.textContent).toBe("Straza/Drafts/Draft 41");
      expect(within(crumb).getByRole("link", { name: "Drafts" })).toBeTruthy();
      expect(meta().querySelector("[data-meta-draft]")?.textContent).toBe("Draft 41");
      const agent = meta().querySelector('[data-author="agent"]') as HTMLElement;
      expect(agent.textContent).toBe("joe-java-developer-agent");
      expect(agent.className).toContain("bg-teal-bg");
      expect(meta().querySelector("[data-author-pill]")?.textContent).toBe("you sponsor it");
      const ago = meta().querySelector("[data-meta-ago]") as HTMLElement;
      expect([ago.textContent, ago.getAttribute("title")]).toEqual([agoWord(DETAIL.draft.updated_at), absTime(DETAIL.draft.updated_at)]);
      expect(document.querySelector("[data-facts]")).toBeNull();
    });

    const p = (over: Partial<DraftPrincipal>): DraftPrincipal => ({ user_id: "u", username: "carol", agent: false, via: "session", client: "console", ...over });
    const authors: [string, DraftPrincipal, string, string, string][] = [
      ["an agent another person sponsors", p({ username: "sam", agent: true, sponsor: "bob" }), "agent", "sam", "sponsored by bob"],
      ["an agent with no sponsor", p({ username: "sam", agent: true }), "agent", "sam", "no sponsor"],
      ["a file of the apps directory", p({ username: "strazad", via: "file" }), "file", "demo-tools.yaml", "from the apps directory"],
      ["an admin API token", p({ username: "ci-bundles", via: "api-token" }), "api-token", "ci-bundles", "from the apps directory"],
      ["the reader", p({ username: "alice" }), "person", "alice", "from the apps directory"],
      ["another person", p({}), "person", "carol", "from the apps directory"],
    ];
    it.each(authors)("names %s with a tag and the pill after it", async (_name, author, kind, tag, pill) => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { authors: [author], door: "apps-directory", source: "/etc/straza/apps/demo-tools.yaml" } }));
      mount();
      await title();
      expect(meta().querySelector('[data-author="' + kind + '"]')?.textContent).toBe(tag);
      expect(meta().querySelector("[data-author-pill]")?.textContent).toBe(kind === "api-token" ? "an admin API token" : pill);
    });

    it("names Straza's upgrade with two pills and no tag", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { authors: [p({ username: "strazad", via: "upgrade" })], door: "console" } }));
      mount();
      await title();
      expect(meta().querySelector('[data-author="upgrade"]')?.textContent).toBe("Straza's upgrade");
      expect(meta().querySelector("[data-author-pill]")?.textContent).toBe("a saved edit moved into a draft");
      expect(meta().querySelector(".bg-card[data-author]")).toBeNull();
    });

    it.each([
      ["published", { decided_at: "2026-09-24T11:00:00Z" }, "2026-09-24T11:00:00Z"],
      ["discarded", { decided_at: "2026-09-24T11:00:00Z" }, "2026-09-24T11:00:00Z"],
      ["expired", { expires_at: "2026-10-08T10:42:00Z" }, "2026-10-08T10:42:00Z"],
    ] as const)("says how long ago a draft was %s", async (state, at, stamp) => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { state, ...at }, verdict: NO_LINES, may_publish: false }));
      mount();
      await title();
      const ago = meta().querySelector("[data-meta-ago]") as HTMLElement;
      expect([ago.textContent, ago.getAttribute("title")]).toEqual([state + " " + agoWord(stamp), absTime(stamp)]);
    });

    it("opens the facts under Details as pills, each with its full reading on hover", async () => {
      mount();
      await title();
      await details();
      expect(screen.getByRole("button", { name: "Details" }).getAttribute("aria-expanded")).toBe("true");
      expect(pillTexts(fact("Came from"))).toEqual(["the straza app, from claude-code"]);
      const revision = (fact("Revision") as HTMLElement).querySelector("span.rounded-pill") as HTMLElement;
      expect([revision.textContent, revision.getAttribute("title")]).toEqual(["2", revisionLine(2, "joe-java-developer-agent", "straza-app", absTime(DETAIL.revisions[1].created_at), false)]);
      const checked = (fact("Last checked") as HTMLElement).querySelector("span.rounded-pill") as HTMLElement;
      expect([checked.textContent, checked.getAttribute("title")]).toEqual([agoWord(CHECKED_AT), checkedLine(absTime(CHECKED_AT), "7f3a91c2")]);
      expect(pillTexts(fact("May publish"))).toEqual(["you"]);
      const expires = (fact("Expires") as HTMLElement).querySelector("span.rounded-pill") as HTMLElement;
      expect([expires.textContent, expires.getAttribute("title")]).toEqual([dayWord(DETAIL.draft.expires_at), expiresLine(absTime(DETAIL.draft.expires_at))]);
      expect(fact("Undoes")).toBeNull();
      await details();
      expect(document.querySelector("[data-facts]")).toBeNull();
    });

    it("says a reader who may not publish waits, with the grants publishing needs", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ may_publish: false, publish_refusal: "You cannot publish draft 41." }));
      mount();
      await title();
      await details();
      const may = fact("May publish") as HTMLElement;
      expect(pillTexts(may)).toEqual(["not you"]);
      expect(may.textContent).toContain("The server github needs the scope apps:write or the role straza-global-mcp-admin. The policy github-writers-access needs the scope policy:write. " + WAITS_WHOLE);
    });

    it("gives the server's reason where no object needs a grant and the reader still may not publish", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ may_publish: false, publish_refusal: "You changed this draft, so another admin publishes it.", verdict: { needs: [] } }));
      mount();
      await title();
      await details();
      expect((fact("May publish") as HTMLElement).textContent).toBe("May publishnot youYou changed this draft, so another admin publishes it. " + WAITS_WHOLE);
    });

    it("links an undo draft to the draft it takes back", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { reverts: "40", title: "Undo draft 40" } }));
      mount();
      await title("Undo draft 40");
      await details();
      await userEvent.click(within(fact("Undoes") as HTMLElement).getByRole("link", { name: "Draft 40" }));
      expect(navigate).toHaveBeenCalledWith("drafts", ["40"]);
    });

    it("keeps the head and the crumb while the draft reads, when it is missing, and when it fails", async () => {
      let finish: (d: DraftDetail) => void = () => undefined;
      vi.mocked(getDraft).mockImplementationOnce(() => new Promise((r) => { finish = r; }));
      const { unmount } = mount();
      expect(screen.getByRole("heading", { level: 1, name: "Draft 41" })).toBeTruthy();
      expect(screen.getByText("Reading draft 41.")).toBeTruthy();
      expect(within(screen.getByRole("navigation", { name: "Breadcrumb" })).getByRole("link", { name: "Drafts" })).toBeTruthy();
      finish(DETAIL);
      await title();
      unmount();
      const sentence = "There is no draft 9. List the drafts with strazactl drafts list, or open Drafts on the console.";
      vi.mocked(getDraft).mockRejectedValue(new ApiError(sentence, 404));
      mount("9");
      expect(await screen.findByText(MISSING_TITLE)).toBeTruthy();
      expect(screen.getByRole("navigation", { name: "Breadcrumb" }).textContent).toBe("Straza/Drafts/Draft 9");
      expect(screen.getByText(sentence)).toBeTruthy();
      expect(document.querySelector("[data-page-head]")?.textContent).not.toContain("wait here");
      await userEvent.click(screen.getByRole("button", { name: OPEN_DRAFTS }));
      expect(navigate).toHaveBeenCalledWith("drafts");
    });

    it("says why a draft could not be read", async () => {
      const err = new ApiError("Straza could not read the drafts. Try again, and read the strazad log if it keeps failing.", 500);
      vi.mocked(getDraft).mockRejectedValue(err);
      mount();
      expect((await screen.findByRole("status")).textContent).toContain(readFailed(subjectDraft("41"), err));
      expect(screen.getByRole("navigation", { name: "Breadcrumb" }).textContent).toBe("Straza/Drafts/Draft 41");
    });

    it("keeps the page and says above it when a reload fails", async () => {
      vi.mocked(contactDraftServer).mockResolvedValue({ object: "App/github", host: "api.githubcopilot.com", contacted_at: "2026-09-24T10:47:00Z", tools: [] });
      vi.mocked(getDraft).mockResolvedValueOnce(DETAIL).mockRejectedValueOnce(new ApiError("strazad did not answer", 0, true));
      mount();
      await title();
      await userEvent.click(screen.getByRole("button", { name: "Contact it now" }));
      await waitFor(() => expect(document.querySelector("[data-fetch-error]")).not.toBeNull());
      expect(document.querySelector("[data-fetch-error]")?.textContent).toContain(readFailed(subjectDraft("41"), new ApiError("strazad did not answer", 0, true)));
      expect(band()).not.toBeNull();
    });
  });

  describe("the banners", () => {
    it("opens with a red warning naming the agent when its draft loosens the rules that bind it", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [LOOSEN_RISK, GATE_LOST] } }));
      mount();
      await title();
      const warn = document.querySelector("[data-agent-loosens]") as HTMLElement;
      expect(warn.querySelector("b")?.textContent).toBe("joe-java-developer-agent wrote this draft to loosen its own rules.");
      expect(warn.textContent).toContain(LOOSEN_RISK.sentence + " " + GATE_LOST.sentence);
      expect(warn.className).toContain("bg-danger-bg");
      expect(document.querySelector("[data-agent-loosens], [data-publish-band], [data-in-draft], [data-short]")).toBe(warn);
    });

    it("warns the same way when the agent's draft widens what the agent reaches, and says how to accept it", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [OWN_REACH_RISK] } }));
      mount();
      await title();
      const warn = document.querySelector("[data-agent-loosens]") as HTMLElement;
      expect(warn.querySelector("b")?.textContent).toBe("joe-java-developer-agent wrote this draft to widen its own reach.");
      expect(warn.textContent).toContain(OWN_REACH_RISK.sentence);
      const line = document.querySelector('[data-group="risk"] [data-finding="agent.own-reach"]') as HTMLElement;
      expect(line.textContent).toContain(OWN_REACH_RISK.sentence);
      expect(line.querySelector("[data-ack-words]")?.textContent).toBe("Type github when you publish to accept it.");
    });

    it("names both when the agent's draft loosens its rules and widens its reach", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [OWN_REACH_RISK, LOOSEN_RISK] } }));
      mount();
      await title();
      expect(document.querySelector("[data-agent-loosens] b")?.textContent).toBe("joe-java-developer-agent wrote this draft to loosen its own rules and widen its own reach.");
    });

    // A line cut for this reader carries no object, so it matches no other
    // line, and the warning says its sentence once.
    it("says a cut line of the warning once", async () => {
      const cut = (code: string, key: string) => ({ ...CUT_RISK, code, key });
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [cut("agent.guardrail", "k-c1"), cut("policy.guardrail-changed", "k-c2")] } }));
      mount();
      await title();
      expect((document.querySelector("[data-agent-loosens]") as HTMLElement).textContent?.split(CUT_RISK.sentence).length).toBe(2);
    });

    it.each([
      ["a gate line alone", { risks: [GATE_LOST] }],
      ["the same code refused", { risks: [], refused: [{ ...LOOSEN_RISK, class: "refused" as const, ack: undefined, typed: undefined }] }],
      ["no line of the code", {}],
    ])("shows no agent warning for %s", async (_name, verdict) => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict }));
      mount();
      await title();
      expect(document.querySelector("[data-agent-loosens]")).toBeNull();
    });

    it("says an apps directory file did not read, and shows the table of items in place of In short", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { door: "apps-directory", items: [], refusal: "line 3: mapping values are not allowed here" }, verdict: NO_LINES }));
      mount();
      await title();
      const banner = document.querySelector("[data-file-refused]") as HTMLElement;
      expect(banner.textContent).toBe(FILE_REFUSED + "line 3: mapping values are not allowed here");
      expect(document.querySelector("[data-short]")).toBeNull();
      expect(document.querySelector("[data-in-draft]")).not.toBeNull();
      expect(document.querySelector("[data-object-count]")).toBeNull();
    });
  });

  describe("the band", () => {
    const pill = () => (band() as HTMLElement).querySelector("[data-band-pill]")?.textContent;
    const chips = () => [...(band() as HTMLElement).querySelectorAll("[data-band-count]")].map((c) => [c.textContent, c.getAttribute("data-tone")]);

    it("counts what the check found, says the confirmations come with Publish, and opens the publish dialog", async () => {
      mount();
      await title();
      expect((band() as HTMLElement).className).toContain("sticky");
      expect(chips()).toEqual([["2 to confirm", "warn"], ["2 warnings", "plain"], ["1 not checked", "unknown"]]);
      expect(pill()).toBe(BAND_PILL.risks);
      expect(within(band() as HTMLElement).getByRole("button", { name: DISCARD })).toBeTruthy();
      await userEvent.click(within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }));
      expect(await screen.findByRole("heading", { name: "Publish draft 41?" })).toBeTruthy();
    });

    it("lets the head scroll away, so the band alone stays at the top", async () => {
      mount();
      await title();
      expect((document.querySelector("[data-page-head]") as HTMLElement).className).not.toContain("sticky");
      expect((band() as HTMLElement).className).toContain("sticky");
    });

    it.each([PUBLISH_OPEN, DISCARD])("puts the focus back on %s when its dialog closes", async (name) => {
      mount();
      await title();
      const opener = within(band() as HTMLElement).getByRole("button", { name });
      await userEvent.click(opener);
      await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: CANCEL }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      await waitFor(() => expect(document.activeElement).toBe(opener));
    });

    it("counts the confirm blocks, not the lines", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [LOOSEN_RISK, GATE_LOST], warnings: [], unchecked: [] } }));
      mount();
      await title();
      expect(chips()).toEqual([["1 to confirm", "warn"]]);
    });

    it("says the checks passed when nothing stands before Publish", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [], warnings: [], unchecked: [] } }));
      mount();
      await title();
      expect(chips()).toEqual([["1 check passed", "ok"]]);
      expect(pill()).toBe(BAND_PILL.none);
    });

    it("shows no pill when warnings are all there is, since their chip says them", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [], unchecked: [] } }));
      mount();
      await title();
      expect(chips()).toEqual([["2 warnings", "plain"]]);
      expect(pill()).toBeUndefined();
    });

    it("holds Publish for a refused draft, with the reason as its title", async () => {
      const refused = { code: "names.taken", class: "refused" as const, object: "App/github", key: "k-r1", sentence: "The name github is taken." };
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { refused: [refused] } }));
      mount();
      await title();
      expect(document.querySelector("[data-state-badge]")?.textContent).toBe("Refused");
      expect(chips()[0]).toEqual(["1 refused", "danger"]);
      expect(pill()).toBe(BAND_PILL.refused);
      const publish = within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN });
      expect([publish.getAttribute("aria-disabled"), publish.getAttribute("title")]).toEqual(["true", PUBLISH_NOT_YET]);
      await userEvent.click(publish);
      expect(screen.queryByRole("dialog")).toBeNull();
    });

    it("gives a reader who may not publish the pill that it waits, the grants behind its help, and no Publish", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ may_publish: false, publish_refusal: "You cannot publish draft 41." }));
      mount();
      await title();
      expect(pill()).toBe(BAND_PILL.waits);
      expect(within(band() as HTMLElement).queryByRole("button", { name: PUBLISH_OPEN })).toBeNull();
      expect(within(band() as HTMLElement).getByRole("button", { name: DISCARD })).toBeTruthy();
      await userEvent.hover(within(band() as HTMLElement).getByRole("button", { name: "Help: May publish" }));
      expect((await screen.findByRole("tooltip")).textContent).toContain(WAITS_WHOLE + " The server github needs the scope apps:write or the role straza-global-mcp-admin.");
    });

    it("keeps the warning that holders lose tools right under the band", async () => {
      mount();
      await title();
      const loses = document.querySelector("[data-loses]") as HTMLElement;
      expect(loses.textContent).toBe(holdersLose(["demo-tools-readers"]));
      expect(loses.className).toContain("text-warn");
      expect(band()?.nextElementSibling).toBe(loses);
    });

    it("lets its parts wrap at a narrow width", async () => {
      mount();
      await title();
      expect((band() as HTMLElement).className).toContain("flex-wrap");
    });
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
      expect((band() as HTMLElement).querySelector("[data-band-pill]")?.textContent).toBe(BAND_PILL.stale);
      const stale = document.querySelector("[data-stale]") as HTMLElement;
      expect(stale.textContent).toContain(STALE_TITLE);
      expect(stale.textContent).toContain(STALE.sentence);
      const publish = within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN });
      expect([publish.getAttribute("aria-disabled"), publish.getAttribute("title")]).toEqual(["true", STALE_BAR]);
      await userEvent.click(publish);
      expect(screen.queryByRole("dialog")).toBeNull();
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
      await userEvent.click(within(picks).getByRole("button", { name: CHECK_AGAIN }));
      expect(rebaseDraft).toHaveBeenCalledTimes(1);
      await userEvent.click(within(picks).getByRole("radio", { name: keepDraft("20") }));
      await userEvent.click(within(picks).getByRole("button", { name: CHECK_AGAIN }));
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

  describe("In short and the note", () => {
    it("sums up what publishing does in a card under the band, before the note", async () => {
      vi.mocked(getDraft).mockResolvedValue(READABLE());
      mount();
      await title();
      const short = document.querySelector("[data-short]") as HTMLElement;
      expect(within(short).getByRole("heading", { level: 2, name: "In short" })).toBeTruthy();
      expect([...short.querySelectorAll("dt")].map((dt) => dt.textContent).filter(Boolean)).toEqual(["Adds", "Changes", "Removes", "Also removes", "Gives", "Affects"]);
      expect(document.querySelector("[data-in-draft]")).toBeNull();
      expect(document.querySelector("[data-short], [data-says]")).toBe(short);
    });

    it("says a policy that is off live comes back on, in In short, on its card and in the publish dialog", async () => {
      const live = { ...DETAIL.live, "PolicySet/demo-tools-readers-access": { op: "off" as const, doc: readersSet(true) } };
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { items: [DETAIL.draft.items[5]] }, verdict: { risks: [] }, live }));
      mount();
      await title();
      expect([...(document.querySelector("[data-short]") as HTMLElement).querySelectorAll("dt")].map((dt) => dt.textContent).filter(Boolean)[0]).toBe("Turns on");
      expect(document.querySelector('[data-item="PolicySet/demo-tools-readers-access"] [data-change]')?.textContent).toBe("turned on");
      await userEvent.click(within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }));
      expect((await screen.findByRole("dialog")).textContent).toContain("Publishing turns on 1 policy.");
    });

    it("lists every item in the table instead when an item is withheld, and a row moves focus to its card", async () => {
      mount();
      await title();
      expect(document.querySelector("[data-short]")).toBeNull();
      const rows = [...document.querySelectorAll("[data-row-item]")] as HTMLElement[];
      expect(rows.map((r) => r.getAttribute("data-row-item"))).toEqual(DETAIL.draft.items.map((it) => it.kind + "/" + it.name));
      const row = (o: string) => document.querySelector('[data-row-item="' + o + '"]') as HTMLElement;
      expect([...row("Role/github-readers").querySelectorAll("td")].map((td) => td.textContent)).toEqual(["Application role", "github-readersowned by github", "new", "Reaches 2 tools of github.nobody yet"]);
      await userEvent.click(row("PolicySet/github-writers-access"));
      expect(document.activeElement).toBe(document.querySelector('[data-item="PolicySet/github-writers-access"]'));
      await userEvent.click(within(row("App/demo-tools")).getByRole("button", { name: "demo-tools" }));
      expect(document.activeElement).toBe(document.querySelector('[data-item="App/demo-tools"]'));
    });

    it("shows the agent's note as unverified text in its own words, never as markup or a link", async () => {
      mount();
      await title();
      const says = document.querySelector("[data-says]") as HTMLElement;
      expect(says.querySelector('[data-author="agent"]')?.textContent).toBe("joe-java-developer-agent");
      expect(says.textContent).toContain("in its own words");
      expect(says.querySelector("[data-unverified]")?.getAttribute("data-tone")).toBe("plain");
      expect(says.querySelector("[data-note]")?.textContent).toBe(DETAIL.draft.note);
      expect(says.querySelector("a, b")).toBeNull();
      await userEvent.hover(within(says).getByRole("button", { name: "Help: What the agent says" }));
      expect((await screen.findByRole("tooltip")).textContent).toContain(SAYS_AGENT_HELP);
      expect(within(says).queryByRole("button", { name: "Show all" })).toBeNull();
    });

    it("names a person's note in their own words, and opens a long one", async () => {
      const note = "Line one of the reason.\nLine two of the reason.";
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { authors: [{ ...ALICE, username: "carol" }], door: "console", note } }));
      mount();
      await title();
      const says = document.querySelector("[data-says]") as HTMLElement;
      expect(says.textContent).toContain("in their own words");
      expect(within(says).getByRole("button", { name: "Help: What carol says" })).toBeTruthy();
      expect(saysPersonHelp("carol")).toContain("carol");
      expect((says.querySelector("[data-note]") as HTMLElement).className).toContain("line-clamp-2");
      await userEvent.click(within(says).getByRole("button", { name: "Show all" }));
      expect((says.querySelector("[data-note]") as HTMLElement).className).not.toContain("line-clamp-2");
      expect(within(says).getByRole("button", { name: "Show less" })).toBeTruthy();
    });
  });

  describe("what changes", () => {
    it("heads the cards with the count of objects, all or nothing, and opens a card's file in the YAML sheet", async () => {
      mount();
      await title();
      const section = screen.getByRole("region", { name: "What changes" });
      expect(pillTexts(section.querySelector("div"))).toEqual(["8 objects", "all or nothing"]);
      await userEvent.click(within(document.querySelector('[data-item="Role/github-readers"]') as HTMLElement).getByRole("button", { name: "View as YAML" }));
      const sheet = await screen.findByRole("dialog");
      expect(within(sheet).getByRole("heading").textContent).toBe("github-readers.yaml");
      expect(within(sheet).getByText("As it will be published.")).toBeTruthy();
      await userEvent.click(within(sheet).getByRole("button", { name: "Close" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      // Focus goes back to the button that opened the sheet.
      await waitFor(() => expect(document.activeElement).toBe(within(document.querySelector('[data-item="Role/github-readers"]') as HTMLElement).getByRole("button", { name: "View as YAML" })));
    });

    it("says on the server's card and under Checks that whoever publishes enters the missing secret in the dialog", async () => {
      const warn: DraftFinding = { code: "ready.secret-missing", class: "warning", object: "App/github", key: "k-sec", sentence: "github needs a static secret.", fix: "After publishing, run strazactl apps secret set github." };
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { warnings: [warn] } }));
      mount();
      await title();
      const card = document.querySelector('[data-item="App/github"]') as HTMLElement;
      const facts = [...card.querySelectorAll("[data-fact]")].map((f) => f.getAttribute("data-fact"));
      expect(facts.slice(facts.indexOf("Sent as"), facts.indexOf("Sent as") + 2)).toEqual(["Sent as", "Secret"]);
      expect(card.querySelector("[data-secret-row]")?.textContent).toBe(SECRET_LINE);
      expect(document.querySelector('[data-item="App/demo-tools"] [data-secret-row]')).toBeNull();
      expect((document.querySelector('[data-finding="ready.secret-missing"]') as HTMLElement).textContent).toBe(warn.sentence + SECRET_CHECKS_LINE);
    });

    it("leaves Access after publishing out unless a holder's tool changes its outcome or part of it is hidden", async () => {
      mount();
      await title();
      expect(screen.getByRole("heading", { level: 2, name: "Access after publishing" })).toBeTruthy();
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { gains: VERDICT.gains.slice(0, 2) } }));
      const second = mount();
      await waitFor(() => expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(2));
      expect(within(second.container).queryByRole("heading", { level: 2, name: "Access after publishing" })).toBeNull();
      second.unmount();
    });

    it("says part of the access is hidden from the reader", async () => {
      const hidden = { code: "info.gains-hidden", class: "info" as const, key: "k-h", sentence: "2 rows are on servers you cannot read." };
      vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { gains: [], info: [hidden] } }));
      mount();
      await title();
      expect(document.querySelector("[data-gains-hidden]")?.textContent).toContain(GAINS_HIDDEN);
    });

    it("keeps a withheld item's card with its head and the server's sentence", async () => {
      mount();
      await title();
      const card = document.querySelector('[data-item="Role/secret-role"]') as HTMLElement;
      expect(card.querySelector("header [data-kind]")?.textContent).toBe("role");
      expect(card.textContent).toContain(DETAIL.draft.items[7].withheld as string);
    });

    it("reads a draft whose set equals live state, its note under Show", async () => {
      const same = { code: "info.no-change", class: "info" as const, object: "PolicySet/demo-tools-readers-access", key: "k-same", sentence: "demo-tools-readers-access is the same as live state." };
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { items: [{ kind: "PolicySet", name: "demo-tools-readers-access", op: "put", doc: readersSet(true), existed: true }] }, verdict: { ...NO_LINES, info: [same] } }));
      mount();
      await title();
      const card = document.querySelector('[data-item="PolicySet/demo-tools-readers-access"]') as HTMLElement;
      expect(card.querySelector("table")).toBeNull();
      expect(card.querySelector("[data-unchanged-rules]")?.getAttribute("data-unchanged-rules")).toBe("2");
      expect(pillTexts(screen.getByRole("region", { name: "What changes" }).querySelector("div"))).toEqual(["1 object"]);
      await userEvent.click(within(checksHead()).getByRole("button", { name: "Show" }));
      expect(document.querySelector('[data-finding="info.no-change"]')?.textContent).toBe(same.sentence);
    });

    it("names the remote rate limit default the config answers on a new server, and leaves it unnamed when the config is closed", async () => {
      const rate = () => document.querySelector('[data-item="App/github"] [data-fact="Rate limit"]')?.textContent;
      const { unmount } = mount();
      await title();
      await waitFor(() => expect(rate()).toBe("Rate limit25 calls per second, per session, the default."));
      unmount();
      vi.mocked(getConfig).mockRejectedValue(new ApiError("requires the config area", 403));
      mount();
      await title();
      await waitFor(() => expect(getConfig).toHaveBeenCalledTimes(2));
      expect(rate()).toBe("Rate limitThe default for a remote server.");
    });

    it("lays the facts out in one label column from the sm width up", async () => {
      mount();
      await title();
      const grid = (document.querySelector('[data-item="App/github"] dl') as HTMLElement).className;
      expect(grid).toContain("sm:grid-cols-[128px_minmax(0,1fr)]");
    });
  });

  describe("the checks", () => {
    it("counts each class beside the heading, keeps the lines before Publish open, and shows the passed lines on Show", async () => {
      mount();
      await title();
      expect([...checksHead().querySelectorAll("[data-checks-count]")].map((c) => c.textContent)).toEqual(["2 to confirm", "2 warnings", "1 not checked", "1 passed"]);
      expect(document.querySelector('[data-group="risk"]')).not.toBeNull();
      expect(document.querySelector('[data-group="passed"]')).toBeNull();
      expect(document.querySelector('[data-group="info"]')).toBeNull();
      await userEvent.click(within(checksHead()).getByRole("button", { name: "Show" }));
      expect(document.querySelector('[data-group="passed"]')?.textContent).toContain(VERDICT.passed[0].sentence);
      expect(document.querySelector("[data-checks-lede]")).toBeNull();
    });

    // A warning folded into a row of Widens access is said there alone: the
    // band, the row beside Checks and the Warnings group all leave it out.
    it("leaves a warning folded into a row out of the band, the counts and the Warnings group", async () => {
      vi.mocked(getDraft).mockResolvedValue(DRAFT_86);
      mount("86");
      await title(DRAFT_86.draft.title);
      expect([...(band() as HTMLElement).querySelectorAll("[data-band-count]")].map((c) => c.textContent)).toEqual(["1 to confirm"]);
      expect([...checksHead().querySelectorAll("[data-checks-count]")].map((c) => c.textContent)).toEqual(["1 to confirm", "2 passed"]);
      expect(document.querySelector('[data-group="warning"]')).toBeNull();
      expect(document.querySelector("[data-row-note]")?.textContent).toBe("It matches any MCP server a person's own harness runs under the name elevenlabs. If you meant a registered server, fix the name.");
    });

    // A fingerprint is a version of a policy rule or a document, and the
    // page shows one only under the Details of Widens access.
    it.each([["91", DRAFT_91], ["87", DRAFT_87]] as const)("shows no fingerprint of draft %s outside the Details of Widens access", async (id, d) => {
      vi.mocked(getDraft).mockResolvedValue(d);
      mount(id);
      await title(d.draft.title);
      const page = document.body.cloneNode(true) as HTMLElement;
      const inDetails = page.querySelector("[data-widens-details]") as HTMLElement;
      expect(inDetails.textContent).toMatch(/[0-9a-f]{12}/);
      inDetails.remove();
      expect(page.textContent).not.toMatch(/[0-9a-f]{12}/);
      expect(page.textContent).not.toContain("Now:");
    });

    it("offers Contact it now without a click, and reads the draft again after it", async () => {
      vi.mocked(contactDraftServer).mockResolvedValue({ object: "App/github", host: "api.githubcopilot.com", contacted_at: "2026-09-24T10:47:00Z", tools: [] });
      mount();
      await title();
      await userEvent.click(screen.getByRole("button", { name: "Contact it now" }));
      expect(contactDraftServer).toHaveBeenCalledWith("41", "App/github");
      await waitFor(() => expect(getDraft).toHaveBeenCalledTimes(2));
    });
  });

  // A draft that is not open is never checked again: the server answers a
  // verdict with no line and the counts of the check it stored for the
  // revision, so the page shows those counts and no band, gains or lines.
  const closedLeft = () => {
    expect(document.querySelector("[data-checks]")).toBeNull();
    expect(band()).toBeNull();
    expect(document.querySelector("[data-holders]")).toBeNull();
    expect(fact("May publish")).toBeNull();
    expect(fact("Expires")).toBeNull();
    expect(screen.queryByRole("heading", { level: 2, name: "Access after publishing" })).toBeNull();
  };
  const published = (over: Partial<DraftDetail> = {}) => detailWith({
    draft: { state: "published", decided_at: "2026-09-24T10:51:00Z", decided_by: ALICE, snapshot: "3be0a1ff00" },
    verdict: { ...NO_LINES, checked_at: CHECKED_AT, snapshot: "7f3a91c2d4e5" },
    checks: { refused: 0, risks: 2, warnings: 1, unchecked: 0, revision: 2, checked_at: CHECKED_AT },
    may_publish: false,
    ...over,
  });

  describe("a published draft", () => {
    it("shows its proof as one row of pills, the counts of its stored check, and undoes it into a new draft", async () => {
      vi.mocked(getDraft).mockResolvedValue(published());
      vi.mocked(revertDraft).mockResolvedValue({ draft: { ...DETAIL.draft, id: "42", reverts: "41" }, verdict: VERDICT });
      mount();
      await title();
      const proof = document.querySelector("[data-published]") as HTMLElement;
      expect([proof.getAttribute("role"), proof.className.includes("bg-ok-bg"), proof.className.includes("border-ok/40"), proof.className.includes("border-l-")]).toEqual(["status", true, true, false]);
      expect(proof.querySelector('[data-author="person"]')?.textContent).toBe("alice");
      expect(pillTexts(proof)).toEqual([absTime("2026-09-24T10:51:00Z"), "from the console", "version3be0a1ff", "signed"]);
      await userEvent.hover(within(proof).getByRole("button", { name: "Help: Published" }));
      expect((await screen.findByRole("tooltip")).textContent).toBe(liveSince(absTime("2026-09-24T10:51:00Z"), "3be0a1ff") + " " + REEVALUATE);
      expect(within(proof).getByRole("button", { name: "Help: " + UNDO })).toBeTruthy();
      expect(UNDO_HELP).toContain("new draft");
      expect([...checksHead().querySelectorAll("[data-checks-count]")].map((c) => c.textContent)).toEqual(["2 widen access", "1 warning"]);
      expect(pillTexts(checksHead())).toEqual(["revision 2", absTime(CHECKED_AT), "before publishing"]);
      expect(screen.getByRole("heading", { level: 2, name: "What changed" })).toBeTruthy();
      expect(pillTexts(screen.getByRole("region", { name: "What changed" }).querySelector("div"))).toEqual(["8 objects"]);
      await details();
      expect((fact("Last checked") as HTMLElement).querySelector("span.rounded-pill")?.getAttribute("title")).toBe(checkedLine(absTime(CHECKED_AT), "7f3a91c2"));
      closedLeft();
      await userEvent.click(within(proof).getByRole("button", { name: UNDO }));
      expect(revertDraft).toHaveBeenCalledWith("41", "");
      await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["42"]));
    });

    it("says sessions follow the new version only for a draft that holds a role or a policy", async () => {
      const d = published();
      vi.mocked(getDraft).mockResolvedValue({ ...d, draft: { ...d.draft, items: [DETAIL.draft.items[0]] } });
      mount();
      await title();
      await userEvent.hover(within(document.querySelector("[data-published]") as HTMLElement).getByRole("button", { name: "Help: Published" }));
      expect((await screen.findByRole("tooltip")).textContent).toBe(liveSince(absTime("2026-09-24T10:51:00Z"), "3be0a1ff"));
    });

    it("shows a refused Undo under the proof", async () => {
      vi.mocked(getDraft).mockResolvedValue(published());
      vi.mocked(revertDraft).mockRejectedValue(new ApiError("Draft 41 cannot be undone: draft 44 changed demo-tools since.", 409));
      mount();
      await title();
      await userEvent.click(screen.getByRole("button", { name: UNDO }));
      expect((await screen.findByRole("alert")).textContent).toContain("Draft 41 cannot be undone");
      expect((document.querySelector("[data-published]") as HTMLElement).contains(screen.getByRole("alert"))).toBe(true);
    });

    it("offers to assign a role the draft added that nobody holds, from the live role list", async () => {
      const AUDIO_ROLE = "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n  name: audio-gen\nspec:\n  kind: application\n  bindings: []\n";
      vi.mocked(getDraft).mockResolvedValue(published({
        draft: { ...DETAIL.draft, state: "published", decided_at: "2026-09-24T10:51:00Z", decided_by: ALICE, items: [{ kind: "Role", name: "audio-gen", op: "put", doc: AUDIO_ROLE, existed: false }] },
        live: {}, changes: [{ kind: "Role", name: "audio-gen", implied: false, before_op: "remove", before_doc: "", before_fp: "", after_op: "put", after_doc: AUDIO_ROLE, after_fp: "a" }],
      }));
      vi.mocked(listRoles).mockResolvedValue([{ id: "r-9", name: "audio-gen", kind: "application", holder_count: 0 }]);
      mount();
      await title();
      const assign = await screen.findByRole("button", { name: "Assign audio-gen" });
      const short = document.querySelector("[data-short]") as HTMLElement;
      expect([...short.querySelectorAll("dt")].map((dt) => dt.textContent)).toEqual(["Added", "Holders now"]);
      expect(document.querySelector('[data-item="Role/audio-gen"] [data-fact="Holders"]')?.textContent).toBe("Holdersnobody yet");
      await userEvent.click(assign);
      expect(navigate).toHaveBeenCalledWith("roles", ["r-9", "holders"]);
    });

    it("says a later publish replaced it, names that draft, and offers no Undo", async () => {
      const later = { ...PUBLISHED_DETAIL, live: { ...PUBLISHED_DETAIL.live, "Role/old-readers": { op: "put" as const, doc: "" } } };
      vi.mocked(getDraft).mockResolvedValue(later);
      const row = { id: "44", title: "Draft 44", state: "published" as const, door: "console" as const, revision: 1, proposer: ALICE, items: [], created_at: "", updated_at: "", decided_at: "2026-09-25T09:00:00Z", decided_by: ALICE };
      vi.mocked(publishedFor).mockImplementation(async (object) => ({ items: object === "Role/old-readers" ? [row] : [], next_cursor: "" }));
      mount("40");
      await title();
      const strip = await waitFor(() => {
        const s = document.querySelector("[data-replaced]") as HTMLElement | null;
        if (!s) throw new Error("not replaced yet");
        return s;
      });
      expect(publishedFor).toHaveBeenCalledTimes(3);
      expect(strip.className).not.toContain("bg-ok-bg");
      expect(strip.textContent).toContain("Replaced");
      expect(strip.querySelector('[data-tag="Draft 44"]')).not.toBeNull();
      expect(pillTexts(strip)).toEqual([absTime(PUBLISHED_DETAIL.draft.decided_at), dayWord("2026-09-25T09:00:00Z")]);
      expect(screen.queryByRole("button", { name: UNDO })).toBeNull();
      await userEvent.click(within(strip).getByRole("button", { name: "Open draft 44" }));
      expect(navigate).toHaveBeenCalledWith("drafts", ["44"]);
    });

    it("reads no later draft for a draft that was open when the page opened", async () => {
      const answer: DraftPublished = { draft: { ...DETAIL.draft, state: "published", decided_at: "2026-09-24T10:51:00Z", decided_by: ALICE, snapshot: "3be0a1ff00" }, snapshot: "3be0a1ff00", servers: [], next: [] };
      vi.mocked(getDraft).mockResolvedValueOnce(detailWith({ verdict: { risks: [] } })).mockResolvedValue({ ...DETAIL, draft: answer.draft });
      vi.mocked(publishDraft).mockResolvedValue(answer);
      mount();
      await title();
      await userEvent.click(within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }));
      await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Publish" }));
      await waitFor(() => expect(document.querySelector("[data-published]")).not.toBeNull());
      await waitFor(() => expect(listRoles).toHaveBeenCalled());
      expect(publishedFor).not.toHaveBeenCalled();
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
      await userEvent.click(within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }));
      await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Publish" }));
      const proof = await waitFor(() => {
        const p = document.querySelector("[data-published]") as HTMLElement | null;
        if (!p) throw new Error("no proof yet");
        return p;
      });
      expect(proof.querySelector('[data-author="person"]')?.textContent).toBe("alice");
      expect(proof.textContent).toContain(serverLine("github", "created", "starting"));
      expect(screen.getByText(NEXT_TITLE)).toBeTruthy();
      expect(screen.getByText("Each person runs straza connect github.")).toBeTruthy();
      expect(band()).toBeNull();
    });

    const SECRET_WARN: DraftFinding = {
      code: "ready.secret-missing", class: "warning", object: "App/github", key: "k-sec",
      sentence: "github needs a static secret and none is stored yet, so every call fails until one is.", fix: "After publishing, run strazactl apps secret set github.",
    };

    it("keeps the dialog open past the publish, then names the server with its check's status and drops the Next line the stored secret answers", async () => {
      const answer: DraftPublished = {
        draft: { ...DETAIL.draft, state: "published", decided_at: "2026-09-24T10:51:00Z", decided_by: ALICE, snapshot: "3be0a1ff00" },
        snapshot: "3be0a1ff00",
        servers: [{ name: "github", change: "created", status: "pending" }],
        next: [SECRET_WARN.fix as string, "After publishing, run strazactl apps secret set github --role readers.", "Each person runs straza connect github."],
      };
      vi.mocked(getDraft).mockResolvedValueOnce(detailWith({ verdict: { risks: [], warnings: [SECRET_WARN] } })).mockResolvedValue({ ...DETAIL, draft: answer.draft });
      vi.mocked(publishDraft).mockResolvedValue(answer);
      const row = { id: "app-github", name: "github", runtime: "remote", status: "running", tools: ["get_me"], reached_by: [] };
      vi.mocked(listApps).mockResolvedValue([row]);
      vi.mocked(setSecret).mockResolvedValue({ id: "c1", scope: "app", role: "", fingerprint: "a1c4" });
      vi.mocked(recheckApp).mockResolvedValue(row);
      mount();
      await title();
      await userEvent.click(within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }));
      const dialog = await screen.findByRole("dialog");
      await userEvent.type(within(dialog).getByLabelText("Secret of github"), "s3cret");
      await userEvent.click(within(dialog).getByRole("button", { name: "Publish" }));
      await waitFor(() => expect(within(screen.getByRole("dialog")).getByRole("heading").textContent).toBe("Draft 41 is live."));
      expect(setSecret).toHaveBeenCalledWith("app-github", "s3cret");
      const proof = document.querySelector("[data-published]") as HTMLElement;
      await waitFor(() => expect(proof.textContent).toContain(serverLine("github", "created", "running")));
      const next = document.querySelector("[data-next]") as HTMLElement;
      expect([...next.querySelectorAll("p")].map((p) => p.textContent)).toEqual(["After publishing, run strazactl apps secret set github --role readers.", "Each person runs straza connect github."]);
      await userEvent.click(within(document.querySelector('[data-slot="dialog-footer"]') as HTMLElement).getByRole("button", { name: "Close" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(document.querySelector("[data-next]")?.textContent).not.toContain(SECRET_WARN.fix);
    });

    // publishNow opens the page on an open draft, publishes it through the
    // dialog, and waits for the proof the publish answer draws.
    const publishNow = async () => {
      const answer: DraftPublished = { draft: { ...DETAIL.draft, state: "published", decided_at: "2026-09-24T10:51:00Z", decided_by: ALICE, snapshot: "3be0a1ff00" }, snapshot: "3be0a1ff00", servers: [], next: [] };
      vi.mocked(publishDraft).mockResolvedValue(answer);
      mount();
      await title();
      await userEvent.click(within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }));
      await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Publish" }));
      await waitFor(() => expect(document.querySelector("[data-published]")).not.toBeNull());
      return answer;
    };
    const unread = () => document.querySelector("[data-checks-unread]")?.textContent;

    it("reads the draft again after a publish before it says anything about its stored check", async () => {
      let finish: (d: DraftDetail) => void = () => undefined;
      vi.mocked(getDraft).mockResolvedValueOnce(detailWith({ verdict: { risks: [] } })).mockImplementationOnce(() => new Promise((r) => { finish = r; }));
      const answer = await publishNow();
      expect(unread()).toBe("Reading draft 41.");
      await details();
      expect(fact("Last checked")).toBeNull();
      expect(document.body.textContent).not.toContain("no check stored");
      finish(detailWith({ draft: answer.draft, verdict: { ...NO_LINES, checked_at: CHECKED_AT, snapshot: "7f3a91c2d4e5" }, checks: { refused: 0, risks: 0, warnings: 0, unchecked: 0, revision: 2, checked_at: CHECKED_AT }, may_publish: false }));
      await waitFor(() => expect(unread()).toBeUndefined());
      expect(pillTexts(checksHead())).toEqual(["nothing to confirm", "revision 2", absTime(CHECKED_AT), "before publishing"]);
    });

    it("says the draft was published and could not be read again when the read after a publish fails", async () => {
      vi.mocked(getDraft).mockResolvedValueOnce(detailWith({ verdict: { risks: [] } })).mockRejectedValueOnce(new ApiError("strazad did not answer", 0, true));
      await publishNow();
      await waitFor(() => expect(unread()).toBe("Draft 41 is published. This page could not read it again, so its stored check is not shown. Reload to try again."));
      expect(document.body.textContent).toContain(readFailed(subjectDraft("41"), new ApiError("strazad did not answer", 0, true)));
      expect(document.body.textContent).not.toContain("no check stored");
    });
  });

  describe("a discarded or expired draft", () => {
    it.each(["discarded", "expired"] as const)("says when the server stored no check of the revision of a draft now %s", async (state) => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { state, decided_at: "2026-09-24T11:00:00Z", decided_by: ALICE }, verdict: { ...NO_LINES, checked_at: "", snapshot: "" }, may_publish: false }));
      mount();
      await title();
      expect(checksHead().textContent).toBe("Checksno check stored");
      await details();
      expect(fact("Last checked")).toBeNull();
      closedLeft();
    });

    it("shows a discarded draft with who discarded it, when, and why", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { state: "discarded", decided_at: "2026-09-24T11:00:00Z", decided_by: ALICE, decided_reason: "Not needed." } }));
      mount();
      await title();
      const row = document.querySelector("[data-decided]") as HTMLElement;
      expect(row.className).not.toContain("bg-ok-bg");
      expect(row.querySelector('[data-author="person"]')?.textContent).toBe("alice");
      expect(pillTexts(row)).toEqual([absTime("2026-09-24T11:00:00Z")]);
      expect(row.querySelector("[data-reason]")?.textContent).toBe("ReasonNot needed.");
      expect(band()).toBeNull();
    });

    it("shows an expired draft with the time it expired", async () => {
      vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { state: "expired" }, verdict: NO_LINES, may_publish: false }));
      mount();
      await title();
      expect(pillTexts(document.querySelector("[data-decided]"))).toEqual([absTime(DETAIL.draft.expires_at)]);
      expect(document.querySelector("[data-decided] [data-author]")).toBeNull();
    });

    it("discards with the reason and the revision the person read", async () => {
      vi.mocked(discardDraft).mockResolvedValue({ draft: { ...DETAIL.draft, state: "discarded" } });
      mount();
      await title();
      await userEvent.click(within(band() as HTMLElement).getByRole("button", { name: DISCARD }));
      await userEvent.type(await screen.findByRole("textbox", { name: "Reason" }), "Not needed.");
      await userEvent.click(screen.getByRole("button", { name: DISCARD_GO }));
      expect(discardDraft).toHaveBeenCalledWith("41", "Not needed.", 2);
      await waitFor(() => expect(getDraft).toHaveBeenCalledTimes(2));
    });
  });

  const fileDraft = (items: DraftDetail["draft"]["items"]) => detailWith({ draft: { door: "apps-directory", source: "/etc/straza/apps/demo-tools.yaml", items, authors: [{ user_id: "strazad", username: "strazad", agent: false, via: "file", client: "" }] } });

  it("names an apps directory draft's author by its file", async () => {
    vi.mocked(getDraft).mockResolvedValue(fileDraft([DETAIL.draft.items[1]]));
    mount();
    await title();
    expect(within(band() as HTMLElement).getByRole("button", { name: DISCARD })).toBeTruthy();
    expect(meta().querySelector('[data-author="file"]')?.textContent).toBe("demo-tools.yaml");
    await details();
    expect(pillTexts(fact("Came from"))).toEqual(["the apps directory, /etc/straza/apps/demo-tools.yaml"]);
  });

  it("names the server a file's removal keeps when it is discarded", async () => {
    vi.mocked(getDraft).mockResolvedValue(fileDraft([DETAIL.draft.items[6]]));
    mount();
    await title();
    await userEvent.click(within(band() as HTMLElement).getByRole("button", { name: DISCARD }));
    expect((await screen.findByRole("dialog")).textContent).toContain(discardUnlinks("old-tools"));
  });

  it("shows Access after publishing for a held role that only covers more objects", async () => {
    vi.mocked(getDraft).mockResolvedValue(COVERS_DETAIL);
    mount("50");
    await title("Change role tracker-triage");
    expect(screen.getByRole("heading", { level: 2, name: GAINS_TITLE })).toBeTruthy();
  });

  it("reads the registered servers only for a full seat and a draft with a policy set", async () => {
    mount();
    await title();
    await waitFor(() => expect(listApps).toHaveBeenCalledTimes(1));
    vi.mocked(listApps).mockClear();
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "apps:read,drafts:write", expiresIn: 300, sessionID: null });
    vi.mocked(getDraft).mockResolvedValue(COVERS_DETAIL);
    mount("50");
    await title("Change role tracker-triage");
    expect(listApps).not.toHaveBeenCalled();
  });
});
