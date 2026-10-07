import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RolePacks } from "./role-packs";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type PackRow, type RoleRow, bindPack, unbindPack } from "@/lib/api";
import { notify } from "@/lib/notify";
import { BIND_PACK, NO_PACK_BOUND, PACK_MISSING, PACK_PICK, UNBIND, UNBIND_PACK, boundToast, packVersion, unbindBody, unbindTitle, unboundToast } from "@/lib/role-words";

const role: RoleRow = { id: "r-dev-tools", name: "dev-tools", kind: "application" };
const house: PackRow = { id: "p1", name: "house-rules", version: "3", bindings: [{ id: "pb1", role_id: "r-dev-tools" }] };
const style: PackRow = { id: "p2", name: "style-guide", version: "1", bindings: [] };

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), bindPack: vi.fn(), unbindPack: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const onChanged = vi.fn();
const mount = (packs: PackRow[]) => render(<TooltipProvider><RolePacks role={role} packs={packs} onChanged={onChanged} /></TooltipProvider>);

describe("the Packs tab", () => {
  beforeEach(() => {
    vi.mocked(bindPack).mockResolvedValue({});
    vi.mocked(unbindPack).mockResolvedValue({ status: "removed" });
    onChanged.mockClear();
    vi.mocked(notify.ok).mockClear();
  });

  it("lists the packs bound to this role with their version", () => {
    mount([house, style]);
    expect(screen.getByText("house-rules")).toBeTruthy();
    expect(screen.getByText(packVersion("3"))).toBeTruthy();
    expect(screen.queryByText(NO_PACK_BOUND)).toBeNull();
  });

  it("says so when no pack is bound", () => {
    mount([style]);
    expect(screen.getByText(NO_PACK_BOUND)).toBeTruthy();
  });

  it("binds the pack that was picked, and offers only unbound ones", async () => {
    mount([house, style]);
    await userEvent.click(screen.getByRole("combobox", { name: PACK_PICK }));
    expect((await screen.findAllByRole("option")).map((o) => o.textContent)).toEqual(["style-guide"]);
    await userEvent.click(screen.getByRole("option", { name: "style-guide" }));
    await userEvent.click(screen.getByRole("button", { name: BIND_PACK }));
    await waitFor(() => expect(bindPack).toHaveBeenCalledWith("p2", "r-dev-tools"));
    expect(notify.ok).toHaveBeenCalledWith(boundToast("dev-tools", "style-guide"));
    expect(onChanged).toHaveBeenCalled();
  });

  it("names the missing pick instead of binding nothing", async () => {
    mount([house, style]);
    await userEvent.click(screen.getByRole("button", { name: BIND_PACK }));
    expect(await screen.findByText(PACK_MISSING)).toBeTruthy();
    expect(bindPack).not.toHaveBeenCalled();
  });

  it("restates what stops arriving before it unbinds a pack", async () => {
    mount([house, style]);
    await userEvent.click(screen.getByRole("button", { name: UNBIND }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(unbindTitle("house-rules"))).toBeTruthy();
    expect(dialog.textContent).toContain(unbindBody("dev-tools", "house-rules"));
    await userEvent.click(within(dialog).getByRole("button", { name: UNBIND_PACK }));
    await waitFor(() => expect(unbindPack).toHaveBeenCalledWith("p1", "r-dev-tools"));
    expect(notify.ok).toHaveBeenCalledWith(unboundToast("dev-tools", "house-rules"));
  });
});
