import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DevicesTab } from "./approver-devices";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApproverDeviceRow, listApproverDevices, revokeApproverDevice } from "@/lib/api";
import { shortID } from "@/lib/approval-model";
import {
  BY_PUSH,
  DEVICES_EMPTY_BODY,
  DEVICES_EMPTY_TITLE,
  DEVICES_LEGEND,
  HARDWARE_KEY,
  MUST_CHECK,
  MUST_CHECK_LINE,
  REVOKE_DEVICE,
  SEEN_NEVER,
  SOFTWARE_BROWSER,
  SOFTWARE_KEY,
  SOFTWARE_PHONE,
  SUBJECT_DEVICES,
  attestedWords,
  chipWords,
  devicesTotal,
  groupDevices,
  hideDevices,
  matchWords,
  peopleWords,
  showDevices,
  shownWords,
  revokeBody,
  revokeTitle,
  revokedToast,
} from "@/lib/approval-words";
import { notify } from "@/lib/notify";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listApproverDevices: vi.fn(),
  revokeApproverDevice: vi.fn(),
  listUsers: vi.fn(),
  getUser: vi.fn(),
  mintEnrollToken: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const HOUR = 3600000;
const at = (ms: number) => new Date(Date.now() + ms).toISOString();

// The three devices are the eval shapes: alice's browser, her attested
// phone, and a phone whose owner the list answered by id alone.
const firefox: ApproverDeviceRow = {
  id: "apd_01a08b3d-de71-4c1b", user_id: "usr_01a0alice77f1", username: "alice",
  name: "Firefox on Windows", platform: "browser", key_security_level: "software",
  attestation: "none", enrolled_at: at(-96 * HOUR), last_seen: at(-3 * HOUR), push_routes: 0,
};
const pixel: ApproverDeviceRow = {
  id: "apd_01a09c4e-1f02-4d2a", user_id: "usr_01a0alice77f1", username: "alice",
  name: "Pixel 8", platform: "android", key_security_level: "strongbox",
  attestation: "play-integrity", enrolled_at: at(-72 * HOUR), last_seen: at(-2 * HOUR), push_routes: 1,
};
const iphone: ApproverDeviceRow = {
  id: "apd_01a0b71c-9a30-41ff", user_id: "usr_01a0bob4412c", username: "",
  name: "iPhone 15", platform: "ios", key_security_level: "software",
  attestation: "none", enrolled_at: at(-1 * HOUR), last_seen: null, push_routes: 0,
};
const rows = [firefox, pixel, iphone];

const mount = (addRequest = 0, onChanged = () => undefined) =>
  render(<TooltipProvider><DevicesTab addRequest={addRequest} onChanged={onChanged} /></TooltipProvider>);
const row = (name: string) => document.querySelector('[data-device="' + name + '"]') as HTMLElement;
const ask = () => screen.getByRole("alertdialog");

