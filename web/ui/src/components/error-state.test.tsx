import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { FetchError, RefusedError } from "./error-state";
import { ApiError } from "@/lib/api";
import { refused } from "@/lib/say";

// The read block says "could not be read" once. A detail that opens with
// what could not be read is shown as it is, with that opening as the
// heading. Any other detail follows the subject's own line.
const block = () => document.querySelector("[data-fetch-error]") as HTMLElement;
const heading = () => (block().querySelector("span") as HTMLElement).textContent;

describe("the read error block", () => {
  it.each([
    ["a detail with the server's why", {}, "The repos could not be read: unknown server. Reload to try again.", "The repos could not be read: unknown server. Reload to try again."],
    ["a detail with an unanswered call", {}, "The audit chain could not be read because Straza did not answer. Check that it is running, then reload.", "The audit chain could not be read because Straza did not answer. Check that it is running, then reload."],
    ["a detail that opens another way", {}, "The listing credential was refused. Set it again on the server page.", "Repos could not be read. The listing credential was refused. Set it again on the server page."],
    ["a heading of its own", { heading: "The repos may be out of date." }, "The repos could not be read: unknown server. Reload to try again.", "The repos may be out of date. The repos could not be read: unknown server. Reload to try again."],
    ["a last good read", { lastRead: new Date("2026-09-12T10:45:01Z") }, "The repos could not be read: unknown server. Reload to try again.", "The repos could not be read: unknown server. Reload to try again. Last good read: 10:45:01 UTC."],
  ])("reads %s", (_, extra, detail, want) => {
    render(<FetchError subject="Repos" detail={detail} {...extra} />);
    expect(block().textContent).toBe(want);
  });

  it("sets the opening of a detail in the heading style", () => {
    const { unmount } = render(<FetchError subject="Repos" detail="The repos could not be read: unknown server. Reload to try again." />);
    expect(heading()).toBe("The repos could not be read:");
    unmount();
    render(<FetchError subject="Audit" detail="The audit chain could not be read because Straza did not answer. Check that it is running, then reload." />);
    expect(heading()).toBe("The audit chain could not be read");
  });
});

// The refused block says refused once. A message from refused folds its
// opening into the heading, and a write Straza did not answer is not
// called refused, since it may have landed.
describe("the refused error block", () => {
  const refusedBlock = () => document.querySelector("[data-refused-error]") as HTMLElement;
  it.each([
    ["a refusal Straza spoke", {}, refused(new ApiError("session is already revoked", 409)), "Revoke session refused: session is already revoked. Fix what it names, then try again.", "Revoke session refused:"],
    ["a write Straza did not answer", {}, refused(new ApiError("unreachable", 0, true)), "Revoke session not confirmed. Straza did not answer. The change may have been saved. Reload to check before trying again.", "Revoke session not confirmed."],
    ["a sentence of the caller's own", {}, "The reason is too long. Shorten it.", "Revoke session refused. The reason is too long. Shorten it.", "Revoke session refused."],
    ["a heading of its own", { heading: "Not revoked." }, refused(new ApiError("session is already revoked", 409)), "Not revoked. Straza refused it: session is already revoked. Fix what it names, then try again.", "Not revoked."],
  ])("reads %s", (_, extra, message, want, head) => {
    render(<RefusedError subject="Revoke session" message={message} {...extra} />);
    expect(refusedBlock().textContent).toBe(want);
    expect((refusedBlock().querySelector("span") as HTMLElement).textContent).toBe(head);
  });
});
