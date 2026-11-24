import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SessionHead } from "./session-head";
import type { SessionRow } from "@/lib/api";

const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const altair: SessionRow = {
  id: "01a1da3d-934c-7be8-bd1a-92e51c045044", user_id: "u-altair", username: "altair", harness: "claude-code/2.2.0", attestation: "managed", status: "active",
  started_at: ago(5 * 60000 + 5000), last_seen: ago(65000), user_kind: "agent", sponsor: "alice",
};

const draw = (row: SessionRow, own = false, sponsorID: string | null = null) => {
  const onRevoke = vi.fn();
  render(<SessionHead row={row} own={own} sponsorID={sponsorID} onRevoke={onRevoke} />);
  return onRevoke;
};
const userLine = () => document.querySelector("[data-session-user]") as HTMLElement | null;

describe("the session's head", () => {
  it("names who on which harness, the cut id in the crumbs, the session tile and the times", () => {
    draw(altair);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("altair on Claude Code 2.2.0");
    expect(screen.getByRole("navigation", { name: "Breadcrumb" }).textContent).toBe("Straza/Sessions/01a1da3d");
    expect(document.querySelector("[data-kind-icon]")?.getAttribute("data-kind-icon")).toBe("session");
    expect(document.querySelector("[data-session-times]")?.textContent).toBe("Started 5 minutes ago. Last seen 1 minute ago.");
  });

  it.each([
    ["the machine and the folder", "dev-laptop", "~/projects/game-audio", "Started 5 minutes ago on dev-laptop in ~/projects/game-audio. Last seen 1 minute ago."],
    ["the machine alone", "dev-laptop", undefined, "Started 5 minutes ago on dev-laptop. Last seen 1 minute ago."],
    ["the folder alone", undefined, "~/x", "Started 5 minutes ago in ~/x. Last seen 1 minute ago."],
    ["neither", undefined, undefined, "Started 5 minutes ago. Last seen 1 minute ago."],
  ])("names where the session started with %s", (_case, host, folder, words) => {
    draw({ ...altair, host, folder });
    const times = document.querySelector("[data-session-times]") as HTMLElement;
    expect(times.textContent).toBe(words);
    for (const [attr, value] of [["data-session-host", host], ["data-session-folder", folder]]) {
      const part = times.querySelector("[" + attr + "]");
      if (!value) {
        expect(part).toBeNull();
        continue;
      }
      expect(part?.textContent).toBe(value);
      expect(part?.className).toContain("font-mono");
      expect(part?.className).toContain("[overflow-wrap:anywhere]");
    }
  });

  it.each([
    ["found", "u-alice", true],
    ["not found", null, false],
  ])("names an agent's sponsor with a person tile, as a link when %s", (_case, sponsorID, link) => {
    draw(altair, false, sponsorID);
    const line = userLine() as HTMLElement;
    expect(line.textContent).toBe("altairAI agent, sponsored byalice");
    const sponsor = line.querySelector("[data-sponsor]") as HTMLElement;
    expect(sponsor.querySelector("[data-kind-icon]")?.getAttribute("data-kind-icon")).toBe("person");
    expect(!!within(sponsor).queryByRole("link", { name: "alice" })).toBe(link);
    if (link) expect(within(sponsor).getByRole("link").getAttribute("href")).toContain("users/u-alice");
  });

  it("names a person with no sponsor", () => {
    draw({ ...altair, username: "carol", user_id: "u-carol", user_kind: "person", sponsor: undefined });
    expect(userLine()?.textContent).toBe("carolPerson");
    expect(userLine()?.querySelector("[data-kind-icon]")?.getAttribute("data-kind-icon")).toBe("person");
  });

  it("draws no kind tile and no kind words for a user the server sends no kind for", () => {
    draw({ ...altair, user_kind: undefined, sponsor: undefined });
    expect(userLine()?.textContent).toBe("altair");
    expect(userLine()?.querySelector("[data-kind-icon]")).toBeNull();
  });

  it("leaves out the user line for a session whose user has no row", () => {
    draw({ ...altair, username: undefined, user_id: "0199aa00-1111-7000-8000-000000000004" });
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("0199aa00 on Claude Code 2.2.0");
    expect(userLine()).toBeNull();
  });

  it("marks the console's own session with a plain chip", () => {
    draw(altair, true);
    expect(document.querySelector("[data-this-console]")?.getAttribute("data-tone")).toBe("plain");
  });

  it("offers Revoke session in red for an active session alone", async () => {
    const onRevoke = draw(altair);
    const button = screen.getByRole("button", { name: "Revoke session" });
    expect(button.className).toContain("text-danger");
    expect(button.querySelector("svg")).toBeTruthy();
    await userEvent.click(button);
    expect(onRevoke).toHaveBeenCalledTimes(1);
  });

  it("offers no Revoke session for a revoked session", () => {
    draw({ ...altair, status: "revoked" });
    expect(screen.queryByRole("button", { name: "Revoke session" })).toBeNull();
    expect(document.querySelector("[data-status-word]")?.textContent).toBe("revoked");
  });
});
