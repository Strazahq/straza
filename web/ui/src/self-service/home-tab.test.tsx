import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApprovalRow, type SelfAnswer, type SelfDevice, selfDevices } from "@/lib/api";
import { SelfContext, type SelfState } from "./context";
import type { CredentialRow } from "./credential-rows";
import { READING } from "./credential-words";
import { HomeTab } from "./home-tab";
import { support } from "./push";
import type { Enrollment } from "./store";
import * as W from "./words";

// Home's reads are the shell's, handed in through the shared state, and
// the device list it reads itself. The queue under the cards has its own
// suite, so here it stands in as the heading Review leads to.
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  selfDevices: vi.fn(),
}));
vi.mock("./push", () => ({ support: vi.fn() }));
vi.mock("./requests-tab", () => ({
  RequestsTab: ({ home }: { home?: boolean }) => (
    <section data-home-waiting data-queue={home ? "home" : "tab"}><h2>{"Waiting"}</h2></section>
  ),
}));

const at = (s: number) => new Date(Date.now() + s * 1000).toISOString();

const enrollment = {
  deviceId: "apd_here", deviceToken: "dt-live", tokenExpiresAt: Date.now() + 86400000,
  project: null, user: { id: "u-alice", username: "alice" },
  webpush: { vapid_public_key: "BFxc" }, keys: {} as CryptoKeyPair,
  deviceName: "Firefox on Windows", enrolledAt: at(-86400),
} as Enrollment;

const BROWSER_ONLY: SelfAnswer = { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [] };
const BOTH: SelfAnswer = { ...BROWSER_ONLY, enroll_channels: ["browser", "mobile"] };
const PHONE_ONLY: SelfAnswer = { ...BROWSER_ONLY, enroll_channels: ["mobile"] };
const NO_ROLE: SelfAnswer = { ...BROWSER_ONLY, enroll_channels: [] };

const request = (id: string, who: string, summary: string, left: number): ApprovalRow => ({
  id, state: "pending", createdAt: at(-30), expiresAt: at(left), decidedAt: null, user: who, username: who, session: "s", rule: "r", set: "p",
  lane: "hook", summary, justification: "", approverRoles: [], approverUsers: ["alice"], selfApproval: false, mode: "approve", decidedBy: "", decidedByName: "",
});
const push = request("apr_push", "altair", "shell.exec: git push origin main", 100);
const sum = request("apr_sum", "joe-java-developer-agent", "mcp.call demo-tools:get-sum", 400);

const server = (app: string, fields: Partial<CredentialRow>): CredentialRow => ({ app, runtime: "remote", kind: "token", reached: true, who: "", ...fields });

const here: SelfDevice = { id: "apd_here", name: "Firefox on Windows", platform: "browser", key_security_level: "software", created_at: at(-86400), notifies: false };
const iphone: SelfDevice = { id: "apd_iphone", name: "iPhone", platform: "ios", key_security_level: "hardware", attestation: "apple_app_attest", created_at: at(-864000), notifies: true };

const setSheet = vi.fn();
const openSignIn = vi.fn();
const refreshSelf = vi.fn(async () => undefined);
const readServers = vi.fn();

function shared(over: Partial<SelfState> = {}): SelfState {
  return {
    session: { kind: "signed-in", user: "alice", grants: "", servers: 0 },
    self: BROWSER_ONLY,
    refreshSelf,
    enrollment,
    setEnrollment: vi.fn(),
    storage: { usable: true, outlook: "durable" },
    openSignIn,
    signedIn: vi.fn(),
    notice: null,
    setNotice: () => undefined,
    sheets: { enable: false, phone: false },
    setSheet,
    setCount: () => undefined,
    waiting: [],
    setWaiting: () => undefined,
    servers: [],
    readServers,
    ...over,
  };
}

const mount = (over: Partial<SelfState> = {}) => render(
  <TooltipProvider>
    <SelfContext.Provider value={shared(over)}><HomeTab /></SelfContext.Provider>
  </TooltipProvider>,
);