describe("the approver devices tab", () => {
  beforeEach(() => {
    vi.mocked(listApproverDevices).mockReset().mockResolvedValue(rows);
    vi.mocked(revokeApproverDevice).mockReset().mockResolvedValue({ status: "revoked" });
    vi.mocked(notify.ok).mockClear();
  });

  it("lists every device with its name, kind, identifier and person", async () => {
    mount();
    await waitFor(() => expect(row("Pixel 8")).toBeTruthy());
    expect(within(row("Pixel 8")).getByText("Pixel 8")).toBeTruthy();
    expect(row("Pixel 8").querySelector("[data-device-kind]")?.textContent).toBe("phone · android");
    expect(row("Firefox on Windows").querySelector("[data-device-kind]")?.textContent).toBe("browser");
    expect(row("iPhone 15").querySelector("[data-device-kind]")?.textContent).toBe("phone · ios");
    expect(within(row("Pixel 8")).getByText(shortID(pixel.id))).toBeTruthy();
    expect(within(row("Pixel 8")).getByText("alice")).toBeTruthy();
    // A row the server answered with no username still names its owner.
    expect(within(row("iPhone 15")).getByText(shortID(iphone.user_id))).toBeTruthy();
  });

  const keys: [string, string, string, string][] = [
    ["Pixel 8", HARDWARE_KEY, "text-ok", attestedWords("play-integrity")],
    ["iPhone 15", SOFTWARE_KEY, "text-warn", SOFTWARE_PHONE],
    ["Firefox on Windows", SOFTWARE_KEY, "", SOFTWARE_BROWSER],
  ];
  it.each(keys)("says where %s keeps its key, and what that means", async (name, word, tone, line) => {
    mount();
    await waitFor(() => expect(row(name)).toBeTruthy());
    const badge = row(name).querySelector("[data-key]") as HTMLElement;
    expect(badge.textContent).toBe(word);
    if (tone) expect(badge.className).toContain(tone);
    else {
      expect(badge.className).not.toContain("text-warn");
      expect(badge.className).not.toContain("text-ok");
    }
    expect(within(row(name)).getByText(line)).toBeTruthy();
  });

  it("marks the devices Straza cannot notify, and the one never seen", async () => {
    mount();
    await waitFor(() => expect(row("Pixel 8")).toBeTruthy());
    expect(row("Pixel 8").querySelector("[data-notified]")?.textContent).toBe(BY_PUSH);
    const must = row("Firefox on Windows").querySelector("[data-notified]") as HTMLElement;
    expect(must.textContent).toBe(MUST_CHECK);
    expect(must.className).toContain("text-warn");
    expect(within(row("Firefox on Windows")).getByText(MUST_CHECK_LINE)).toBeTruthy();
    expect(row("iPhone 15").querySelector("[data-last-seen]")?.textContent).toBe(SEEN_NEVER);
  });

  it("says what would be here when no device is enrolled, and keeps the legend either way", async () => {
    vi.mocked(listApproverDevices).mockResolvedValue([]);
    mount();
    expect(await screen.findByText(DEVICES_EMPTY_TITLE)).toBeTruthy();
    expect(screen.getByText(DEVICES_EMPTY_BODY)).toBeTruthy();
    expect(screen.getByText(DEVICES_LEGEND)).toBeTruthy();
  });

  it("says the legend beside a full table too", async () => {
    mount();
    await waitFor(() => expect(row("Pixel 8")).toBeTruthy());
    expect(screen.getByText(DEVICES_LEGEND)).toBeTruthy();
  });

  it("says what could not be read when the first read never arrives", async () => {
    vi.mocked(listApproverDevices).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount();
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain(SUBJECT_DEVICES + ": unreachable, state unknown.");
    expect(block.textContent).toContain("Check that it is running, then reload.");
  });

  it("keeps the rows on screen when a later read fails", async () => {
    mount();
    await waitFor(() => expect(row("Pixel 8")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Revoke Pixel 8" }));
    vi.mocked(listApproverDevices).mockRejectedValue(new ApiError("the database is unreachable", 500));
    await userEvent.click(within(ask()).getByRole("button", { name: REVOKE_DEVICE }));
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain(SUBJECT_DEVICES + " could not be read: the database is unreachable.");
    expect(row("Pixel 8")).toBeTruthy();
  });

  it("asks with the device and the person named, and writes nothing until the verb", async () => {
    mount();
    await waitFor(() => expect(row("Pixel 8")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Revoke Firefox on Windows" }));
    expect(within(ask()).getByText(revokeTitle("Firefox on Windows"))).toBeTruthy();
    expect(ask().textContent).toContain(revokeBody("alice"));
    expect(ask().getAttribute("data-revoke-device")).toBe(firefox.id);
    expect(revokeApproverDevice).not.toHaveBeenCalled();
    await userEvent.click(within(ask()).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBe(null));
    expect(revokeApproverDevice).not.toHaveBeenCalled();
  });

  it("revokes through the dialog, says so, reads the list again and tells the page", async () => {
    const onChanged = vi.fn();
    mount(0, onChanged);
    await waitFor(() => expect(row("Pixel 8")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Revoke Pixel 8" }));
    vi.mocked(listApproverDevices).mockResolvedValue([firefox, iphone]);
    await userEvent.click(within(ask()).getByRole("button", { name: REVOKE_DEVICE }));
    await waitFor(() => expect(revokeApproverDevice).toHaveBeenCalledWith(pixel.id));
    expect(notify.ok).toHaveBeenCalledWith(revokedToast("alice", "Pixel 8"));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBe(null));
    await waitFor(() => expect(row("Pixel 8")).toBe(null));
    expect(onChanged).toHaveBeenCalled();
  });

  it("quotes a refused revoke inside the dialog and keeps the button", async () => {
    vi.mocked(revokeApproverDevice).mockRejectedValue(new ApiError("no such approver device", 404));
    mount();
    await waitFor(() => expect(row("Pixel 8")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Revoke Pixel 8" }));
    await userEvent.click(within(ask()).getByRole("button", { name: REVOKE_DEVICE }));
    const alert = await within(ask()).findByRole("alert");
    expect(alert.textContent).toContain(REVOKE_DEVICE + " refused.");
    expect(alert.textContent).toContain("no such approver device");
    expect(alert.textContent).toContain("Fix what it names, then try again.");
    expect(within(ask()).getByRole("button", { name: REVOKE_DEVICE })).toBeTruthy();
    expect(notify.ok).not.toHaveBeenCalled();
  });

  it("opens the enrol sheet when the head's counter moves, and not on its first value", async () => {
    const view = mount(0);
    await waitFor(() => expect(row("Pixel 8")).toBeTruthy());
    expect(screen.queryByRole("dialog")).toBe(null);
    view.rerender(<TooltipProvider><DevicesTab addRequest={1} onChanged={() => undefined} /></TooltipProvider>);
    const sheet = await screen.findByRole("dialog");
    expect(sheet.getAttribute("data-enroll-sheet")).toBe("person");
  });

  // A fleet of sixty devices over six people: every third device has no
  // push route, every tenth phone sits on a software key, and the first
  // four have not been seen for forty days.
  const fleet = (): ApproverDeviceRow[] => Array.from({ length: 60 }, (_, i) => {
    const phone = i % 4 !== 0;
    return {
      id: "apd_" + String(i).padStart(4, "0"), user_id: "usr_" + (i % 6), username: ["alice", "bob", "carol", "dave", "erin", "frank"][i % 6],
      name: phone ? (i % 2 ? "Pixel 8" : "iPhone 15") : "Firefox on Windows", platform: phone ? (i % 2 ? "android" : "ios") : "browser",
      key_security_level: phone && i % 10 !== 5 ? "strongbox" : "software", attestation: phone && i % 10 !== 5 ? "play-integrity" : "none",
      enrolled_at: at(-(200 + i) * 24 * HOUR), last_seen: i < 4 ? at(-40 * 24 * HOUR) : at(-i * HOUR), push_routes: i % 3 === 0 ? 0 : 1,
    };
  });
  const chip = (f: string) => document.querySelector('[data-chip="' + f + '"]') as HTMLButtonElement;
  const count = () => (document.querySelector("[data-row-count]") as HTMLElement).textContent;

  it("counts the fleet on the strip and narrows the table by a chip", async () => {
    const rows = fleet();
    vi.mocked(listApproverDevices).mockResolvedValue(rows);
    mount();
    await waitFor(() => expect(document.querySelectorAll("[data-device]").length).toBe(50));
    expect((document.querySelector("[data-devices-total]") as HTMLElement).textContent).toBe(devicesTotal(60));
    expect(chip("phones").textContent).toBe(chipWords("phones", 45));
    expect(chip("browsers").textContent).toBe(chipWords("browsers", 15));
    expect(chipWords("browsers", 1)).toBe("1 browser");
    expect(chipWords("phones", 1)).toBe("1 phone");
    expect(chipWords("mustcheck", 1)).toBe("1 must check");
    expect(chip("mustcheck").textContent).toBe(chipWords("mustcheck", 20));
    expect(chip("stale").textContent).toBe(chipWords("stale", 4));
    expect(count()).toBe(matchWords(60, 60));
    await userEvent.click(chip("mustcheck"));
    expect(chip("mustcheck").getAttribute("aria-pressed")).toBe("true");
    expect(document.querySelectorAll("[data-device]").length).toBe(20);
    expect(count()).toBe(matchWords(20, 60));
    expect([...document.querySelectorAll("[data-notified]")].every((el) => el.textContent === MUST_CHECK)).toBe(true);
  });

  // Typing into a list of fifty ran out of vitest's 5 s under make ui-test's
  // full load and passes alone well inside it, so it carries its own budget.
  it("finds a person or a device as you type", async () => {
    vi.mocked(listApproverDevices).mockResolvedValue(fleet());
    mount();
    await waitFor(() => expect(document.querySelectorAll("[data-device]").length).toBe(50));
    await userEvent.type(screen.getByRole("textbox", { name: "Find a person or a device" }), "carol");
    expect(document.querySelectorAll("[data-device]").length).toBe(10);
    expect(count()).toBe(matchWords(10, 60));
    await userEvent.clear(screen.getByRole("textbox", { name: "Find a person or a device" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Find a person or a device" }), "firefox");
    expect(document.querySelectorAll("[data-device]").length).toBe(15);
  }, 30000);

  it("shows fifty at a time, newest contact first, with Load more", async () => {
    vi.mocked(listApproverDevices).mockResolvedValue(fleet());
    mount();
    await waitFor(() => expect(document.querySelectorAll("[data-device]").length).toBe(50));
    expect((document.querySelector("[data-shown]") as HTMLElement).textContent).toBe(shownWords(50, 60));
    // The four stale devices were seen forty days ago, so they read last;
    // the fifth device, a browser seen four hours ago, reads first.
    const names = [...document.querySelectorAll("[data-device]")].map((el) => el.getAttribute("data-device"));
    expect(names[0]).toBe("Firefox on Windows");
    expect(names.slice(-4).every((n) => n === "Firefox on Windows" || n === "Pixel 8" || n === "iPhone 15")).toBe(true);
    await userEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(document.querySelectorAll("[data-device]").length).toBe(60);
    expect(screen.queryByRole("button", { name: "Load more" })).toBe(null);
  });

  it("groups the fleet by person, folded, with how many to look at, and opens one", async () => {
    vi.mocked(listApproverDevices).mockResolvedValue(fleet());
    mount();
    await waitFor(() => expect(document.querySelectorAll("[data-device]").length).toBe(50));
    await userEvent.click(screen.getByRole("switch", { name: "Group by person" }));
    expect(count()).toBe(peopleWords(6, 60));
    expect(document.querySelectorAll("[data-device]").length).toBe(0);
    const alice = screen.getByRole("button", { name: showDevices("alice") });
    // Every sixth device is alice's, and every one of those is a multiple of
    // three, so all ten lack a push route.
    expect(alice.textContent).toContain(groupDevices(10, 10));
    await userEvent.click(alice);
    expect(screen.getByRole("button", { name: hideDevices("alice") })).toBeTruthy();
    expect(document.querySelectorAll("[data-device]").length).toBe(10);
  });
});
