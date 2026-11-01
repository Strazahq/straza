import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftChecks, VerdictStrip } from "./draft-checks";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type DraftFinding } from "@/lib/api";
import { confirmBlocks } from "@/lib/drafts-model";
import { CONTACT, CONTACT_FAILED, GROUP, NOTHING_TO_CONFIRM, ackWords, nowAfter, riskTail } from "@/lib/drafts-words";
import { COVERAGE, COVERS_DETAIL, COVERS_RISK, DETAIL, VERDICT, detailWith } from "@/test/drafts-fixture";

// The checks of a draft: the strip
// counts each class that holds a line and jumps to the lines, every class
// renders its lines in the server's words, an empty class is left out,
// Passed is folded, and Contact it now sits on the line of a remote server
// nobody read.

const mount = (d = DETAIL, onContact = vi.fn(async () => undefined), omit?: string[]) => {
  render(<TooltipProvider><DraftChecks detail={d} onContact={onContact} omit={omit} /></TooltipProvider>);
  return onContact;
};
const group = (cls: string) => document.querySelector('[data-group="' + cls + '"]') as HTMLElement | null;
const line = (code: string) => document.querySelector('[data-finding="' + code + '"]') as HTMLElement;

describe("the verdict strip", () => {
  it("counts only the classes that hold a line, and jumps to the checks", async () => {
    const onJump = vi.fn();
    render(<VerdictStrip verdict={VERDICT} onJump={onJump} />);
    const counts = [...document.querySelectorAll("[data-strip]")].map((b) => (b as HTMLElement).textContent);
    expect(counts).toEqual(["2lines to confirm", "2warnings", "1not checked"]);
    await userEvent.click(screen.getByRole("button", { name: "2 lines to confirm: go to the checks" }));
    expect(onJump).toHaveBeenCalled();
  });

  it("says there is nothing to confirm when no class holds a line", () => {
    render(<VerdictStrip verdict={{ ...VERDICT, risks: [], warnings: [], unchecked: [] }} onJump={vi.fn()} />);
    expect(document.querySelectorAll("[data-strip]")).toHaveLength(0);
    expect(document.querySelector("[data-strip-none]")?.textContent).toBe(NOTHING_TO_CONFIRM);
  });
});

describe("the checks", () => {
  it("draws each class under its heading with the server's sentence, fix and words", () => {
    mount();
    const blocks = confirmBlocks(VERDICT.risks);
    expect(within(group("risk") as HTMLElement).getByText(VERDICT.risks[0].sentence)).toBeTruthy();
    expect(group("risk")?.textContent).toContain(GROUP.risk + "2· " + riskTail(2));
    expect(line("host.new").textContent).toContain(ackWords(blocks[0]));
    expect(line("reach.new").textContent).toContain(ackWords(blocks[1]));
    expect(group("warning")?.textContent).toContain("Assign them after publishing, or let the identity manager do it.");
    expect(group("warning")?.textContent).toContain(nowAfter("reached", "leaves the role"));
    expect(group("unchecked")?.textContent).toContain(VERDICT.unchecked[0].sentence);
    expect(group("passed")?.textContent).toContain(VERDICT.passed[0].sentence);
    expect(group("info")?.textContent).toContain(VERDICT.info[0].sentence);
  });

  it("leaves out every class with no line, refused included, and folds Passed", () => {
    mount(detailWith({ verdict: { warnings: [], info: [] } }));
    for (const cls of ["refused", "warning", "info"]) expect(group(cls)).toBeNull();
    const passed = group("passed") as HTMLDetailsElement;
    expect(passed.tagName).toBe("DETAILS");
    expect(passed.open).toBe(false);
  });

  it("leaves out the lines the page already says, and a class they empty", () => {
    mount(DETAIL, undefined, ["k-i1"]);
    expect(group("info")).toBeNull();
  });

  it("names the count to type for a typed line of a group, and one tick for a group of ticks", () => {
    const typed = (key: string, object: string): DraftFinding => ({ code: "policy.guardrail-changed", class: "risk", ack: "typed", typed: object, group: "policy.guardrail-changed", object: "PolicySet/" + object, key, sentence: object + " stops asking for approval." });
    const tick = (key: string): DraftFinding => ({ code: "reach.new", class: "risk", ack: "tick", group: "reach.new", object: "Role/" + key, key, sentence: key + " reaches more." });
    mount(detailWith({ verdict: { risks: [typed("a", "set-a"), typed("b", "set-b"), tick("r1"), tick("r2")] } }));
    const words = [...document.querySelectorAll("[data-ack-words]")].map((s) => s.textContent);
    expect(words[0]).toBe("Type 2 when you publish, once for the 2 lines of this kind: republishing the old state cannot undo them.");
    expect(words[2]).toBe("One tick confirms the 2 lines of this kind when you publish: republishing the old state undoes them.");
  });

  // The covers widening is the case no gain row shows: its line, with Now
  // and After, and the holders the client counts from the coverage list.
  it("shows a covers widening with its Now and After and who holds the role", () => {
    mount(COVERS_DETAIL);
    const l = line("role.covers-wider");
    expect(l.textContent).toContain(COVERS_RISK.sentence + " 1 person holds it.");
    expect(l.textContent).toContain(nowAfter("T-1", "T-1, T-2, T-3"));
    expect(l.textContent).toContain("Tick it when you publish");
  });

  it("counts the holders in the plural", () => {
    mount(detailWith({ ...COVERS_DETAIL, verdict: { ...COVERS_DETAIL.verdict, objects: [{ ...COVERAGE, holders_count: 3 }] } }));
    expect(line("role.covers-wider").textContent).toContain(COVERS_RISK.sentence + " 3 people hold it.");
  });

  it("adds no holders sentence for a server that sends no coverage list", () => {
    mount(detailWith({ ...COVERS_DETAIL, verdict: { ...COVERS_DETAIL.verdict, objects: undefined } }));
    expect(line("role.covers-wider").textContent).not.toContain("holds it");
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
