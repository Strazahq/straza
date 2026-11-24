import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SessionRevoke } from "./session-revoke";
import { ApiError, revokeSession } from "@/lib/api";
import { notify } from "@/lib/notify";
import { OWN_SESSION_WARNING, revokePageBody } from "@/lib/session-words";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), revokeSession: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const ID = "01a1da3d-934c-7be8-bd1a-92e51c045044";

const draw = (own = false) => {
  const props = { onOpenChange: vi.fn(), onRevoked: vi.fn() };
  render(<SessionRevoke id={ID} name="altair" own={own} open {...props} />);
  return props;
};

describe("the revoke confirm", () => {
  beforeEach(() => {
    vi.mocked(revokeSession).mockReset().mockResolvedValue({ status: "revoked" });
    vi.mocked(notify.ok).mockReset();
  });

  it("asks without a reason field and revokes on the red button", async () => {
    const { onOpenChange, onRevoked } = draw();
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByRole("heading").textContent).toBe("Revoke this session?");
    expect(dialog.textContent).toContain(revokePageBody("altair"));
    expect(dialog.textContent).not.toContain(OWN_SESSION_WARNING);
    expect(within(dialog).queryByRole("textbox")).toBeNull();
    const go = within(dialog).getByRole("button", { name: "Revoke session" });
    expect(go.className).toContain("bg-danger");
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeTruthy();
    await userEvent.click(go);
    expect(revokeSession).toHaveBeenCalledWith(ID);
    expect(notify.ok).toHaveBeenCalledWith("Session revoked.");
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(onRevoked).toHaveBeenCalledTimes(1);
  });

  it("warns when it is the console's own session", () => {
    draw(true);
    expect((document.querySelector("[data-own-warning]") as HTMLElement).textContent).toBe(OWN_SESSION_WARNING);
  });

  it("keeps a refusal inside the dialog", async () => {
    vi.mocked(revokeSession).mockRejectedValue(new ApiError("session is already revoked", 409));
    const { onOpenChange, onRevoked } = draw();
    await userEvent.click(screen.getByRole("button", { name: "Revoke session" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("Revoke session refused: session is already revoked. Fix what it names, then try again."));
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(onRevoked).not.toHaveBeenCalled();
    expect(notify.ok).not.toHaveBeenCalled();
  });
});
