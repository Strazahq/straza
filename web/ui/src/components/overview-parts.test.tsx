import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Door, Panel, PanelNote, ToneBadge, oldestPending } from "./overview-parts";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { ApprovalRow } from "@/lib/api";
import { navigate, pathFor } from "@/lib/router";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const held = (id: string, createdAt: string) => ({ id, createdAt } as ApprovalRow);

describe("the Overview chrome", () => {
  it("makes a door a real link that navigates inside the app", async () => {
    render(<Door to="audit" />);
    const link = screen.getByRole("link", { name: /Audit/ });
    expect(link.getAttribute("href")).toBe(pathFor("audit"));
    await userEvent.click(link);
    expect(vi.mocked(navigate)).toHaveBeenCalledWith("audit", []);
  });

  it("carries a row's id as the hash, so the target opens on that row", () => {
    render(<Door to="settings" rest={["configuration"]} hash="floor" label="Set governance.minAttestation" arrow={false} />);
    expect(screen.getByRole("link", { name: "Set governance.minAttestation" }).getAttribute("href")).toBe(pathFor("settings", ["configuration"]) + "#floor");
  });

  it("gives every panel a title and the help icon that carries its sentence", () => {
    render(
      <TooltipProvider>
        <Panel name="posture" title="Posture" help="What the settings say."><PanelNote>Nothing yet.</PanelNote></Panel>
      </TooltipProvider>,
    );
    expect(screen.getByRole("heading", { name: /Posture/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Help: Posture" })).toBeTruthy();
    expect(screen.getByText("Nothing yet.")).toBeTruthy();
  });

  it("badges a word in its tone", () => {
    render(<ToneBadge tone="danger" word="denied" />);
    expect(screen.getByText("denied").getAttribute("data-tone")).toBe("danger");
  });

  it("finds the request that has waited longest", () => {
    const rows = [held("b", "2026-09-13T09:00:00Z"), held("a", "2026-09-13T08:00:00Z"), held("c", "2026-09-13T10:00:00Z")];
    expect(oldestPending(rows)?.id).toBe("a");
    expect(oldestPending([])).toBeNull();
  });
});
