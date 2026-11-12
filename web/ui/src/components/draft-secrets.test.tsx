import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftPublish } from "./draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type Draft, type DraftFinding, type DraftPublished, type SecretRow, listApps, recheckApp, setListingCredential, setSecret } from "@/lib/api";
import { publishDraft } from "@/lib/drafts-api";
import {
  ACK_MISSING,
  NOT_PUBLISHED,
  notSent,
  againHint,
  enterListingOr,
  enterOr,
  laterLine,
  laterListing,
  leadBoth,
  listingHint,
  listingNotStored,
  listingNotStoredText,
  listsSoon,
  maybeStored,
  maybeText,
  notStored,
  notStoredText,
  retryHint,
  secretHint,
  storedNotPassed,
} from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { snapshot } from "@/lib/session";
import { ALICE, DETAIL, GITHUB_APP, HOST_RISK, REACH_RISK, VERDICT } from "@/test/drafts-fixture";

// The Secrets section of the review page's publish dialog: one field for
// each value the verdict warns a server lacks, a button that counts them,
// and the run after the publish that stores each value once and checks the
// server with the wizard's sequence, with no second publish on a retry.

vi.mock("@/lib/drafts-api", async (orig) => ({ ...(await orig<typeof import("@/lib/drafts-api")>()), publishDraft: vi.fn() }));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listApps: vi.fn(), setSecret: vi.fn(), setListingCredential: vi.fn(), recheckApp: vi.fn(),
}));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));

const PG_APP = `apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: postgres
straza:
  runtime:
    kind: remote
    remote:
      url: https://postgres-mcp.example.com/mcp
  credential:
    kind: static
  objects:
    type: database
    plural: databases
    list:
      tool: list_databases
`;
const PIN = { type: "database", plural: "databases", tool: "list_databases" };
const URL_ = "https://api.githubcopilot.com/mcp/";

const secretWarn = (name: string): DraftFinding => ({
  code: "ready.secret-missing", class: "warning", object: "App/" + name, key: "k-sec-" + name,
  sentence: name + " needs a static secret and none is stored yet, so every call fails until one is.", fix: "After publishing, run strazactl apps secret set " + name + ".",
});
const LISTING_WARN: DraftFinding = {
  code: "ready.objects-listing", class: "warning", object: "App/postgres", key: "k-lst",
  sentence: "postgres has no listing credential, so Straza cannot list its databases, and only roles on every database can use its database tools until it has one.",
};
const DRAFT: Draft = {
  ...DETAIL.draft,
  items: [
    { kind: "App", name: "github", op: "put", doc: GITHUB_APP, existed: false },
    { kind: "App", name: "linear", op: "put", doc: GITHUB_APP.replace("name: github", "name: linear"), existed: false },
    { kind: "App", name: "postgres", op: "put", doc: PG_APP, existed: false },
  ],
};
const PUBLISHED: DraftPublished = { draft: { ...DRAFT, state: "published" }, snapshot: "3be0a1ff", servers: [{ name: "github", change: "created", status: "pending" }], next: [] };
const app = (name: string, over: Partial<AppRow> = {}): AppRow => ({ id: "app-" + name, name, runtime: "remote", status: "running", tools: ["get_me", "list_issues"], reached_by: [], ...over });

function mount(warnings: DraftFinding[], opts: { risks?: DraftFinding[]; draft?: Draft; checks?: boolean } = {}) {
  const onPublished = vi.fn();
  const onOpenChange = vi.fn();
  const onChecked = vi.fn();
  render(
    <TooltipProvider>
      <DraftPublish
        draft={opts.draft || DRAFT}
        verdict={{ ...VERDICT, risks: opts.risks || [], warnings }}
        open
        onOpenChange={onOpenChange}
        onPublished={onPublished}
        onChecked={opts.checks === false ? undefined : onChecked}
      />
    </TooltipProvider>,
  );
  return { onPublished, onOpenChange, onChecked };
}

const dialog = () => screen.getByRole("dialog");
const go = () => within(dialog()).getByRole("button", { name: /^Publish/ }) as HTMLButtonElement;
const field = (label: string) => within(dialog()).getByLabelText(label) as HTMLInputElement;
const laterOf = (key: string) => within(document.querySelector('[data-secret="' + key + '"]') as HTMLElement).getByRole("checkbox", { name: "Add it later" });
const rows = (scope: ParentNode = document) => [...(scope.querySelector("[data-landed]") as HTMLElement).querySelectorAll("li")].map((li) => li.textContent);
const foot = () => [...(document.querySelector('[data-slot="dialog-footer"]') as HTMLElement).querySelectorAll("button")].map((b) => b.textContent);
const title = () => within(dialog()).getByRole("heading").textContent;
const missingLines = () => [...(document.querySelector("[data-ack-missing]")?.querySelectorAll("span") || [])].map((s) => s.textContent);

