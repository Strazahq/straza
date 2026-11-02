import { describe, expect, it } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SignedIn, clientDiffers } from "./overview-signed-in";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { SessionRow } from "@/lib/api";
import { ADMIN_CLIENT, NOT_MEASURED_ADMIN, NO_GOVERNED, CLIENT_BUILD_DIFFERS, SESSIONS_UNREAD, SIGNED_IN_TITLE, buildsTitle, fleetLine, seatLine } from "@/lib/config-words";
import { NONE } from "@/lib/words";

// The server's /version commit. A box image stamps git describe and a
// goreleaser snapshot stamps its own form, so the versions below differ in
// spelling while the commits say whether the builds do.
const SERVER = "011d686b";

const row = (over: Partial<SessionRow>): SessionRow => ({
  id: "s", user_id: "u", username: "joe", harness: "codex/1.2", client_version: "v1.0.0-1654-g011d686b", client_commit: SERVER,
  attestation: "advisory", status: "active", started_at: "2026-09-13T08:00:00Z", last_seen: "2026-09-13T09:00:00Z", ...over,
});

const CONSOLE = row({ id: "s-alice", username: "alice", harness: "console/1", client_version: undefined, client_commit: undefined, wiring_status: undefined });

const mount = (rows: SessionRow[] | null, denied = false, server = SERVER) =>
  render(<TooltipProvider><SignedIn rows={rows} denied={denied} serverCommit={server} /></TooltipProvider>);

// tip hovers el and reads the tooltip its trigger names.
const tip = async (el: Element) => {
  await userEvent.hover(el);
  return waitFor(() => {
    const id = el.getAttribute("aria-describedby");
    expect(id).toBeTruthy();
    return document.getElementById(id as string)?.textContent || "";
  });
};

describe("which client builds differ from the server", () => {
  const cases: { name: string; over: Partial<SessionRow>; server?: string; marked: boolean }[] = [
    { name: "the same commit under a differently spelled version", over: { client_version: "1.0.0-SNAPSHOT-011d686b" }, marked: false },
    { name: "a different commit", over: { client_version: "1.0.0-SNAPSHOT-87aad807", client_commit: "87aad807" }, marked: true },
    { name: "a seven-character commit under the server's eight", over: { client_commit: "011d686" }, marked: false },
    { name: "an eight-character commit under the server's seven", over: { client_commit: "011d686b" }, server: "011d686", marked: false },
    { name: "a full commit under the server's short one", over: { client_commit: "011d686b1e5c6f0a8d2b4c7e9f1a3b5d7c9e0f12" }, marked: false },
    { name: "a row with no commit, from an older client", over: { client_commit: undefined }, marked: false },
    { name: "a server that names no commit", over: { client_commit: "87aad807" }, server: "", marked: false },
    { name: "an unstamped client build", over: { client_commit: "none" }, marked: false },
    { name: "an unstamped server build", over: { client_commit: "87aad807" }, server: "none", marked: false },
    { name: "a row with no version", over: { client_version: undefined, client_commit: "87aad807" }, marked: false },
  ];
  for (const tc of cases) {
    it(tc.name + (tc.marked ? " is marked" : " is not marked"), () => {
      expect(clientDiffers(row(tc.over), tc.server ?? SERVER)).toBe(tc.marked);
      mount([row(tc.over)], false, tc.server ?? SERVER);
      expect(document.querySelectorAll("[data-build-differs]").length).toBe(tc.marked ? 1 : 0);
      expect(document.querySelector("[data-fleet-line]")?.textContent?.includes("from the server")).toBe(tc.marked);
    });
  }
});

