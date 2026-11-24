import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChecksHead, DraftChecks } from "./draft-checks";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type DraftDetail, type DraftFinding } from "@/lib/api";
import { CONTACT, CONTACT_FAILED, GROUP, NOT_CHECKED_AGAIN, RISK_TAIL, nowAfter } from "@/lib/drafts-words";
import { absTime, agoWord } from "@/lib/words";
import { COVERAGE, COVERS_DETAIL, COVERS_RISK, CUT_RISK, DETAIL, REACH_RISK, VERDICT, detailWith } from "@/test/drafts-fixture";

// The checks of a draft: a heading row of counts, every class of line in
// the server's words with an empty class left out, how each block of the
// lines that widen access is confirmed said once under them, the passed
// lines and the notes opened on Show, and Contact it now on the line of a
// remote server nobody read.

const mount = (d = DETAIL, onContact = vi.fn(async () => undefined), omit?: string[], more = false) => {
  render(<TooltipProvider><DraftChecks detail={d} onContact={onContact} omit={omit} more={more} /></TooltipProvider>);
  return onContact;
};
const group = (cls: string) => document.querySelector('[data-group="' + cls + '"]') as HTMLElement | null;
const line = (code: string) => document.querySelector('[data-finding="' + code + '"]') as HTMLElement;
const acks = () => [...document.querySelectorAll("[data-confirm] [data-ack-words]")].map((s) => s.textContent);
const undo = () => document.querySelector("[data-confirm] [data-undo]")?.textContent;

describe("the checks", () => {
  it("draws each class under its heading with the server's sentence, fix and words", () => {
    mount(DETAIL, undefined, undefined, true);
    expect(within(group("risk") as HTMLElement).getByText(VERDICT.risks[0].sentence)).toBeTruthy();
    expect(group("risk")?.querySelector("h3")?.textContent).toBe(GROUP.risk + "· " + RISK_TAIL);
    expect(line("host.new").querySelector("[data-ack-words]")).toBeNull();
    expect(acks()).toEqual(["Type api.githubcopilot.com when you publish.", "Tick to confirm."]);
    expect(undo()).toBe("Publishing the old version again does not undo it.");
    expect(group("warning")?.textContent).toContain("Assign them after publishing, or let the identity manager do it.");
    expect(group("warning")?.textContent).toContain(nowAfter("reached", "leaves the role"));
    expect(group("unchecked")?.textContent).toContain(VERDICT.unchecked[0].sentence);
    expect(group("passed")?.textContent).toContain(VERDICT.passed[0].sentence);
    expect(group("info")?.textContent).toContain(VERDICT.info[0].sentence);
  });

  it("keeps the lines before Publish open, and leaves the passed lines and the notes out until Show", () => {
    mount();
    for (const cls of ["risk", "warning", "unchecked"]) expect(group(cls)).not.toBeNull();
    for (const cls of ["passed", "info"]) expect(group(cls)).toBeNull();
  });

  it("leaves out every class with no line, refused included", () => {
    mount(detailWith({ verdict: { warnings: [], info: [] } }), undefined, undefined, true);
    for (const cls of ["refused", "warning", "info"]) expect(group(cls)).toBeNull();
    expect(group("passed")?.tagName).toBe("SECTION");
  });

  it("leaves out the lines the page already says, and a class they empty", () => {
    mount(DETAIL, undefined, ["k-i1"], true);
    expect(group("info")).toBeNull();
  });

  it("names the count to type for a typed line of a group, and one tick for a group of ticks", () => {
    const typed = (key: string, object: string): DraftFinding => ({ code: "policy.guardrail-changed", class: "risk", ack: "typed", typed: object, group: "policy.guardrail-changed", object: "PolicySet/" + object, key, sentence: object + " stops asking for approval." });
    const tick = (key: string): DraftFinding => ({ code: "reach.new", class: "risk", ack: "tick", group: "reach.new", object: "Role/" + key, key, sentence: key + " reaches more." });
    mount(detailWith({ verdict: { risks: [typed("a", "set-a"), typed("b", "set-b"), tick("r1"), tick("r2")] } }));
    expect(acks()).toEqual(["Type 2 when you publish, the number of policies it loosens.", "Tick to confirm."]);
    expect(document.querySelectorAll("[data-ack-words]").length).toBe(2);
  });

  it("says a block of ticks undoes on publishing the old version, and a block this reader cannot confirm once", () => {
    mount(detailWith({ verdict: { risks: [REACH_RISK] } }));
    expect(undo()).toBe("Publishing the old version again undoes it.");
    document.body.innerHTML = "";
    mount(detailWith({ verdict: { risks: [CUT_RISK, { ...CUT_RISK, key: "k-cut-2" }] } }));
    expect(acks()).toEqual(["Only someone who may read what this line names can confirm it, so ask an admin who may read it to publish this draft."]);
  });

  // The covers widening is the case no gain row shows: a row from the
  // coverage list, with the objects now and after and who holds the role.
  it("shows a covers widening as a row of the objects now and after, and who holds the role", () => {
    mount(COVERS_DETAIL);
    expect(line("role.covers-wider")).toBeNull();
    const row = document.querySelector('[data-widens-covers="tracker-triage"]') as HTMLElement;
    const cells = [...row.children].map((c) => c.textContent);
    expect(cells).toEqual(["ObjectsTicketsontracker", "Fortracker-triage1 holder", "NowT-1", "", "After publishing+ T-2+ T-3T-1"]);
    expect([...row.querySelectorAll("[data-added]")].map((c) => c.getAttribute("data-name"))).toEqual(["T-2", "T-3"]);
    expect(acks()).toEqual(["Tick to confirm."]);
  });

  it("counts the holders in the plural", () => {
    mount(detailWith({ ...COVERS_DETAIL, verdict: { ...COVERS_DETAIL.verdict, objects: [{ ...COVERAGE, holders_count: 3 }] } }));
    expect(document.querySelector('[data-widens-covers="tracker-triage"]')?.textContent).toContain("tracker-triage3 holders");
  });

  it("keeps the sentence and its Now and After for a server that sends no coverage list", () => {
    mount(detailWith({ ...COVERS_DETAIL, verdict: { ...COVERS_DETAIL.verdict, objects: undefined } }));
    expect(line("role.covers-wider").textContent).toBe(COVERS_RISK.sentence + nowAfter("T-1", "T-1, T-2, T-3"));
    expect(document.querySelector("[data-widens-covers]")).toBeNull();
  });

  it("renders a line whose code it does not know by its class", () => {
    mount(detailWith({ verdict: { warnings: [{ code: "future.code", class: "warning", key: "k-f", sentence: "A line from a newer server." }] } }));
    expect(group("warning")?.textContent).toContain("A line from a newer server.");
  });

  it("offers Contact it now on a remote server nobody read, and hands the object to the page", async () => {
    const onContact = mount();
    const unchecked = group("unchecked") as HTMLElement;
    await userEvent.click(within(unchecked).getByRole("button", { name: CONTACT }));
    expect(onContact).toHaveBeenCalledWith("App/github");
  });

  it("shows why a contact did not finish in the server's own words", async () => {
    const sentence = "Straza contacted api.githubcopilot.com and it did not answer as an MCP server: it answered with HTTP 401. Check the address in the draft.";
    mount(DETAIL, vi.fn(async () => { throw new ApiError(sentence, 502); }));
    await userEvent.click(within(group("unchecked") as HTMLElement).getByRole("button", { name: CONTACT }));
    await waitFor(() => expect((screen.getByRole("alert")).textContent).toBe(CONTACT_FAILED + " " + sentence));
  });

  it("offers no Contact on a decided draft or a server that is not remote", () => {
    mount(detailWith({ draft: { state: "published" } }));
    expect(screen.queryByRole("button", { name: CONTACT })).toBeNull();
  });
});

