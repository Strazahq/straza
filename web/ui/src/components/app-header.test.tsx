import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AppHeader } from "./app-header";
import { TooltipProvider } from "@/components/ui/tooltip";
import { navigate } from "@/lib/router";
import { setTheme } from "@/lib/theme";
import { setTimeZone, useTimeZone } from "@/lib/timezone";
import { absTime } from "@/lib/words";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

// Stamp renders one absolute time the way a screen does, subscribed to the
// zone so the header's toggle repaints it.
function Stamp() {
  useTimeZone();
  return <span data-stamp>{absTime("2026-09-10T10:00:00Z")}</span>;
}

function stubReadyz(answer: () => Response | Promise<Response>) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    if (String(input) === "/readyz") return answer();
    return new Response("{}", { status: 404 });
  });
}

const json = (body: unknown, status: number) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

function mount() {
  return render(
    <TooltipProvider>
      <AppHeader user="alice" onOpenPalette={() => {}} onSignOut={() => {}} />
      <Stamp />
    </TooltipProvider>,
  );
}

describe("the app header", () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    stubReadyz(() => json({ status: "ok", components: {} }, 200));
  });
  afterEach(() => {
    vi.restoreAllMocks();
    setTimeZone("utc");
    setTheme("system");
  });

  it.each([
    ["Healthy", () => json({ status: "ok", components: { db: "ok" } }, 200), "ok"],
    ["Degraded", () => json({ status: "degraded", components: { db: "dial tcp: refused" } }, 503), "degraded"],
    ["Unknown", () => Promise.reject(new TypeError("Failed to fetch")), "unreachable"],
  ])("reads %s from the readiness probe", async (word, answer, state) => {
    stubReadyz(answer);
    mount();
    const el = await screen.findByText(word);
    expect(el.closest("[data-health]")?.getAttribute("data-health")).toBe(state);
  });

  it("flips every absolute stamp between the UTC suffix and the local offset", async () => {
    mount();
    expect(screen.getByText("2026-09-10 10:00:00 UTC")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "UTC" }));
    expect(screen.getByRole("button", { name: "local" })).toBeTruthy();
    const stamp = document.querySelector("[data-stamp]")?.textContent || "";
    expect(stamp).toMatch(/ [+-]\d{2}:\d{2}$/);
    await userEvent.click(screen.getByRole("button", { name: "local" }));
    expect(screen.getByText("2026-09-10 10:00:00 UTC")).toBeTruthy();
  });

  it("sets the theme from the menu with a check on the current choice", async () => {
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Theme: System" }));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: "Dark" }));
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(screen.getByRole("button", { name: "Theme: Dark" })).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Theme: Dark" }));
    expect((await screen.findByRole("menuitemradio", { name: "Dark" })).getAttribute("aria-checked")).toBe("true");
  });

  it("opens the palette from the header button", async () => {
    const onOpenPalette = vi.fn();
    render(
      <TooltipProvider>
        <AppHeader user="alice" onOpenPalette={onOpenPalette} onSignOut={() => {}} />
      </TooltipProvider>,
    );
    await userEvent.click(screen.getByRole("button", { name: /Find a page, server, user, role or policy/ }));
    expect(onOpenPalette).toHaveBeenCalledTimes(1);
  });
});

describe("the door to the self-service page", () => {
  it("keeps self-service available in mobile navigation", async () => {
    render(
      <TooltipProvider>
        <AppHeader user="alice" onOpenPalette={() => {}} onSignOut={() => {}} />
      </TooltipProvider>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Open navigation" }));
    const door = screen.getByRole("menuitem", { name: /Self-service/ });
    expect(door.getAttribute("href")).toBe("/self-service/");
  });
});

describe("the header's link to your draft (mock steer CD-14)", () => {
  const header = (yours: { id: string; changes: number } | null) => render(
    <TooltipProvider>
      <AppHeader user="alice" yours={yours} onOpenPalette={() => {}} onSignOut={() => {}} />
    </TooltipProvider>,
  );

  it("names the open working draft by its changes and opens it", async () => {
    header({ id: "39", changes: 1 });
    const link = screen.getByRole("button", { name: "Your draft · 1 change" });
    expect(link.getAttribute("title")).toBe("Your open draft");
    await userEvent.click(link);
    expect(navigate).toHaveBeenCalledWith("drafts", ["39"]);
    document.body.innerHTML = "";
    header({ id: "39", changes: 3 });
    expect(screen.getByRole("button", { name: "Your draft · 3 changes" })).toBeTruthy();
  });

  it("shows nothing while no working draft is open", () => {
    header(null);
    expect(screen.queryByRole("button", { name: /Your draft/ })).toBeNull();
  });
});