describe("who is signed in", () => {
  it("counts the governed rows in the header and marks clients built from another commit", () => {
    mount([
      row({ id: "a", wiring_status: "current" }),
      row({ id: "b", username: "agent-sam", client_version: "1.0.0-SNAPSHOT-87aad807", client_commit: "87aad807", wiring_status: "unmeasured" }),
      row({ id: "c", username: "agent-kim", client_version: "1.1.2", client_commit: "0ff1ce55", wiring_status: "current" }),
      CONSOLE,
    ]);
    expect((document.querySelector("[data-fleet-line]") as HTMLElement).textContent).toBe(fleetLine(3, { current: 2, unmeasured: 1 }, 2));
    expect(document.querySelector("[data-fleet-line]")?.textContent).toContain("2 client builds differ from the server");
    expect(document.querySelectorAll("[data-build-differs]").length).toBe(2);
    expect(screen.getAllByText(CLIENT_BUILD_DIFFERS).length).toBe(2);
  });

  it("says one client build in the singular", () => {
    mount([row({ client_commit: "87aad807" })]);
    expect(document.querySelector("[data-fleet-line]")?.textContent).toContain("1 client build differs from the server");
  });

  it("names both builds on the mark and the full version on the version", async () => {
    const long = "v1.0.0-1654-g87aad807-dirty-and-a-long-tail";
    mount([row({ client_version: long, client_commit: "87aad807" })]);
    const version = document.querySelector("[data-client-version]") as HTMLElement;
    expect(version.textContent).toBe(long);
    expect(version.className).toContain("truncate");
    expect(await tip(version)).toBe(long);
    const mark = document.querySelector("[data-build-differs]") as HTMLElement;
    expect(mark.textContent).toBe(CLIENT_BUILD_DIFFERS);
    expect(await tip(mark)).toBe(buildsTitle("87aad807", SERVER));
    expect(buildsTitle("87aad807", SERVER)).toBe("This session started on 87aad807. The server runs 011d686b.");
  });

  it("names the server's commit at the client's length, at least eight characters", async () => {
    mount([row({ client_commit: "87aad807" })], false, "b74dc723348e99e96059dc783a133158ec3dfe55");
    expect(await tip(document.querySelector("[data-build-differs]") as HTMLElement)).toBe(buildsTitle("87aad807", "b74dc723"));
  });

  it("cuts a long name and harness inside their columns and keeps them whole on hover", () => {
    mount([row({ username: "joe-java-developer-agent-with-a-long-name", harness: "claude-code-with-a-long-harness-name" })]);
    const who = document.querySelector("[data-who]") as HTMLElement;
    const harness = document.querySelector("[data-harness]") as HTMLElement;
    expect(who.className).toContain("truncate");
    expect(who.title).toBe("joe-java-developer-agent-with-a-long-name");
    expect(harness.className).toContain("truncate");
    expect(harness.title).toBe("claude-code-with-a-long-harness-name");
  });

  it("keeps the fixed column layout whether or not a row is marked", () => {
    const widths = () => Array.from(document.querySelectorAll("colgroup col")).map((c) => (c as HTMLElement).style.width);
    mount([row({})]);
    const plain = widths();
    expect(document.querySelector("table")?.className).toContain("table-fixed");
    document.body.innerHTML = "";
    mount([row({ client_commit: "87aad807" })]);
    expect(widths()).toEqual(plain);
    expect(plain.length).toBe(5);
  });

  it("does not call a newer client older than the server", () => {
    mount([row({ client_version: "v1.0.0-1700-g87aad807", client_commit: "87aad807" })]);
    expect(screen.getByText(CLIENT_BUILD_DIFFERS)).toBeTruthy();
    expect(screen.queryByText(/older/)).toBeNull();
  });

  it("reads an admin harness as the admin client with no wiring measurement", () => {
    mount([CONSOLE]);
    expect(screen.getByText(ADMIN_CLIENT)).toBeTruthy();
    expect(screen.getByText(NOT_MEASURED_ADMIN)).toBeTruthy();
    expect((document.querySelector("[data-fleet-line]") as HTMLElement).textContent).toBe(NO_GOVERNED);
  });

  it("marks an admin client of another commit on its row and leaves it out of the governed count", () => {
    mount([row({ harness: "strazactl/1", client_commit: "87aad807" }), row({ id: "g" })]);
    expect(document.querySelectorAll("[data-build-differs]").length).toBe(1);
    expect((document.querySelector("[data-fleet-line]") as HTMLElement).textContent).toBe(fleetLine(1, { "not reported": 1 }, 0));
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
