import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { BotIcon, KeyRoundIcon, ServerCogIcon } from "lucide-react";
import { KIND_ICON, KindIcon, KindTile } from "./kind-icon";
import { KIND_WORD, type Kind } from "@/lib/chip-words";

// One icon per kind. A named icon carries the kind's word as its name; an
// icon beside its word stays hidden.

describe("KIND_ICON", () => {
  it("gives every kind of the words file an icon", () => {
    for (const kind of Object.keys(KIND_WORD) as Kind[]) expect(KIND_ICON[kind]).toBeTruthy();
  });

  it.each<[Kind, unknown]>([
    ["agent", BotIcon],
    ["role-server-admin", ServerCogIcon],
    ["api-token", KeyRoundIcon],
    ["credential", KeyRoundIcon],
  ])("maps %s to its decided icon", (kind, icon) => {
    expect(KIND_ICON[kind]).toBe(icon);
  });
});

describe("KindIcon", () => {
  it("hides the icon beside its own word", () => {
    render(<KindIcon kind="server" />);
    const svg = document.querySelector('[data-kind-icon="server"]') as SVGElement;
    expect(svg.getAttribute("aria-hidden")).toBe("true");
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("names the icon when it stands alone", () => {
    render(<KindIcon kind="agent" named />);
    const svg = screen.getByRole("img", { name: "AI agent" });
    expect(svg.getAttribute("data-kind-icon")).toBe("agent");
    expect(svg.querySelector("title")?.textContent).toBe("AI agent");
  });
});

describe("KindTile", () => {
  it("gives only the agent tile a hue", () => {
    const { container } = render(<><KindTile kind="agent" /><KindTile kind="person" size="lg" /></>);
    const [agent, person] = Array.from(container.children);
    expect(agent.classList).toContain("bg-teal-bg");
    expect(person.classList).toContain("bg-secondary");
    expect(person.classList).toContain("size-10");
  });

  it("draws the status overlay hidden from assistive technology", () => {
    const { container } = render(<KindTile kind="person" status="locked" named />);
    const tile = container.firstElementChild as HTMLElement;
    const overlay = tile.querySelector(":scope > span") as HTMLElement;
    expect(overlay.getAttribute("aria-hidden")).toBe("true");
    expect(overlay.querySelector("svg")?.getAttribute("class")).toContain("text-danger");
    expect(screen.getByRole("img", { name: "Person" })).toBeTruthy();
  });
});
