import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftPage } from "./draft-page";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type DraftDetail, type DraftFinding, type DraftPublished, listApps, recheckApp, setSecret } from "@/lib/api";
import { contactDraftServer, discardDraft, getDraft, publishDraft, rebaseDraft, revertDraft } from "@/lib/drafts-api";
import {
  CHECK_AGAIN,
  DISCARD,
  DISCARD_GO,
  DISCARD_KEEP_LIVE,
  KEEP_DRAFT_TEXT,
  KEEP_LIVE_TEXT,
  MAY_PUBLISH_ALL,
  MISSING_TITLE,
  NEXT_TITLE,
  OPEN_DRAFTS,
  PICKS_GO,
  PUBLISH_NOT_YET,
  PUBLISH_OPEN,
  REFUSED_BAR,
  SAYS_AGENT_HELP,
  SECRET_CHECKS_LINE,
  SECRET_LINE,
  STALE_TITLE,
  UNDO,
  WAITS_WHOLE,
  ackBar,
  checkedLine,
  discardKeep,
  discardedLine,
  holdersLose,
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
import { ALICE, COVERS_DETAIL, CUT_RISK, DETAIL, GATE_LOST, LOOSEN_RISK, STALE, VERDICT, detailWith } from "@/test/drafts-fixture";

// The review page of one draft: read top to bottom, the same for every
// door, with the band that publishes and discards, In this draft above the
// cards, Check again when live state moved, and Undo once published.

vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  getDraft: vi.fn(),
  rebaseDraft: vi.fn(),
  discardDraft: vi.fn(),
  revertDraft: vi.fn(),
  contactDraftServer: vi.fn(),
  publishDraft: vi.fn(),
}));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), listApps: vi.fn(), setSecret: vi.fn(), recheckApp: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const mount = (id = "41") => render(<TooltipProvider><DraftPage id={id} /></TooltipProvider>);
const title = (name = DETAIL.draft.title) => screen.findByRole("heading", { level: 1, name });
const fact = (label: string) => document.querySelector('[data-fact="' + label + '"]') as HTMLElement;
const band = () => document.querySelector("[data-publish-band]") as HTMLElement | null;
const details = () => userEvent.click(screen.getByRole("button", { name: "Details" }));

