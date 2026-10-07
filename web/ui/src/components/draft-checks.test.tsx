import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftChecks, VerdictStrip } from "./draft-checks";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError } from "@/lib/api";
import { CONTACT, CONTACT_FAILED, GROUP, NOTHING_REFUSED, ackWords, nowAfter } from "@/lib/drafts-words";
import { DETAIL, VERDICT, detailWith } from "@/test/drafts-fixture";

// The checks of a draft: the strip
// counts each class and jumps to the lines, every class renders its lines
// in the server's words, and Contact it now sits on the line of a remote
// server nobody read.

const mount = (d = DETAIL, onContact = vi.fn(async () => undefined)) => {
  render(<TooltipProvider><DraftChecks detail={d} onContact={onContact} /></TooltipProvider>);
  return onContact;
};
const group = (cls: string) => document.querySelector('[data-group="' + cls + '"]') as HTMLElement | null;

describe("the verdict strip", () => {
  it("counts refused, widens access, warnings and not checked, and jumps to the checks", async () => {
    const onJump = vi.fn();
    render(<VerdictStrip verdict={VERDICT} onJump={onJump} />);
    const counts = [...document.querySelectorAll("[data-strip]")].map((b) => (b as HTMLElement).textContent);
    expect(counts).toEqual(["0refused", "2widen access", "2warnings", "1not checked"]);
    await userEvent.click(screen.getByRole("button", { name: "2 widen access: go to the checks" }));
    expect(onJump).toHaveBeenCalled();
  });
});

describe("the checks", () => {
  it("draws each class under its heading with the server's sentence, fix and words", () => {
    mount();
    expect(group("refused")?.textContent).toContain(NOTHING_REFUSED);
    expect(within(group("risk") as HTMLElement).getByText(VERDICT.risks[0].sentence)).toBeTruthy();
    expect(group("risk")?.textContent).toContain(GROUP.risk);
    expect(group("risk")?.textContent).toContain(ackWords("typed", "api.githubcopilot.com"));
    expect(group("risk")?.textContent).toContain(ackWords("tick", undefined));
    expect(group("warning")?.textContent).toContain("Assign them after publishing, or let the identity manager do it.");
    expect(group("warning")?.textContent).toContain(nowAfter("reached", "leaves the role"));
    expect(group("unchecked")?.textContent).toContain(VERDICT.unchecked[0].sentence);
    expect(group("passed")?.textContent).toContain(VERDICT.passed[0].sentence);
    expect(group("info")?.textContent).toContain(VERDICT.info[0].sentence);
  });

  it("leaves out a class with no line, but always says what was refused", () => {
    mount(detailWith({ verdict: { warnings: [], info: [] } }));
    expect(group("warning")).toBeNull();
    expect(group("info")).toBeNull();
    expect(group("refused")).not.toBeNull();
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
