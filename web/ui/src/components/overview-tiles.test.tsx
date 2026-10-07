import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Tiles } from "./overview-tiles";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { OverviewAnswer } from "@/lib/api";
import { CHAIN_UNREAD, CHAIN_UNVERIFIED, NOT_READ, NO_ANSWER, TILE, TILE_UNREAD, lacksGrant } from "@/lib/config-words";
import { navigate } from "@/lib/router";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const ANSWER: OverviewAnswer = {
  users: { total: 4, active: 3 },
  sessions: { total: 9, active: 2 },
  apps: { total: 2, running: 2 },
  policies: { total: 1, active: 1 },
  audit: { head_seq: 120 },
  push: { lane_up: true, connected: 1 },
};

const mount = (answer: OverviewAnswer | null, chain: Parameters<typeof Tiles>[0]["chain"] = { word: "intact", seq: 120 }) =>
  render(<TooltipProvider><Tiles answer={answer} approvals={[]} approvalsDenied={false} chain={chain} /></TooltipProvider>);

const tile = (key: string) => document.querySelector('[data-tile="' + key + '"]') as HTMLElement;
const value = (key: string) => (tile(key).querySelector("[data-tile-value]") as HTMLElement).textContent;
const detail = (key: string) => (tile(key).querySelector("[data-tile-detail]") as HTMLElement).textContent;

describe("the six tiles", () => {
  it("opens the area a tile counts", async () => {
    mount(ANSWER);
    await userEvent.click(screen.getByRole("link", { name: new RegExp(TILE.servers.label) }));
    expect(vi.mocked(navigate)).toHaveBeenCalledWith("servers");
  });

  it("says no answer rather than zero when the read behind a number never landed", () => {
    mount(null, { word: "unknown", seq: 0 });
    expect(value("sessions")).toBe(NO_ANSWER);
    expect(value("users")).toBe(NO_ANSWER);
    expect(value("audit")).toBe("Not verified");
    expect(detail("audit")).toBe(CHAIN_UNREAD);
  });

  it("degrades the approvals tile alone when this seat may not read it", () => {
    render(<TooltipProvider><Tiles answer={ANSWER} approvals={null} approvalsDenied chain={{ word: "intact", seq: 120 }} /></TooltipProvider>);
    expect(value("approvals")).toBe(NOT_READ);
    expect(detail("approvals")).toBe(lacksGrant("approvals:read"));
    expect(value("users")).toBe("3of 4");
  });

  it("says the chain was not re-hashed when the browser cannot hash", () => {
    mount(ANSWER, { word: "unverified", seq: 120 });
    expect(detail("audit")).toBe(CHAIN_UNVERIFIED);
  });

  it("carries a help sentence on every tile", () => {
    mount(ANSWER);
    for (const t of Object.values(TILE)) expect(screen.getByRole("button", { name: "Help: " + t.label })).toBeTruthy();
  });
});

it("distinguishes missing fields from confirmed zero and from permission limits", () => {
  const view = mount({ sessions: { active: 0 }, users: { total: 5 } });
  expect(value("sessions")).toBe("0");
  expect(value("users")).toBe(NO_ANSWER);
  expect(detail("users")).toBe(TILE_UNREAD);
  expect(detail("servers")).toBe(TILE_UNREAD);
  view.unmount();
  render(<TooltipProvider><Tiles answer={null} approvals={null} approvalsDenied={false} chain={{ word: "unknown", seq: 0 }} denied /></TooltipProvider>);
  expect(value("sessions")).toBe(NOT_READ);
  expect(detail("sessions")).toBe("Permission required");
});
it("labels retained data with its last successful read", () => {
  render(<TooltipProvider><Tiles answer={ANSWER} approvals={[]} approvalsDenied={false} chain={{ word: "intact", seq: 120 }} stale={{ summary: "2 m ago", chain: "3 m ago" }} /></TooltipProvider>);
  expect(detail("users")).toContain("Last read 2 m ago");
  expect(detail("audit")).toContain("Last checked 3 m ago");
});