describe("the review page", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: null });
    vi.mocked(getDraft).mockResolvedValue(DETAIL);
  });

  it("heads the page with the server's title, its state and id, and one line of who drafted it, how, which revision, when and until when", async () => {
    mount();
    await title();
    expect(document.querySelector("[data-state-badge]")?.textContent).toBe("Ready to publish");
    expect(document.querySelector("[data-draft-id]")?.textContent).toBe("draft 41");
    expect(screen.getByRole("navigation", { name: "Breadcrumb" }).textContent).toBe("Straza/Drafts / Draft 41");
    const meta = document.querySelector("[data-meta]") as HTMLElement;
    expect(fact("Drafted by").textContent).toBe("joe-java-developer-agent");
    expect(meta.textContent).toContain("(an agent, sponsored by you)");
    for (const said of ["the straza app", "revision 2", "expires " + absTime(DETAIL.draft.expires_at).slice(0, 10)]) expect(meta.textContent).toContain(said);
    expect(document.querySelector("[data-facts]")).toBeNull();
  });

  it("opens the facts under Details", async () => {
    mount();
    await title();
    await details();
    expect(screen.getByRole("button", { name: "Details" }).getAttribute("aria-expanded")).toBe("true");
    expect(fact("Came from").textContent).toContain("the straza app, from claude-code");
    expect(fact("Revisions").textContent).toContain(revisionLine(2, "joe-java-developer-agent", "straza-app", absTime(DETAIL.revisions[1].created_at), false));
    expect(fact("Last checked").textContent).toContain(checkedLine(absTime(VERDICT.checked_at), "7f3a91c2"));
    expect(fact("Who may publish").textContent).toBe("Who may publish" + MAY_PUBLISH_ALL);
    await details();
    expect(document.querySelector("[data-facts]")).toBeNull();
  });

  it("names the reader's own draft with (you)", async () => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { authors: [ALICE], door: "console" } }));
    mount();
    await title();
    expect((document.querySelector("[data-meta]") as HTMLElement).textContent).toContain("alice(you)");
  });

  it("puts the grants publishing needs behind the help of who may publish, in the band", async () => {
    mount();
    await title();
    const may = (band() as HTMLElement).querySelector("[data-may-publish]") as HTMLElement;
    expect(may.textContent).toBe(MAY_PUBLISH_ALL);
    await userEvent.hover(within(may).getByRole("button", { name: "Help: Who may publish" }));
    expect((await screen.findByRole("tooltip")).textContent).toContain(
      "The server github needs the scope apps:write or the role straza-global-mcp-admin. The policy set github-writers-access needs the scope policy:write.",
    );
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

  it("lays out the band, in this draft, what changes, access after publishing and the checks, with no files section and no bottom bar", async () => {
    mount();
    await title();
    expect((band() as HTMLElement).querySelector("[data-verdict-strip]")).not.toBeNull();
    expect((band() as HTMLElement).className).toContain("sticky");
    for (const name of ["In this draft", "What changes", "Access after publishing", "Checks"]) expect(screen.getByRole("heading", { level: 2, name: new RegExp("^" + name) })).toBeTruthy();
    expect(screen.queryByRole("heading", { level: 2, name: "Files" })).toBeNull();
    expect(document.querySelector("[data-publish-bar]")).toBeNull();
    expect(document.querySelectorAll("[data-item] [data-file]").length).toBe(6);
  });

  it("says what to do before publishing in the band and opens the publish dialog from it", async () => {
    mount();
    await title();
    expect(band()?.textContent).toContain("Before you publish");
    expect(band()?.textContent).toContain(ackBar(2, 2, 2));
    expect(within(band() as HTMLElement).getByRole("button", { name: DISCARD })).toBeTruthy();
    await userEvent.click(within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN }));
    expect(await screen.findByRole("heading", { name: "Publish draft 41?" })).toBeTruthy();
  });

  it("says once that there is nothing to confirm", async () => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [], warnings: [], unchecked: [] } }));
    mount();
    await title();
    expect((band() as HTMLElement).querySelector("[data-strip-none]")?.textContent).toBe("Nothing to confirm");
    expect((band() as HTMLElement).querySelector("[data-band-line]")).toBeNull();
  });

  it("counts the confirm blocks in the band, not the lines", async () => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [LOOSEN_RISK, GATE_LOST] } }));
    mount();
    await title();
    expect(band()?.textContent).toContain(ackBar(1, 2, 2));
    expect(band()?.textContent).toContain("Publishing asks you for 1 confirmation.");
  });

  it("lists every item in this draft with its kind, change and what publishing does, and a row moves focus to its card", async () => {
    mount();
    await title();
    const rows = [...document.querySelectorAll("[data-row-item]")] as HTMLElement[];
    expect(rows.map((r) => r.getAttribute("data-row-item"))).toEqual(DETAIL.draft.items.map((it) => it.kind + "/" + it.name));
    const row = (o: string) => document.querySelector('[data-row-item="' + o + '"]') as HTMLElement;
    expect([...row("Role/github-readers").querySelectorAll("td")].map((td) => td.textContent)).toEqual(["Application role", "github-readersowned by github", "new", "Reaches 2 tools of github.nobody holds it yet"]);
    expect(row("PolicySet/github-writers-access").querySelector("td")?.textContent).toBe("Policy set");
    expect(row("PolicySet/github-writers-access").querySelector("[data-outcomes]")?.textContent).toBe("needs approval");
    expect(row("Role/demo-tools-readers").querySelector("[data-sum]")?.textContent).toBe("-1 tool");
    expect(screen.getByText("8 objects, published together or not at all")).toBeTruthy();
    await userEvent.click(row("PolicySet/github-writers-access"));
    expect(document.activeElement).toBe(document.querySelector('[data-item="PolicySet/github-writers-access"]'));
    await userEvent.click(within(row("App/demo-tools")).getByRole("button", { name: "demo-tools" }));
    expect(document.activeElement).toBe(document.querySelector('[data-item="App/demo-tools"]'));
  });

  it("opens with a red warning naming the agent when its draft loosens the rules that bind it", async () => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [LOOSEN_RISK, GATE_LOST] } }));
    mount();
    await title();
    const warn = document.querySelector("[data-agent-loosens]") as HTMLElement;
    expect(warn.querySelector("b")?.textContent).toBe("joe-java-developer-agent wrote this draft to loosen its own rules.");
    expect(warn.textContent).toContain(LOOSEN_RISK.sentence + " " + GATE_LOST.sentence);
    expect(warn.textContent).toContain("A person may accept it when publishing.");
    expect(warn.className).toContain("bg-danger-bg");
    const first = document.querySelector("[data-agent-loosens], [data-publish-band], [data-in-draft]");
    expect(first).toBe(warn);
  });

  // A line cut for this reader carries no object, so it matches no other
  // line, and the warning says its sentence once.
  it("says a cut line of the warning once", async () => {
    const cut = (code: string, key: string) => ({ ...CUT_RISK, code, key });
    vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { risks: [cut("agent.guardrail", "k-c1"), cut("policy.guardrail-changed", "k-c2")] } }));
    mount();
    await title();
    const warn = document.querySelector("[data-agent-loosens]") as HTMLElement;
    expect(warn.textContent?.split(CUT_RISK.sentence).length).toBe(2);
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

  it("sums up what publishing does to the tickets a held role covers, and who holds it, on its row", async () => {
    vi.mocked(getDraft).mockResolvedValue(COVERS_DETAIL);
    mount("50");
    await title("Change role tracker-triage");
    const row = document.querySelector('[data-row-item="Role/tracker-triage"]') as HTMLElement;
    expect([...row.querySelectorAll("[data-sum]")].map((c) => c.textContent)).toEqual(["+2 tickets"]);
    expect(row.querySelector("[data-holders]")?.textContent).toBe("1 person holds it");
    expect(screen.getByText("1 object")).toBeTruthy();
    expect(band()?.textContent).toContain(ackBar(1, 1, 0));
    expect(document.querySelector("[data-strip]")?.textContent).toBe("1line to confirm");
    expect(document.querySelector("[data-lead]")).toBeNull();
  });

  it("says nobody holds a new role on its chip, drops the note that said it again, and keeps the warning that holders lose tools", async () => {
    mount();
    await title();
    expect(document.querySelector('[data-item="Role/github-readers"] [data-holders]')?.textContent).toBe("nobody holds it yet");
    const loses = document.querySelector("[data-loses]") as HTMLElement;
    expect(loses.textContent).toBe(holdersLose(["demo-tools-readers"]));
    expect(loses.className).toContain("text-warn");
    expect(band()?.nextElementSibling).toBe(loses);
    expect(document.querySelector('[data-group="info"]')).toBeNull();
    expect(document.querySelector("[data-gains-lead]")).toBeNull();
  });

  it("gives a reader who may not publish the server's sentence and no Publish", async () => {
    const refusal = "You cannot publish draft 41: this draft also changes policy sets, which needs the scope policy:write.";
    vi.mocked(getDraft).mockResolvedValue(detailWith({ may_publish: false, publish_refusal: refusal }));
    mount();
    await title();
    expect((band() as HTMLElement).querySelector("[data-may-publish]")?.textContent).toBe(refusal);
    expect((band() as HTMLElement).querySelector("[data-band-line]")?.textContent).toBe(WAITS_WHOLE);
    expect(within(band() as HTMLElement).queryByRole("button", { name: PUBLISH_OPEN })).toBeNull();
    expect(within(band() as HTMLElement).getByRole("button", { name: DISCARD })).toBeTruthy();
  });

  it("holds Publish in the band for a refused draft, with the reason as its title", async () => {
    const refused = { code: "names.taken", class: "refused" as const, object: "App/github", key: "k-r1", sentence: "The name github is taken." };
    vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { refused: [refused] } }));
    mount();
    await title();
    expect(document.querySelector("[data-state-badge]")?.textContent).toBe("Refused");
    const publish = within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN });
    expect([publish.getAttribute("aria-disabled"), publish.getAttribute("title")]).toEqual(["true", PUBLISH_NOT_YET]);
    expect((band() as HTMLElement).querySelector("[data-band-line]")?.textContent).toBe(REFUSED_BAR);
    await userEvent.click(publish);
    expect(screen.queryByRole("dialog")).toBeNull();
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
    expect(proof.textContent).toContain(publishedLine("alice", absTime("2026-09-24T10:51:00Z"), "console", "41"));
    expect(proof.textContent).toContain(serverLine("github", "created", "starting"));
    expect(screen.getByText(NEXT_TITLE)).toBeTruthy();
    expect(screen.getByText("Each person runs straza connect github.")).toBeTruthy();
    expect(band()).toBeNull();
  });

  const SECRET_WARN: DraftFinding = {
    code: "ready.secret-missing", class: "warning", object: "App/github", key: "k-sec",
    sentence: "github needs a static secret and none is stored yet, so every call fails until one is.", fix: "After publishing, run strazactl apps secret set github.",
  };

  it("says on the server's card and under Checks that whoever publishes enters the missing secret in the dialog", async () => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ verdict: { warnings: [SECRET_WARN] } }));
    mount();
    await title();
    const card = document.querySelector('[data-item="App/github"]') as HTMLElement;
    const facts = [...card.querySelectorAll("[data-fact]")].map((f) => f.getAttribute("data-fact"));
    expect(facts.slice(facts.indexOf("Sent as"), facts.indexOf("Sent as") + 2)).toEqual(["Sent as", "Secret"]);
    expect(card.querySelector("[data-secret-row]")?.textContent).toBe(SECRET_LINE);
    expect(card.querySelector("[data-secret-row]")?.className).toContain("text-warn");
    expect(document.querySelector('[data-item="App/demo-tools"] [data-secret-row]')).toBeNull();
    const line = document.querySelector('[data-finding="ready.secret-missing"]') as HTMLElement;
    expect(line.textContent).toBe(SECRET_WARN.sentence + SECRET_CHECKS_LINE);
    expect(document.querySelector("[data-checks]")?.textContent).not.toContain(SECRET_WARN.fix);
  });

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
    await userEvent.click(within(dialog).getByRole("button", { name: "Publish and store the secret" }));
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
  const lede = () => (document.querySelector("[data-checks-lede]") as HTMLElement).textContent;

  it("reads the draft again after a publish before it says anything about its stored check", async () => {
    let finish: (d: DraftDetail) => void = () => undefined;
    vi.mocked(getDraft).mockResolvedValueOnce(detailWith({ verdict: { risks: [] } })).mockImplementationOnce(() => new Promise((r) => { finish = r; }));
    const answer = await publishNow();
    expect(lede()).toBe("Reading draft 41.");
    await details();
    expect(fact("Last checked").textContent).toContain("Reading draft 41.");
    expect(document.body.textContent).not.toContain("stored no check");
    finish(detailWith({ draft: answer.draft, verdict: { ...NO_LINES, checked_at: CHECKED_AT, snapshot: "7f3a91c2d4e5" }, checks: { refused: 0, risks: 0, warnings: 0, unchecked: 0, revision: 2, checked_at: CHECKED_AT }, may_publish: false }));
    await waitFor(() => expect(lede()).toBe("Revision 2 was checked against the live state at " + absTime(CHECKED_AT) + ": nothing to confirm. Straza does not check a draft again once it is published."));
  });

  it("says the draft was published and could not be read again when the read after a publish fails", async () => {
    vi.mocked(getDraft).mockResolvedValueOnce(detailWith({ verdict: { risks: [] } })).mockRejectedValueOnce(new ApiError("strazad did not answer", 0, true));
    await publishNow();
    await waitFor(() => expect(lede()).toBe("Draft 41 is published. This page could not read it again, so its stored check is not shown. Reload to try again."));
    await details();
    expect(fact("Last checked").textContent).toContain("Draft 41 is published.");
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
    expect(band()).toBeNull();
    expect(document.querySelector("[data-holders]")).toBeNull();
    expect(fact("Who may publish")).toBeNull();
    expect(fact("Expires")).toBeNull();
    expect(screen.queryByRole("heading", { level: 2, name: "Access after publishing" })).toBeNull();
    for (const said of ["No object in it needs a grant to publish.", "every read", "No role gains or loses a tool"]) expect(document.body.textContent).not.toContain(said);
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
    await details();
    expect(fact("Last checked").textContent).toContain(checkedLine(absTime(CHECKED_AT), "7f3a91c2"));
    closedLeft();
    await userEvent.click(within(proof).getByRole("button", { name: UNDO }));
    expect(revertDraft).toHaveBeenCalledWith("41", "");
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["42"]));
  });

  it.each(["discarded", "expired"] as const)("says when the server stored no check of the revision of a draft now %s", async (state) => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { state, decided_at: "2026-09-24T11:00:00Z", decided_by: ALICE }, verdict: { ...NO_LINES, checked_at: "", snapshot: "" }, may_publish: false }));
    mount();
    await title();
    await details();
    expect(fact("Last checked").textContent).toContain("Straza stored no check of revision 2.");
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

  it("shows a discarded draft with who discarded it and why, and no band", async () => {
    vi.mocked(getDraft).mockResolvedValue(detailWith({ draft: { state: "discarded", decided_at: "2026-09-24T11:00:00Z", decided_by: ALICE, decided_reason: "Not needed." } }));
    mount();
    await title();
    expect(document.querySelector("[data-decided]")?.textContent).toContain(discardedLine("alice", absTime("2026-09-24T11:00:00Z")));
    expect(document.querySelector("[data-decided]")?.textContent).toContain(reasonLine("Not needed."));
    expect(band()).toBeNull();
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

  const fileDraft = (items: DraftDetail["draft"]["items"]) => detailWith({ draft: { door: "apps-directory", source: "/etc/straza/apps/demo-tools.yaml", items, authors: [{ user_id: "strazad", username: "strazad", agent: false, via: "file", client: "" }] } });

  it("names the apps directory's discard for what it keeps", async () => {
    vi.mocked(getDraft).mockResolvedValue(fileDraft([DETAIL.draft.items[1]]));
    mount();
    await title();
    expect(within(band() as HTMLElement).getByRole("button", { name: DISCARD_KEEP_LIVE })).toBeTruthy();
    expect(fact("Drafted by").textContent).toContain("the file demo-tools.yaml");
    expect((document.querySelector("[data-meta]") as HTMLElement).textContent).toContain("(no person)");
    await details();
    expect(fact("Came from").textContent).toBe("Came fromthe apps directory, /etc/straza/apps/demo-tools.yaml");
  });

  it("names the server a file's removal keeps when it is discarded", async () => {
    vi.mocked(getDraft).mockResolvedValue(fileDraft([DETAIL.draft.items[6]]));
    mount();
    await title();
    expect(within(band() as HTMLElement).getByRole("button", { name: discardKeep("old-tools") })).toBeTruthy();
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
      const publish = within(band() as HTMLElement).getByRole("button", { name: PUBLISH_OPEN });
      expect([publish.getAttribute("aria-disabled"), publish.getAttribute("title")]).toEqual(["true", "Check the draft again before publishing."]);
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
