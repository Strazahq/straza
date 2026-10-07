import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { SignedIn } from "./overview-signed-in";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { SessionRow } from "@/lib/api";
import { ADMIN_CLIENT, NOT_MEASURED_ADMIN, NO_GOVERNED, CLIENT_VERSION_DIFFERS, SESSIONS_UNREAD, SIGNED_IN_TITLE, fleetLine, seatLine } from "@/lib/config-words";
import { NONE } from "@/lib/words";

const STRAZAD = "v1.0.0-1137";

const row = (over: Partial<SessionRow>): SessionRow => ({
  id: "s", user_id: "u", username: "joe", harness: "codex/1.2", client_version: STRAZAD,
  attestation: "advisory", status: "active", started_at: "2026-09-13T08:00:00Z", last_seen: "2026-09-13T09:00:00Z", ...over,
});

const CONSOLE = row({ id: "s-alice", username: "alice", harness: "console/1", client_version: undefined, wiring_status: undefined });

const mount = (rows: SessionRow[] | null, denied = false) =>
  render(<TooltipProvider><SignedIn rows={rows} denied={denied} strazad={STRAZAD} /></TooltipProvider>);

describe("who is signed in", () => {
  it("counts the governed rows in the header and marks clients whose version differs", () => {
    mount([row({ id: "a", wiring_status: "current" }), row({ id: "b", username: "agent-sam", client_version: "v1.0.0-1105", wiring_status: "unmeasured" }), CONSOLE]);
    expect((document.querySelector("[data-fleet-line]") as HTMLElement).textContent).toBe(fleetLine(2, { current: 1, unmeasured: 1 }, 1));
    expect(document.querySelectorAll("[data-version-differs]").length).toBe(1);
    expect(screen.getByText(CLIENT_VERSION_DIFFERS)).toBeTruthy();
  });

  it("does not call a newer client older than the server", () => {
    mount([row({ client_version: "v1.0.0-1200" })]);
    expect(screen.getByText(CLIENT_VERSION_DIFFERS)).toBeTruthy();
    expect(document.querySelector("[data-fleet-line]")?.textContent).toContain("1 client version differs from the server");
    expect(screen.queryByText(/older/)).toBeNull();
  });

  it("reads an admin harness as the admin client with no wiring measurement", () => {
    mount([CONSOLE]);
    expect(screen.getByText(ADMIN_CLIENT)).toBeTruthy();
    expect(screen.getByText(NOT_MEASURED_ADMIN)).toBeTruthy();
    expect((document.querySelector("[data-fleet-line]") as HTMLElement).textContent).toBe(NO_GOVERNED);
  });

  it("reads a governed row the server could not classify as the absent word", () => {
    mount([row({ wiring_status: undefined })]);
    expect(screen.getByText(NONE)).toBeTruthy();
    expect(document.querySelector("[data-fleet-line]")?.textContent).toContain("1 not reported");
  });

  it("says the seat cannot read the sessions instead of failing the panel", () => {
    mount(null, true);
    expect(screen.getByText(seatLine(SIGNED_IN_TITLE, "sessions:read"))).toBeTruthy();
  });

  it("keeps a missing session read distinct from an empty list", () => {
    mount(null);
    expect(screen.getByText(SESSIONS_UNREAD)).toBeTruthy();
    expect(screen.queryByText(NO_GOVERNED)).toBeNull();
  });
});
