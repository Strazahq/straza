import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UserDevices } from "./user-devices";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type DeviceRow, type UserDetail, keyPosture, listDevices } from "@/lib/api";
import { NO_DEVICE, NO_KEY_LINE, SUBJECT_DEVICES } from "@/lib/user-words";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), listDevices: vi.fn(), keyPosture: vi.fn() }));
vi.mock("@/components/entra-link", () => ({ EntraLink: ({ user }: { user: UserDetail }) => <div data-entra={user.username} /> }));

const counts = { sessions: 0, active_sessions: 0, devices: 1, approver_devices: 2, approvals: 0 };
const mara: UserDetail = { id: "u-mara", username: "mara", status: "active", origin: "scim", kind: "human", created_at: "", updated_at: "", effective_roles: [], locks: [], sponsored_count: 0, counts };
const agent: UserDetail = { ...mara, id: "u-nina", username: "nina", kind: "nhi", user_type: "agent", counts: { ...counts, approver_devices: 0 } };
const laptop: DeviceRow = { id: "d1", user_id: "u-mara", name: "mara-laptop", fingerprint: "f", platform: "macos", status: "active", enrolled_at: "2026-08-14T09:00:00Z" };
const nameless: DeviceRow = { ...laptop, id: "0123456789", name: "", platform: "" };

function draw(user: UserDetail) {
  const props = { onRevokeDevice: vi.fn(), onRevokeKey: vi.fn(), onChanged: vi.fn() };
  render(<TooltipProvider><UserDevices user={user} nonce={0} {...props} /></TooltipProvider>);
  return props;
}

const fact = (label: string) => (document.querySelector("[data-fact='" + label + "']") as HTMLElement | null)?.querySelector("dd") as HTMLElement;

describe("the Devices and keys tab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(listDevices).mockResolvedValue([laptop, nameless]);
    vi.mocked(keyPosture).mockResolvedValue({ user_id: "u-nina", registered: false });
  });

  it("lists a person's devices with what a device is, and the approver phones", async () => {
    const { onRevokeDevice } = draw(mara);
    await waitFor(() => expect(document.querySelector("[data-device=d1]")).not.toBeNull());
    expect((document.querySelector("[data-device=d1]") as HTMLElement).textContent).toBe("mara-laptopmacosenrolled 14 Aug 2026activeRevoke");
    expect((document.querySelector("[data-device='0123456789']") as HTMLElement).textContent).toContain("01234567unknown");
    expect(screen.getByRole("button", { name: "Help: Devices" }).getAttribute("data-help")).toBe("Devices");
    expect(fact("Approver phones").textContent).toBe("2 phones");
    expect(document.querySelector("[data-fact='Assertion key']")).toBeNull();
    expect(document.querySelector("[data-entra]")).toBeNull();
    expect(keyPosture).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Revoke mara-laptop" }));
    expect(onRevokeDevice).toHaveBeenCalledWith(laptop, "mara-laptop");
  });

  it("says no device is enrolled, and leaves the phones out at zero", async () => {
    vi.mocked(listDevices).mockResolvedValue([]);
    draw(agent);
    expect(await screen.findByText(NO_DEVICE)).toBeTruthy();
    expect(document.querySelector("[data-fact='Approver phones']")).toBeNull();
  });

  it("shows an agent's assertion key and its Entra agent identity", async () => {
    draw(agent);
    await waitFor(() => expect(fact("Assertion key").textContent).toContain(NO_KEY_LINE));
    expect(within(fact("Assertion key")).getByRole("button", { name: "Register key" })).toBeTruthy();
    expect(keyPosture).toHaveBeenCalledWith("u-nina");
    expect(document.querySelector("[data-entra=nina]")).not.toBeNull();
  });

  it("keeps the key when the device list fails", async () => {
    vi.mocked(listDevices).mockRejectedValue(Object.assign(new Error("down"), { status: 0, unreachable: true }));
    draw(agent);
    await waitFor(() => expect(fact("Devices").textContent).toContain(SUBJECT_DEVICES + " could not be read because Straza did not answer."));
    await waitFor(() => expect(fact("Assertion key").textContent).toContain(NO_KEY_LINE));
  });
});
