import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SessionSheet } from "./session-sheet";
import type { SessionRow } from "@/lib/api";
import { ATT_TITLE, RECORD_AUDIT, RECORD_TRANSCRIPT, WIRING_TITLE, wiringPhrase } from "@/lib/session-words";
import { snapshot } from "@/lib/session";
import { absTime } from "@/lib/words";

// The stamps are relative to now, so relTimeText reads the same words on
// every run instead of falling back to the absolute stamp after a day.
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const STARTED = ago(2 * 3600 * 1000);
const SEEN = ago(4 * 60 * 1000);

const OWN = "0199cf12-4b1e-7a3c-9d21-0b6e4f2a8c10";

const session: SessionRow = {
  id: OWN,
  user_id: "01997f00-1111-7000-8000-000000000001",
  username: "joe-java-developer-agent",
  harness: "claude-code/2.1.0",
  client_version: "v1.0.0-1121",
  attestation: "managed",
  status: "active",
  started_at: STARTED,
  last_seen: SEEN,
  wiring_status: "current",
  wiring_hash: "sha256:9c1e04b7aa1132f6",
};

vi.mock("@/lib/session", async (orig) => ({
  ...(await orig<typeof import("@/lib/session")>()),
  snapshot: vi.fn(),
}));

const onRevoke = vi.fn();
const onOpenUser = vi.fn();
const onOpenTranscript = vi.fn();
const onOpenAudit = vi.fn();

const mount = (row: Partial<SessionRow> = {}) => render(
  <SessionSheet
    session={{ ...session, ...row }}
    open
    onOpenChange={() => {}}
    onRevoke={onRevoke}
    onOpenUser={onOpenUser}
    onOpenTranscript={onOpenTranscript}
    onOpenAudit={onOpenAudit}
  />,
);

// fact reads the value the facts grid renders under one label.
const fact = (label: string) => {
  const dt = [...document.querySelectorAll("dt")].find((d) => d.textContent === label);
  return (dt && dt.nextElementSibling && dt.nextElementSibling.textContent) || "";
};

describe("the session sheet", () => {
  beforeEach(() => {
    vi.mocked(snapshot).mockReturnValue({ user: "admin", roles: [], grants: "full", expiresIn: 300, sessionID: "another-session" });
    onRevoke.mockReset();
    onOpenUser.mockReset();
    onOpenTranscript.mockReset();
    onOpenAudit.mockReset();
  });

  it("heads with the session, its status and who holds it", () => {
    mount();
    const title = screen.getByRole("heading", { level: 2 });
    expect(title.textContent).toContain("Session");
    expect(title.textContent).toContain("0199cf12-4b1e…");
    expect(title.textContent).toContain("active");
    expect(screen.getByRole("dialog").textContent).toContain("joe-java-developer-agent on claude-code/2.1.0, straza v1.0.0-1121. Started 2 h ago, seen 4 m ago.");
  });

  it("states every fact of the session, with the attestation and wiring sentences on the badges", () => {
    mount();
    expect(fact("Attestation")).toContain("managed");
    expect(fact("Attestation")).toContain("hashes verified against the registry");
    expect(screen.getByText("managed").getAttribute("title")).toBe(ATT_TITLE.managed);
    expect(fact("Wiring")).toContain("current");
    expect(fact("Wiring")).toContain(wiringPhrase("current"));
    expect(fact("Wiring")).toContain("9c1e04b7aa11…");
    expect(screen.getByText("current").getAttribute("title")).toBe(WIRING_TITLE.current);
    expect(fact("Session id")).toBe(OWN);
    expect(fact("User")).toBe("joe-java-developer-agent");
    expect(fact("Harness")).toBe("claude-code/2.1.0");
    expect(fact("Client build")).toBe("v1.0.0-1121");
    expect(fact("Started")).toBe(absTime(STARTED));
    expect(fact("Last seen")).toBe(absTime(SEEN));
  });

  it("reads an absent wiring, client build and username as words", () => {
    mount({ wiring_status: undefined, wiring_hash: undefined, client_version: undefined, username: undefined });
    expect(fact("Wiring")).toBe("none");
    expect(fact("Client build")).toBe("none");
    expect(fact("User")).toBe("01997f00-1111…");
  });

  it("wears the this console badge only on the session this browser holds", () => {
    mount();
    expect(screen.queryByText("this console")).toBeNull();
    vi.mocked(snapshot).mockReturnValue({ user: "admin", roles: [], grants: "full", expiresIn: 300, sessionID: OWN });
    mount();
    expect(screen.getAllByText("this console").length).toBe(1);
  });

  it("opens the transcript and the audit trail from the Record section", async () => {
    mount();
    expect(screen.getByText(RECORD_TRANSCRIPT)).toBeTruthy();
    expect(screen.getByText(RECORD_AUDIT)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Open transcript" }));
    expect(onOpenTranscript).toHaveBeenCalledWith(OWN);
    await userEvent.click(screen.getByRole("button", { name: "Open audit trail" }));
    expect(onOpenAudit).toHaveBeenCalledWith(OWN);
    await userEvent.click(screen.getByRole("button", { name: "joe-java-developer-agent" }));
    expect(onOpenUser).toHaveBeenCalledWith("01997f00-1111-7000-8000-000000000001");
  });

  it("carries Revoke session on an active session and nothing on a revoked one", async () => {
    mount();
    const revoke = screen.getByRole("button", { name: "Revoke session" });
    expect(revoke.getAttribute("data-variant")).toBe("outline");
    await userEvent.click(revoke);
    expect(onRevoke).toHaveBeenCalledWith(expect.objectContaining({ id: OWN }));
    expect(screen.getAllByRole("button", { name: "Close" }).some((b) => b.getAttribute("data-variant") === "outline")).toBe(true);
  });

  it("hides Revoke session on a session that is no longer active", () => {
    mount({ status: "revoked" });
    expect(screen.queryByRole("button", { name: "Revoke session" })).toBeNull();
    expect(screen.getByText("revoked")).toBeTruthy();
  });

  it("renders a username that looks like markup as text", () => {
    mount({ username: "<img src=x onerror=alert(1)>" });
    expect(screen.getAllByText("<img src=x onerror=alert(1)>").length).toBeGreaterThan(0);
    expect(document.querySelector("img")).toBeNull();
  });
});