const SIGNED_OUT: Partial<SelfState> = { session: { kind: "signed-out" }, self: null, enrollment: null, waiting: null, servers: null };
const card = (name: string) => document.querySelector("[data-card=" + name + "]") as HTMLElement | null;
const lineOf = (name: string) => (card(name)!.querySelector("[data-card-line]") as HTMLElement).textContent;
const sentenceOf = (name: string) => (card(name)!.querySelector("[data-card-sentence]") as HTMLElement | null)?.textContent ?? null;
const buttonsOf = (name: string) => within(card(name)!).queryAllByRole("button").map((b) => b.textContent);
const tileOf = (name: string) => card(name)!.querySelector("[data-tile]")?.getAttribute("data-tile") || card(name)!.querySelector("[data-kind-icon]")?.getAttribute("data-kind-icon");

describe("Home", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(selfDevices).mockResolvedValue({ devices: [here] });
    vi.mocked(support).mockReturnValue({ available: true, why: "" });
    window.history.replaceState(null, "", "/self-service/");
  });

  describe("the setup card", () => {
    it("asks a signed-in person to set this browser up, with Add a phone for an account that may add one", async () => {
      mount({ enrollment: null, self: BOTH });
      const setup = document.querySelector("[data-setup-card]") as HTMLElement;
      expect(within(setup).getByRole("heading", { name: W.SETUP_HEAD })).toBeTruthy();
      // The title and the buttons say it all for a signed-in person.
      expect(setup.querySelector("p")).toBeNull();
      expect(setup.querySelector("[data-kind-icon=browser]")).not.toBeNull();
      await userEvent.click(within(setup).getByRole("button", { name: W.SETUP_BROWSER }));
      expect(setSheet).toHaveBeenCalledWith("enable", true);
      await userEvent.click(within(setup).getByRole("button", { name: W.ADD_PHONE }));
      expect(setSheet).toHaveBeenCalledWith("phone", true);
      // The setup card stands above the three cards.
      expect(setup.compareDocumentPosition(card("requests")!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("offers no phone to an account that may set up a browser only", () => {
      mount({ enrollment: null });
      expect(document.querySelector("[data-setup-browser]")).not.toBeNull();
      expect(screen.queryByRole("button", { name: W.ADD_PHONE })).toBeNull();
    });

    it("asks a signed-out visitor to sign in first", async () => {
      mount(SIGNED_OUT);
      const setup = document.querySelector("[data-setup-card]") as HTMLElement;
      expect(within(setup).getByText(W.SETUP_SIGN_IN)).toBeTruthy();
      expect(setup.querySelector("[data-setup-browser]")).toBeNull();
      await userEvent.click(within(setup).getByRole("button", { name: W.SIGN_IN }));
      expect(openSignIn).toHaveBeenCalledTimes(1);
    });

    it.each([
      ["this browser is set up", {}],
      ["the account approves from a phone only", { enrollment: null, self: PHONE_ONLY }],
      ["the account has no role to approve", { enrollment: null, self: NO_ROLE }],
      ["Straza has not said yet what the account may set up", { enrollment: null, self: null }],
    ] as [string, Partial<SelfState>][])("is not drawn when %s", (_what, over) => {
      mount(over);
      expect(document.querySelector("[data-setup-card]")).toBeNull();
    });

    it("gives way to a red line when this browser will not keep a key", () => {
      mount({ enrollment: null, storage: { usable: false, outlook: "unknown" } });
      expect(screen.getByRole("alert").textContent).toBe(W.NO_STORAGE);
      expect(document.querySelector("[data-setup-card]")).toBeNull();
    });

    it("gives way to the failed read with Try again", async () => {
      mount({ enrollment: null, self: "error" });
      expect(screen.getByText(W.ELIGIBILITY_UNREAD)).toBeTruthy();
      await userEvent.click(screen.getByRole("button", { name: W.TRY_AGAIN }));
      expect(refreshSelf).toHaveBeenCalledTimes(1);
    });
  });

  describe("the requests card", () => {
    it("counts what waits and names the request that runs out first, with Review", () => {
      mount({ waiting: [sum, push] });
      expect(tileOf("requests")).toBe("warn");
      expect(lineOf("requests")).toBe(W.waitsForYou(2));
      expect(sentenceOf("requests")).toMatch(/^altair wants to run git push origin main\. 1 m [34]\d s left\.$/);
      expect(buttonsOf("requests")).toEqual([W.REVIEW]);
    });

    it("opens the one request that waits on the Requests tab on Review", async () => {
      mount({ waiting: [push] });
      await userEvent.click(screen.getByRole("button", { name: W.REVIEW }));
      expect(window.location.pathname).toBe("/self-service/requests/apr_push");
    });

    it("moves focus to the Waiting heading on Review when several wait, and stays on Home", async () => {
      mount({ waiting: [push, sum] });
      await userEvent.click(screen.getByRole("button", { name: W.REVIEW }));
      expect(document.activeElement?.textContent).toBe(W.WAITING_HEAD);
      expect(window.location.pathname).toBe("/self-service/");
    });

    it("says nothing waits in a neutral tile, with no button", () => {
      mount();
      expect(tileOf("requests")).toBe("plain");
      expect(lineOf("requests")).toBe(W.NOTHING_WAITS_CARD);
      expect(sentenceOf("requests")).toBeNull();
      expect(buttonsOf("requests")).toEqual([]);
    });

    it("counts before setup what a device of the person could decide, and says to set one up, with no button", () => {
      const { unmount } = mount({ enrollment: null, waiting: [push, sum] });
      expect(tileOf("requests")).toBe("warn");
      expect(lineOf("requests")).toBe(W.waitsForYou(2));
      expect(sentenceOf("requests")).toBe(W.answerAfterBrowser("altair", "wants to run", "git push origin main"));
      expect(buttonsOf("requests")).toEqual([]);
      unmount();
      // A phone is named only to an account that may add one.
      mount({ enrollment: null, self: BOTH, waiting: [push, sum] });
      expect(sentenceOf("requests")).toBe(W.answerAfterSetup("altair", "wants to run", "git push origin main"));
    });

    it.each([
      ["a signed-in person who may set up a browser", { enrollment: null }, "hold", W.NOTHING_WAITS_CARD, W.WAITS_SHOW_HERE_BROWSER],
      ["a signed-in person who may set up a browser or a phone", { enrollment: null, self: BOTH }, "hold", W.NOTHING_WAITS_CARD, W.WAITS_SHOW_HERE],
      ["a person whose account Straza has not read yet", { enrollment: null, self: null }, "hold", W.NOTHING_WAITS_CARD, W.WAITS_SHOW_HERE],
      ["a person whose account could not be read", { enrollment: null, self: "error" }, "hold", W.NOTHING_WAITS_CARD, W.WAITS_SHOW_HERE],
      ["a signed-out visitor", SIGNED_OUT, "hold", W.SIGN_IN_FOR_REQUESTS, W.SIGN_IN_FOR_REQUESTS_BODY],
      ["an account that approves from a phone only", { enrollment: null, self: PHONE_ONLY }, "phone", W.NOTHING_WAITS_CARD, W.WAITS_SHOW_HERE_PHONE],
      ["an account with no role to approve", { enrollment: null, self: NO_ROLE }, "hold", W.NOTHING_WAITS_CARD, W.NOT_ENROLLED_NO_ROLE],
    ] as [string, Partial<SelfState>, string, string, string][])("says what would be here before setup when nothing waits, for %s", (_what, over, tile, line, sentence) => {
      mount(over);
      expect(tileOf("requests")).toBe(tile);
      expect(lineOf("requests")).toBe(line);
      expect(sentenceOf("requests")).toBe(sentence);
      expect(buttonsOf("requests")).toEqual([]);
    });

    it("says before setup to answer on a device, for a person who approves from another one", async () => {
      vi.mocked(selfDevices).mockResolvedValue({ devices: [iphone] });
      const { unmount } = mount({ enrollment: null });
      await waitFor(() => expect(sentenceOf("requests")).toBe(W.WAITS_SHOW_HERE_DEVICE));
      expect(lineOf("requests")).toBe(W.NOTHING_WAITS_CARD);
      expect(tileOf("requests")).toBe("hold");
      unmount();
      mount({ enrollment: null, waiting: [push, sum] });
      await waitFor(() => expect(sentenceOf("requests")).toBe(W.answerOnDevice("altair", "wants to run", "git push origin main")));
      expect(lineOf("requests")).toBe(W.waitsForYou(2));
      expect(buttonsOf("requests")).toEqual([]);
    });

    it("says before setup to answer on the phone, or to add one, for an account that approves from a phone only", async () => {
      vi.mocked(selfDevices).mockResolvedValue({ devices: [iphone] });
      const { unmount } = mount({ enrollment: null, self: PHONE_ONLY, waiting: [push, sum] });
      await waitFor(() => expect(sentenceOf("requests")).toBe(W.answerOnPhone("altair", "wants to run", "git push origin main")));
      unmount();
      vi.mocked(selfDevices).mockResolvedValue({ devices: [] });
      mount({ enrollment: null, self: PHONE_ONLY, waiting: [push, sum] });
      await waitFor(() => expect(sentenceOf("requests")).toBe(W.answerAfterPhone("altair", "wants to run", "git push origin main")));
      expect(buttonsOf("requests")).toEqual([]);
    });

    it.each([
      ["while the devices are read", () => vi.mocked(selfDevices).mockReturnValue(new Promise(() => undefined)), BROWSER_ONLY, W.WAITS_SHOW_HERE_BROWSER],
      ["when their read failed", () => vi.mocked(selfDevices).mockRejectedValue(new ApiError("unreachable", 0, true)), BROWSER_ONLY, W.WAITS_SHOW_HERE_BROWSER],
      ["when the list is empty", () => vi.mocked(selfDevices).mockResolvedValue({ devices: [] }), BROWSER_ONLY, W.WAITS_SHOW_HERE_BROWSER],
      ["for an account that approves from a phone only", () => vi.mocked(selfDevices).mockResolvedValue({ devices: [iphone] }), PHONE_ONLY, W.WAITS_SHOW_HERE_PHONE],
    ] as [string, () => void, SelfAnswer, string][])("keeps its words before setup %s", async (_what, arrange, self, sentence) => {
      arrange();
      mount({ enrollment: null, self });
      await waitFor(() => expect(vi.mocked(selfDevices)).toHaveBeenCalledTimes(1));
      await new Promise((r) => setTimeout(r, 20));
      expect(sentenceOf("requests")).toBe(sentence);
    });

    it.each([
      ["this browser is set up", {}],
      ["the person is signed in, not set up", { enrollment: null }],
      ["the account approves from a phone only", { enrollment: null, self: PHONE_ONLY }],
      ["the account has no role to approve", { enrollment: null, self: NO_ROLE }],
    ] as [string, Partial<SelfState>][])("says the requests could not be read, under the tile of a request, when %s", (_what, over) => {
      mount({ ...over, waiting: "error" });
      expect(lineOf("requests")).toBe(W.REQUESTS_UNREAD_CARD);
      expect(sentenceOf("requests")).toBeNull();
      expect(tileOf("requests")).toBe("hold");
      expect(buttonsOf("requests")).toEqual([]);
    });
  });

  describe("the credentials card", () => {
    it("asks a signed-out visitor to sign in", async () => {
      mount(SIGNED_OUT);
      expect(lineOf("credentials")).toBe(W.CREDENTIALS_SIGN_IN);
      expect(sentenceOf("credentials")).toBeNull();
      await userEvent.click(within(card("credentials")!).getByRole("button", { name: W.SIGN_IN }));
      expect(openSignIn).toHaveBeenCalledTimes(1);
    });

    it.each([
      ["a token the person has not set", [server("github", { kind: "token" })], W.noTokenFromYou("github")],
      ["a sign-in the person has not made", [server("midpoint", { kind: "oauth", provider: "keycloak" })], W.noSignInFromYou("midpoint")],
      ["the person's expired token", [server("github", { connected: true, expires_at: at(-3600) })], W.tokenExpired("github", "")],
      ["an agent's expired token", [server("github", { who: "altair", agents: "own", connected: true, expires_at: at(-3600) })], W.tokenExpired("github", "altair")],
      ["an agent's missing token", [server("github", { who: "altair", agents: "own" })], W.agentNoToken("altair", "github")],
    ] as [string, CredentialRow[], string][])("names %s, with Open Credentials", async (_what, rows, sentence) => {
      mount({ servers: rows });
      expect(tileOf("credentials")).toBe("warn");
      expect(lineOf("credentials")).toBe(W.needsCredential(1));
      expect(sentenceOf("credentials")).toBe(sentence);
      await userEvent.click(within(card("credentials")!).getByRole("button", { name: W.OPEN_CREDENTIALS }));
      expect(window.location.pathname).toBe("/self-service/credentials");
    });

    it("counts every row that waits and names the first in the table's order", () => {
      mount({ servers: [server("zebra", {}), server("alpha", { who: "altair", agents: "own" }), server("demo-tools", { kind: "none" })] });
      expect(lineOf("credentials")).toBe(W.needsCredential(2));
      expect(sentenceOf("credentials")).toBe(W.agentNoToken("altair", "alpha"));
    });

    it.each([
      ["no row waits", [server("demo-tools", { kind: "none" }), server("github", { connected: true })]],
      ["there is no row", []],
    ] as [string, CredentialRow[]][])("says no server needs a credential when %s", (_what, rows) => {
      mount({ servers: rows });
      expect(tileOf("credentials")).toBe("credential");
      expect(lineOf("credentials")).toBe(W.CREDENTIALS_SET);
      expect(buttonsOf("credentials")).toEqual([]);
    });

    it("says the read failed, with Try again", async () => {
      mount({ servers: "error" });
      expect(lineOf("credentials")).toBe(W.CREDENTIALS_UNREAD);
      await userEvent.click(within(card("credentials")!).getByRole("button", { name: W.TRY_AGAIN }));
      expect(readServers).toHaveBeenCalledTimes(1);
    });

    it("says it is reading while the shell's read is in flight", () => {
      mount({ servers: null });
      expect(lineOf("credentials")).toBe(READING);
    });
  });

  describe("the devices card", () => {
    it("names the devices, this browser first, and which are not notified, with Manage", async () => {
      vi.mocked(selfDevices).mockResolvedValue({ devices: [iphone, here] });
      mount();
      await waitFor(() => expect(lineOf("devices")).toBe(W.approveFrom(2)));
      expect(tileOf("devices")).toBe("phone");
      expect(sentenceOf("devices")).toBe(W.devicesLine([W.THIS_BROWSER_NAME, "iPhone"], [W.THIS_BROWSER_NAME]));
      await userEvent.click(within(card("devices")!).getByRole("button", { name: W.MANAGE }));
      expect(window.location.pathname).toBe("/self-service/devices");
    });

    it("draws a browser tile when this browser is the only device", async () => {
      mount();
      await waitFor(() => expect(lineOf("devices")).toBe(W.approveFrom(1)));
      expect(tileOf("devices")).toBe("browser");
      expect(sentenceOf("devices")).toBe(W.devicesLine([W.THIS_BROWSER_NAME], [W.THIS_BROWSER_NAME]));
    });

    it("says no device yet when the list is read and empty", async () => {
      vi.mocked(selfDevices).mockResolvedValue({ devices: [] });
      mount({ enrollment: null });
      await waitFor(() => expect(lineOf("devices")).toBe(W.NO_DEVICE_YET));
      expect(sentenceOf("devices")).toBeNull();
      expect(buttonsOf("devices")).toEqual([]);
    });

    it.each([
      ["an older server", () => vi.mocked(selfDevices).mockRejectedValue(new ApiError("HTTP 404", 404))],
      ["a failed read", () => vi.mocked(selfDevices).mockRejectedValue(new ApiError("unreachable", 0, true))],
    ])("speaks of this browser alone when the list is not read, from %s", async (_what, arrange) => {
      arrange();
      mount({ enrollment: { ...enrollment, push: { kind: "webpush", token_or_endpoint: "e", p256dh: "p", auth: "a" } } });
      await waitFor(() => expect(vi.mocked(selfDevices)).toHaveBeenCalledTimes(1));
      expect(lineOf("devices")).toBe(W.APPROVE_FROM_HERE);
      expect(sentenceOf("devices")).toBe(W.hereLine("Firefox on Windows", "on"));
      expect(buttonsOf("devices")).toEqual([W.MANAGE]);
    });

    it("says notifications are off, or not offered, on this browser alone", () => {
      const { unmount } = mount({ session: { kind: "signed-out" }, self: null });
      expect(sentenceOf("devices")).toBe(W.hereLine("Firefox on Windows", "off"));
      unmount();
      vi.mocked(support).mockReturnValue({ available: false, why: W.PUSH_OFF_DEPLOYMENT });
      mount({ session: { kind: "signed-out" }, self: null });
      expect(sentenceOf("devices")).toBe(W.hereLine("Firefox on Windows", "none"));
    });

    it("says this browser is not set up to a signed-out visitor", () => {
      mount(SIGNED_OUT);
      expect(lineOf("devices")).toBe(W.NOT_SET_UP);
      expect(sentenceOf("devices")).toBeNull();
      expect(vi.mocked(selfDevices)).not.toHaveBeenCalled();
    });

    it("offers Add a phone to an account that approves from a phone only and has none", async () => {
      vi.mocked(selfDevices).mockResolvedValue({ devices: [] });
      mount({ enrollment: null, self: PHONE_ONLY });
      await waitFor(() => expect(lineOf("devices")).toBe(W.NO_PHONE_YET));
      expect(tileOf("devices")).toBe("phone");
      expect(sentenceOf("devices")).toBe(W.PHONE_APPROVES);
      await userEvent.click(within(card("devices")!).getByRole("button", { name: W.ADD_PHONE }));
      expect(setSheet).toHaveBeenCalledWith("phone", true);
    });

    it("says how many devices are set up, not that it approves, for an account with no role to approve", async () => {
      vi.mocked(selfDevices).mockResolvedValue({ devices: [iphone] });
      mount({ enrollment: null, self: NO_ROLE });
      await waitFor(() => expect(lineOf("devices")).toBe(W.devicesSetUp(1)));
      expect(sentenceOf("devices")).toBe(W.devicesLine(["iPhone"], []));
      expect(buttonsOf("devices")).toEqual([W.MANAGE]);
    });

    it("is not drawn for an account with no role to approve", async () => {
      vi.mocked(selfDevices).mockResolvedValue({ devices: [] });
      mount({ enrollment: null, self: NO_ROLE });
      await waitFor(() => expect(vi.mocked(selfDevices)).toHaveBeenCalledTimes(1));
      expect(card("devices")).toBeNull();
      expect(card("requests")).not.toBeNull();
    });
  });

  describe("the queue under the cards", () => {
    it.each([
      ["this browser is set up, signed out", { session: { kind: "signed-out" }, self: null }, "home"],
      ["the person is signed in, not set up", { enrollment: null }, "home"],
    ] as [string, Partial<SelfState>, string][])("shows the home view when %s", (_what, over, queue) => {
      mount(over);
      expect(document.querySelector("[data-home-waiting]")!.getAttribute("data-queue")).toBe(queue);
    });

    it("shows nothing when neither is true, since the first card says why", () => {
      mount(SIGNED_OUT);
      expect(document.querySelector("[data-home-waiting]")).toBeNull();
    });
  });
});