// deferred is a promise the test settles by hand, to read the rows between
// two calls.
function deferred<T>() {
  let resolve: (v: T) => void = () => undefined;
  let reject: (e: unknown) => void = () => undefined;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

describe("the Secrets section of the publish dialog", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: null });
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED);
    vi.mocked(listApps).mockResolvedValue([app("github", { url: URL_ }), app("linear"), app("postgres")]);
    vi.mocked(setSecret).mockResolvedValue({ id: "c1", scope: "app", role: "", fingerprint: "a1c4" });
    vi.mocked(setListingCredential).mockResolvedValue({ app: "postgres", fingerprint: "9f3a", setAt: "2026-11-11T09:20:14Z" });
    vi.mocked(recheckApp).mockImplementation(async (id) => app(id.replace("app-", ""), { url: id === "app-github" ? URL_ : undefined }));
  });

  it("asks for the secret of a server the verdict warns has none, only from the review page", () => {
    const view = mount([secretWarn("github")]);
    const section = document.querySelector("[data-secrets]") as HTMLElement;
    expect(within(section).getByText("Secret")).toBeTruthy();
    // The heading reads like the dialog's other section labels, never in caps.
    expect(within(section).getByText("Secret").className).not.toContain("uppercase");
    expect(section.textContent).toContain("github has no secret yet, and every call to it fails until one is stored. Enter it here, and Straza stores it right after the publish.");
    expect(field("Secret of github").type).toBe("password");
    const hint = document.getElementById(field("Secret of github").getAttribute("aria-describedby") || "");
    expect(hint?.textContent).toBe(secretHint("joe-java-developer-agent"));
    expect(view.onChecked).not.toHaveBeenCalled();
  });

  it.each([
    ["the wizard's and the editors' dialog", [secretWarn("github")], false],
    ["a verdict with no secret warning", [VERDICT.warnings[0]], true],
  ])("draws no Secrets section for %s", (_name, warnings, checks) => {
    mount(warnings, { checks });
    expect(document.querySelector("[data-secrets]")).toBeNull();
    expect(go().textContent).toBe("Publish");
  });

  it("leaves the agent out of the hint of a draft only people wrote", () => {
    mount([secretWarn("github")], { draft: { ...DRAFT, authors: [ALICE] } });
    expect(secretHint(null)).toBe("Straza stores it encrypted. It is never shown again and never returned by any read.");
    expect(document.getElementById(field("Secret of github").getAttribute("aria-describedby") || "")?.textContent).toBe(secretHint(null));
  });

  it("names the button for one secret, for several, and Publish when every field waits for later", async () => {
    mount([secretWarn("github"), secretWarn("linear")]);
    expect(go().textContent).toBe("Publish and store 2 secrets");
    expect(within(document.querySelector("[data-secrets]") as HTMLElement).getByText("Secrets")).toBeTruthy();
    await userEvent.click(laterOf("linear"));
    expect(go().textContent).toBe("Publish and store the secret");
    expect(field("Secret of linear").disabled).toBe(true);
    expect(document.querySelector('[data-secret="linear"] [data-later]')?.textContent).toBe(laterLine("linear"));
    await userEvent.click(laterOf("github"));
    expect(go().textContent).toBe("Publish");
  });

  it("keeps the button off until every block is confirmed and every field answered, and says what is missing", async () => {
    mount([secretWarn("github")], { risks: [HOST_RISK, REACH_RISK] });
    expect(go().disabled).toBe(true);
    expect(missingLines()).toEqual([ACK_MISSING, enterOr("github")]);
    expect(go().getAttribute("aria-describedby")).toBe((document.querySelector("[data-ack-missing]") as HTMLElement).id);
    await userEvent.type(within(dialog()).getByRole("textbox", { name: "Type api.githubcopilot.com to confirm" }), "api.githubcopilot.com");
    await userEvent.click(within(dialog()).getByRole("checkbox", { name: REACH_RISK.sentence }));
    expect(go().disabled).toBe(true);
    expect(missingLines()).toEqual([enterOr("github")]);
    await userEvent.type(field("Secret of github"), "s3cret");
    expect(go().disabled).toBe(false);
    expect(document.querySelector("[data-ack-missing]")).toBeNull();
  });

  it("publishes as today with Add it later, and stores nothing", async () => {
    const { onPublished } = mount([secretWarn("github")]);
    await userEvent.click(laterOf("github"));
    await userEvent.click(go());
    await waitFor(() => expect(onPublished).toHaveBeenCalled());
    expect(onPublished.mock.calls[0]).toEqual([PUBLISHED]);
    for (const fn of [listApps, setSecret, recheckApp]) expect(fn).not.toHaveBeenCalled();
  });

  it("runs the rows in the dialog after the click, with Cancel off, and ends with the check's sentence", async () => {
    const pub = deferred<DraftPublished>();
    const store = deferred<SecretRow>();
    vi.mocked(publishDraft).mockReturnValue(pub.promise);
    vi.mocked(setSecret).mockReturnValue(store.promise);
    const { onPublished, onChecked, onOpenChange } = mount([secretWarn("github")]);
    await userEvent.type(field("Secret of github"), "s3cret");
    await userEvent.click(go());
    expect(title()).toBe("Publishing draft 41");
    expect(rows()).toEqual(["runningPublish draft 41", "pendingStore the secret of github", "pendingCheck github"]);
    expect(foot()).toEqual(["Cancel", "Publish and store the secret"]);
    expect([...(document.querySelector('[data-slot="dialog-footer"]') as HTMLElement).querySelectorAll("button")].every((b) => b.disabled)).toBe(true);
    pub.resolve(PUBLISHED);
    await waitFor(() => expect(rows()).toEqual(["donePublish draft 41", "runningStore the secret of github", "pendingCheck github"]));
    expect(onPublished).toHaveBeenCalledWith(PUBLISHED, true);
    store.resolve({ id: "c1", scope: "app", role: "", fingerprint: "a1c4" });
    await waitFor(() => expect(title()).toBe("Draft 41 is live."));
    expect(rows()).toEqual(["donePublish draft 41", "doneStore the secret of github", "doneCheck github"]);
    expect(setSecret).toHaveBeenCalledWith("app-github", "s3cret");
    expect(setSecret).toHaveBeenCalledTimes(1);
    expect(vi.mocked(publishDraft).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(setSecret).mock.invocationCallOrder[0]);
    expect(vi.mocked(setSecret).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(recheckApp).mock.invocationCallOrder[0]);
    expect(document.querySelector("[data-health]")?.textContent).toBe("Running. 2 tools found: get_me, list_issues. Checked just now.");
    expect(onChecked).toHaveBeenCalledWith("github", expect.objectContaining({ id: "app-github", status: "running" }), secretWarn("github").fix);
    expect(foot()).toEqual(["Close", "Open github"]);
    expect(notify.ok).not.toHaveBeenCalled();
    await userEvent.click(within(document.querySelector('[data-slot="dialog-footer"]') as HTMLElement).getByRole("button", { name: "Open github" }));
    expect(navigate).toHaveBeenCalledWith("servers", ["app-github"]);
    await userEvent.click(within(document.querySelector('[data-slot="dialog-footer"]') as HTMLElement).getByRole("button", { name: "Close" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it.each([
    ["Straza refused it", new ApiError("store secret failed", 500), "alert", notStored("github") + " " + notStoredText("41", "github", "store secret failed")],
    ["no answer came back", new ApiError("unreachable", 0, true), "status", maybeStored("github") + " " + maybeText("41", "github")],
  ])("says the publish stays done when %s, and stores and checks again with no second publish", async (_name, failure, role, said) => {
    vi.mocked(setSecret).mockRejectedValueOnce(failure);
    vi.mocked(recheckApp).mockResolvedValueOnce(app("github", { status: "degraded", detail: "requires a credential and none is stored", tools: [] }));
    const { onChecked } = mount([secretWarn("github")]);
    await userEvent.type(field("Secret of github"), "s3cret");
    await userEvent.click(go());
    await waitFor(() => expect(title()).toBe("Draft 41 is live."));
    expect(rows()).toEqual(["donePublish draft 41", "failedStore the secret of github", "failedCheck github"]);
    expect(within(dialog()).getByRole(role).textContent).toBe(said);
    expect(document.querySelector("[data-health]")).toBeNull();
    expect(onChecked).toHaveBeenCalledWith("github", expect.objectContaining({ status: "degraded" }), null);
    expect(foot()).toEqual(["Close"]);
    expect(field("Secret of github").value).toBe("");
    expect(document.getElementById(field("Secret of github").getAttribute("aria-describedby") || "")?.textContent).toBe(retryHint("github"));
    const again = within(dialog()).getByRole("button", { name: "Store the secret and check" }) as HTMLButtonElement;
    expect(again.disabled).toBe(true);
    await userEvent.type(field("Secret of github"), "right-one");
    await userEvent.click(again);
    await waitFor(() => expect(document.querySelector("[data-health]")?.getAttribute("data-health")).toBe("running"));
    expect(setSecret).toHaveBeenLastCalledWith("app-github", "right-one");
    expect(publishDraft).toHaveBeenCalledTimes(1);
    expect(listApps).toHaveBeenCalledTimes(1);
    expect(onChecked).toHaveBeenLastCalledWith("github", expect.objectContaining({ status: "running" }), secretWarn("github").fix);
  });

  it("says the secret is stored when the check fails, with the wizard's sentence and three doors", async () => {
    vi.mocked(recheckApp).mockResolvedValue(app("github", { url: URL_, status: "degraded", detail: "ping: upstream answered 401 Unauthorized", tools: [] }));
    mount([secretWarn("github")]);
    await userEvent.type(field("Secret of github"), "s3cret");
    await userEvent.click(go());
    await waitFor(() => expect(title()).toBe("Draft 41 is live."));
    expect(rows()).toEqual(["donePublish draft 41", "doneStore the secret of github", "refusedCheck github"]);
    expect(document.querySelector("[data-stored-not-passed]")?.textContent).toBe(storedNotPassed("github"));
    expect(document.querySelector("[data-health]")?.textContent).toBe("Refused. " + URL_ + " answered 401 to the secret you stored. Check the value with whoever issued it, set it again, then Recheck. The server exists and keeps this state; you can finish access later from its page.");
    expect(foot()).toEqual(["Set the secret again", "Recheck", "Finish later, open github"]);
    await userEvent.click(within(dialog()).getByRole("button", { name: "Set the secret again" }));
    expect(document.getElementById(field("Secret of github").getAttribute("aria-describedby") || "")?.textContent).toBe(againHint("github"));
    expect(foot()).toEqual(["Recheck", "Finish later, open github"]);
    await userEvent.click(within(dialog()).getByRole("button", { name: "Recheck" }));
    await waitFor(() => expect(recheckApp).toHaveBeenCalledTimes(2));
    expect(setSecret).toHaveBeenCalledTimes(1);
    await userEvent.click(within(dialog()).getByRole("button", { name: "Finish later, open github" }));
    expect(navigate).toHaveBeenCalledWith("servers", ["app-github"]);
    expect(publishDraft).toHaveBeenCalledTimes(1);
  });

  it("runs two servers one by one, and brings back only the field of the one that failed", async () => {
    vi.mocked(setSecret).mockImplementation(async (id) => {
      if (id === "app-linear") throw new ApiError("store secret failed", 500);
      return { id: "c1", scope: "app", role: "", fingerprint: "a1c4" };
    });
    vi.mocked(recheckApp).mockImplementation(async (id) => (id === "app-linear" ? app("linear", { status: "degraded", detail: "requires a credential and none is stored", tools: [] }) : app("github")));
    const { onChecked } = mount([secretWarn("github"), secretWarn("linear")]);
    await userEvent.type(field("Secret of github"), "one");
    await userEvent.type(field("Secret of linear"), "two");
    await userEvent.click(go());
    await waitFor(() => expect(title()).toBe("Draft 41 is live."));
    expect(rows()).toEqual(["donePublish draft 41"]);
    const github = document.querySelector('[data-run="github"]') as HTMLElement;
    const linear = document.querySelector('[data-run="linear"]') as HTMLElement;
    expect(rows(github)).toEqual(["doneStore the secret of github", "doneCheck github"]);
    expect(github.querySelector("[data-health]")?.getAttribute("data-health")).toBe("running");
    expect(within(github).getByRole("button", { name: "Open github" })).toBeTruthy();
    expect(rows(linear)).toEqual(["failedStore the secret of linear", "failedCheck linear"]);
    expect(within(linear).getByRole("alert").textContent).toBe(notStored("linear") + " " + notStoredText("41", "linear", "store secret failed"));
    expect(within(dialog()).getAllByRole("button", { name: "Store the secret and check" }).length).toBe(1);
    expect(within(linear).getByLabelText("Secret of linear")).toBeTruthy();
    expect(within(dialog()).queryByLabelText("Secret of github")).toBeNull();
    expect(vi.mocked(setSecret).mock.calls).toEqual([["app-github", "one"], ["app-linear", "two"]]);
    expect(onChecked).toHaveBeenCalledWith("github", expect.anything(), secretWarn("github").fix);
    expect(onChecked).toHaveBeenCalledWith("linear", expect.anything(), null);
    expect(foot()).toEqual(["Close"]);
  });

  it("drops a typed secret when the dialog closes, so it is empty when it opens again", async () => {
    const ui = (open: boolean) => (
      <TooltipProvider>
        <DraftPublish draft={DRAFT} verdict={{ ...VERDICT, risks: [], warnings: [secretWarn("github")] }} open={open} onOpenChange={vi.fn()} onPublished={vi.fn()} onChecked={vi.fn()} />
      </TooltipProvider>
    );
    const { rerender } = render(ui(true));
    await userEvent.type(field("Secret of github"), "s3cret");
    rerender(ui(false));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    rerender(ui(true));
    expect(field("Secret of github").value).toBe("");
    expect(setSecret).not.toHaveBeenCalled();
  });

  it("sends no secret when the second-person rule refuses the publish, empties the field and says so", async () => {
    const sentence = "You sponsor joe-java-developer-agent, which proposed this draft, and this deployment needs a publisher other than the proposer and its sponsor for a change that widens access (admin.secondPerson). Ask another administrator to review and publish it.";
    vi.mocked(publishDraft).mockRejectedValue(new ApiError(sentence, 409, false, 0, { error: sentence, code: "second_person" }));
    const { onPublished } = mount([secretWarn("github")]);
    await userEvent.type(field("Secret of github"), "s3cret");
    await userEvent.click(go());
    expect((await screen.findByRole("alert")).textContent).toBe(NOT_PUBLISHED + " " + sentence + notSent(1));
    expect(field("Secret of github").value).toBe("");
    expect(go().disabled).toBe(true);
    for (const fn of [listApps, setSecret, recheckApp]) expect(fn).not.toHaveBeenCalled();
    expect(onPublished).not.toHaveBeenCalled();
  });

  describe("the listing credential of a server that pins objects", () => {
    it("asks for both values of a server with both warnings, and counts them as two secrets", () => {
      mount([secretWarn("postgres"), LISTING_WARN]);
      const section = document.querySelector("[data-secrets]") as HTMLElement;
      expect(within(section).getByText("Secrets")).toBeTruthy();
      expect(section.textContent).toContain(leadBoth("postgres", PIN));
      expect(field("Secret of postgres").type).toBe("password");
      expect(field("Listing credential of postgres").type).toBe("password");
      expect(document.getElementById(field("Listing credential of postgres").getAttribute("aria-describedby") || "")?.textContent).toBe(listingHint("postgres", PIN, "joe-java-developer-agent"));
      expect(listingHint("postgres", PIN, null)).toBe("Only Straza's own listing of the databases of postgres carries it, when Straza calls list_databases. Calls by people and agents never carry it. Straza stores it encrypted and never shows it again.");
      expect(go().textContent).toBe("Publish and store 2 secrets");
    });

    it("names the button for the listing credential when it is the only value asked", async () => {
      mount([LISTING_WARN]);
      expect([...document.querySelectorAll("[data-secrets] input[type=password]")].map((i) => i.id && document.querySelector('label[for="' + i.id + '"]')?.textContent)).toEqual(["Listing credential of postgres"]);
      expect(go().textContent).toBe("Publish and store the listing credential");
      expect(missingLines()).toEqual([enterListingOr("postgres")]);
      await userEvent.type(field("Listing credential of postgres"), "list-token");
      expect(go().disabled).toBe(false);
    });

    it("stores the secret, then the listing credential, then checks, and says when the first listing runs", async () => {
      mount([secretWarn("postgres"), LISTING_WARN]);
      await userEvent.type(field("Secret of postgres"), "s3cret");
      await userEvent.type(field("Listing credential of postgres"), "list-token");
      await userEvent.click(go());
      await waitFor(() => expect(title()).toBe("Draft 41 is live."));
      expect(rows()).toEqual(["donePublish draft 41", "doneStore the secret of postgres", "doneStore the listing credential of postgres", "doneCheck postgres"]);
      expect(setListingCredential).toHaveBeenCalledWith("app-postgres", "list-token");
      expect(vi.mocked(setSecret).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(setListingCredential).mock.invocationCallOrder[0]);
      expect(vi.mocked(setListingCredential).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(recheckApp).mock.invocationCallOrder[0]);
      expect(document.querySelector("[data-health]")?.getAttribute("data-health")).toBe("running");
      expect(document.querySelector("[data-lists-soon]")?.textContent).toBe(listsSoon("postgres", PIN));
      expect(foot()).toEqual(["Close", "Open postgres"]);
    });

    it("brings back only the listing field when the listing credential is refused with the secret stored, and publishes nothing again", async () => {
      vi.mocked(setListingCredential).mockRejectedValueOnce(new ApiError("store the listing credential failed", 500));
      mount([secretWarn("postgres"), LISTING_WARN]);
      await userEvent.type(field("Secret of postgres"), "s3cret");
      await userEvent.type(field("Listing credential of postgres"), "list-token");
      await userEvent.click(go());
      await waitFor(() => expect(title()).toBe("Draft 41 is live."));
      expect(rows()).toEqual(["donePublish draft 41", "doneStore the secret of postgres", "failedStore the listing credential of postgres", "doneCheck postgres"]);
      expect(document.querySelector("[data-health]")?.getAttribute("data-health")).toBe("running");
      expect(within(dialog()).getByRole("alert").textContent).toBe(listingNotStored("postgres") + " " + listingNotStoredText("41", "postgres", PIN, true, "store the listing credential failed"));
      expect(listingNotStoredText("41", "postgres", PIN, true, "store the listing credential failed")).toBe("Draft 41 is published and stays published, and the secret of postgres is stored, so its calls work. Straza cannot list its databases, and only roles on every database can use its database tools until it has one. The reason Straza gave: \"store the listing credential failed\". Enter it again below. Nothing is published again.");
      expect(within(dialog()).queryByLabelText("Secret of postgres")).toBeNull();
      expect(foot()).toEqual(["Close", "Open postgres"]);
      await userEvent.type(field("Listing credential of postgres"), "list-token-2");
      await userEvent.click(within(dialog()).getByRole("button", { name: "Store the listing credential" }));
      await waitFor(() => expect(document.querySelector("[data-lists-soon]")).not.toBeNull());
      expect(setListingCredential).toHaveBeenLastCalledWith("app-postgres", "list-token-2");
      expect(publishDraft).toHaveBeenCalledTimes(1);
      expect(setSecret).toHaveBeenCalledTimes(1);
      expect(recheckApp).toHaveBeenCalledTimes(1);
      expect(within(dialog()).queryByLabelText("Listing credential of postgres")).toBeNull();
    });

    it("stores the secret alone when the listing credential waits for later", async () => {
      mount([secretWarn("postgres"), LISTING_WARN]);
      await userEvent.type(field("Secret of postgres"), "s3cret");
      await userEvent.click(laterOf("postgres/listing"));
      expect(document.querySelector('[data-secret="postgres/listing"] [data-later]')?.textContent).toBe(laterListing("postgres", PIN));
      expect(go().textContent).toBe("Publish and store the secret");
      await userEvent.click(go());
      await waitFor(() => expect(title()).toBe("Draft 41 is live."));
      expect(rows()).toEqual(["donePublish draft 41", "doneStore the secret of postgres", "doneCheck postgres"]);
      expect(setListingCredential).not.toHaveBeenCalled();
      expect(document.querySelector("[data-run] [data-later]")?.textContent).toBe(laterListing("postgres", PIN));
    });

    it("sends neither value when the publish is refused", async () => {
      vi.mocked(publishDraft).mockRejectedValue(new ApiError("Draft 41 cannot be published: the draft changed.", 409, false, 0, { error: "Draft 41 cannot be published: the draft changed." }));
      mount([secretWarn("postgres"), LISTING_WARN]);
      await userEvent.type(field("Secret of postgres"), "s3cret");
      await userEvent.type(field("Listing credential of postgres"), "list-token");
      await userEvent.click(go());
      expect((await screen.findByRole("alert")).textContent).toContain(notSent(2));
      expect(notSent(2)).toBe("The secrets you entered were not sent anywhere, because nothing was published. The fields are empty again.");
      expect([field("Secret of postgres").value, field("Listing credential of postgres").value]).toEqual(["", ""]);
      for (const fn of [listApps, setSecret, setListingCredential, recheckApp]) expect(fn).not.toHaveBeenCalled();
    });
  });
});