describe("the row beside the Checks heading", () => {
  const head = (d: DraftDetail, more = false, onMore = vi.fn()) => {
    render(<TooltipProvider><div data-head><ChecksHead detail={d} omit={["k-i1"]} more={more} onMore={onMore} /></div></TooltipProvider>);
    return onMore;
  };
  const counts = () => [...document.querySelectorAll("[data-checks-count]")].map((c) => [c.getAttribute("data-checks-count"), c.getAttribute("data-tone"), c.textContent]);
  const text = () => (document.querySelector("[data-head]") as HTMLElement).textContent;

  it("counts each class of an open draft that holds a line, in the tones of its group, and when it was checked", async () => {
    const onMore = head(detailWith({ verdict: { info: [...VERDICT.info, { code: "info.no-change", class: "info", key: "k-i2", sentence: "Nothing changes." }] } }));
    expect(counts()).toEqual([["risk", "warn", "2 to confirm"], ["warning", "warn", "2 warnings"], ["unchecked", "unknown", "1 not checked"], ["info", "plain", "1 note"], ["passed", "ok", "1 passed"]]);
    const ago = screen.getByText(agoWord(VERDICT.checked_at));
    expect(ago.getAttribute("title")).toBe(absTime(VERDICT.checked_at));
    await userEvent.click(screen.getByRole("button", { name: "Show" }));
    expect(onMore).toHaveBeenCalled();
  });

  it("counts a refusal, and offers no Show where nothing is folded", () => {
    head(detailWith({ verdict: { refused: [{ code: "names.taken", class: "refused", key: "k-r", sentence: "Taken." }], risks: [], warnings: [], unchecked: [], passed: [], info: [] } }));
    expect(counts()).toEqual([["refused", "danger", "1 refused"]]);
    expect(screen.queryByRole("button", { name: "Show" })).toBeNull();
  });

  it("says Hide once the folded lines are open", () => {
    head(DETAIL, true);
    expect(screen.getByRole("button", { name: "Hide" }).getAttribute("aria-expanded")).toBe("true");
  });

  const stored = { refused: 0, risks: 2, warnings: 1, unchecked: 0, revision: 2, checked_at: "2026-09-24T10:44:00Z" };
  it.each(["published", "discarded", "expired"] as const)("counts the stored check of a draft now %s, says when it was stored, and that it is not run again", async (state) => {
    head(detailWith({ draft: { state }, checks: stored }));
    expect(counts()).toEqual([["risk", "warn", "2 widen access"], ["warning", "warn", "1 warning"]]);
    const words = { published: "before publishing", discarded: "before it was discarded", expired: "before it expired" }[state];
    for (const said of ["revision 2", absTime(stored.checked_at), words]) expect(text()).toContain(said);
    await userEvent.hover(screen.getByRole("button", { name: "Help: Checks" }));
    expect((await screen.findByRole("tooltip")).textContent).toContain(NOT_CHECKED_AGAIN[state]);
  });

  it("says nothing was to confirm when every stored count is zero", () => {
    head(detailWith({ draft: { state: "published" }, checks: { ...stored, risks: 0, warnings: 0 } }));
    expect(counts()).toEqual([]);
    expect(text()).toContain("nothing to confirm");
  });

  it("says no check is stored for a decided draft the server stored none of", () => {
    head(detailWith({ draft: { state: "expired" } }));
    expect(text()).toBe("no check stored");
    expect(screen.getByRole("button", { name: "Help: Checks" })).toBeTruthy();
  });
});
