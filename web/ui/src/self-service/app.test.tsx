import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { SelfServiceApp } from "./app";
import { type ServerRow, readSelf, selfServers } from "@/lib/api";

// The shell with a signed-in session and no enrolment, driven to each tab
// through the address. The tabs are the real ones over a mocked API, so a
// shell that hands a tab an unstable door shows up here as the boundary's
// error text instead of the tab.

// sessionStub is the hook's answer, mutable per test so the header can be
// read for an area admin, a server admin and a plain person.
const sessionStub = vi.hoisted(() => ({
  state: { kind: "signed-in", user: "alice", grants: "full", servers: 0, expiresAt: Date.now() + 300000 } as Record<string, unknown>,
}));
vi.mock("@/lib/use-session", () => ({
  useSession: () => ({ state: sessionStub.state, signedIn: vi.fn(), signOut: vi.fn() }),
}));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  readSelf: vi.fn(),
  selfServers: vi.fn(),
}));
vi.mock("./store", async (orig) => ({
  ...(await orig<typeof import("./store")>()),
  storageAvailable: () => true,
  storageOutlook: async () => "unknown",
  loadEnrollment: async () => null,
}));
vi.mock("./push", () => ({ teardown: vi.fn(), retireOldWorker: vi.fn(), support: () => ({ available: false, why: "" }), sync: vi.fn(), enable: vi.fn(), disable: vi.fn() }));

const midpoint: ServerRow = { app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "own", reached: true, connected: false };
const none: ServerRow = { app: "demo-tools", runtime: "remote", kind: "none", reached: true };

beforeEach(() => {
  vi.mocked(readSelf).mockResolvedValue({ username: "alice", user_kind: "human", admin_grants: "full", enroll_channels: ["browser", "mobile"], sponsored: ["joe-java-developer-agent", "sam-sre-agent"] });
  vi.mocked(selfServers).mockImplementation(async (user = "") => (user ? [none, midpoint] : []));
});
afterEach(() => window.history.replaceState(null, "", "/self-service/requests"));

describe("the self-service shell", () => {
  it("renders the Credentials tab with the agents' rows and the count, without an update loop", async () => {
    window.history.replaceState(null, "", "/self-service/credentials");
    render(<SelfServiceApp />);
    await waitFor(() => expect(screen.getAllByText("midpoint").length).toBeGreaterThanOrEqual(2), { timeout: 5000 });
    expect(screen.queryByText(/This screen hit an error/)).toBeNull();
    expect(screen.getByText("Signed in as alice")).toBeTruthy();
    await waitFor(() => expect(document.querySelector('[data-count="credentials"]')).not.toBeNull());
    expect(vi.mocked(selfServers)).toHaveBeenCalledTimes(3);
  });

  it("offers Enable this browser on the This browser tab to an account that may enrol a browser", async () => {
    window.history.replaceState(null, "", "/self-service/browser");
    render(<SelfServiceApp />);
    await waitFor(() => expect(document.querySelector("[data-enable-browser]")).not.toBeNull(), { timeout: 5000 });
    expect(screen.queryByText(/This screen hit an error/)).toBeNull();
  });

  it("offers Add a phone beside Enable this browser to an account that may enrol a phone", async () => {
    window.history.replaceState(null, "", "/self-service/browser");
    render(<SelfServiceApp />);
    await waitFor(() => expect(document.querySelector("[data-add-phone]")).not.toBeNull(), { timeout: 5000 });
    expect(document.querySelector("[data-enable-browser]")).not.toBeNull();
  });
  it("offers the console door to an area admin, to a server admin, and to nobody else", async () => {
    sessionStub.state = { kind: "signed-in", user: "alice", grants: "full", servers: 0, expiresAt: Date.now() + 300000 };
    const { unmount: u1 } = render(<SelfServiceApp />);
    expect(await screen.findByRole("link", { name: /Open the console/ })).toBeTruthy();
    u1();
    sessionStub.state = { kind: "signed-in", user: "carol", grants: "", servers: 1, expiresAt: Date.now() + 300000 };
    const { unmount: u2 } = render(<SelfServiceApp />);
    expect(await screen.findByRole("link", { name: /Open the console/ })).toBeTruthy();
    u2();
    sessionStub.state = { kind: "signed-in", user: "grace", grants: "", servers: 0, expiresAt: Date.now() + 300000 };
    render(<SelfServiceApp />);
    await screen.findByText(/Signed in as grace/);
    expect(screen.queryByRole("link", { name: /Open the console/ })).toBeNull();
    sessionStub.state = { kind: "signed-in", user: "alice", grants: "full", servers: 0, expiresAt: Date.now() + 300000 };
  });
});
