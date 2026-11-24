import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { HealthCell, KindPills, WaitingCell, WhereCell, sessionColumns } from "./session-list-parts";
import type { SessionRow } from "@/lib/api";

const row = (id: string, user: string, kind: string | undefined, status = "active"): SessionRow =>
  ({ id, user_id: user, harness: "claude-code/2.2.0", attestation: "managed", status, started_at: "", last_seen: "", user_kind: kind });

describe("the Waiting cell", () => {
  it("draws a warn chip named by its count", () => {
    render(<WaitingCell n={3} />);
    const cell = screen.getByRole("img", { name: "3 waiting" });
    expect(cell.textContent).toBe("3");
    expect(cell.querySelector("[data-tone]")?.getAttribute("title")).toBe("3 waiting");
  });

  it("draws nothing at zero", () => {
    const { container } = render(<WaitingCell n={0} />);
    expect(container.textContent).toBe("");
  });
});

describe("the kind pills", () => {
  it("counts each user with an active session once, by kind, and leaves out a kind at zero", () => {
    render(<KindPills rows={[row("s1", "u1", "person"), row("s2", "u1", "person"), row("s3", "u2", "agent"), row("s4", "u3", "agent"), row("s5", "u4", "service", "closed"), row("s6", "u5", undefined)]} />);
    const pills = [...document.querySelectorAll("[data-kind-pill]")].map((p) => p.getAttribute("data-kind-pill") + ": " + p.textContent);
    expect(pills).toEqual(["person: 1 person", "agent: 2 agents"]);
  });

  it("draws nothing when no row sends a kind", () => {
    const { container } = render(<KindPills rows={[row("s1", "u1", undefined)]} />);
    expect(container.innerHTML).toBe("");
  });
});

describe("the Where cell", () => {
  const host = () => document.querySelector("[data-where-host]") as HTMLElement | null;
  const folder = () => document.querySelector("[data-where-folder]") as HTMLElement | null;

  it("stacks the machine in mono over the folder in muted caption, each cut with its whole value on hover", () => {
    render(<WhereCell host="dev-laptop" folder="~/projects/game-audio" own={false} />);
    expect(document.querySelector("[data-where]")?.textContent).toBe("dev-laptop~/projects/game-audio");
    expect(host()?.textContent).toBe("dev-laptop");
    expect(host()?.getAttribute("title")).toBe("dev-laptop");
    expect(host()?.className.split(" ")).toEqual(expect.arrayContaining(["truncate", "font-mono", "text-foreground"]));
    expect(folder()?.textContent).toBe("~/projects/game-audio");
    expect(folder()?.getAttribute("title")).toBe("~/projects/game-audio");
    expect(folder()?.className.split(" ")).toEqual(expect.arrayContaining(["truncate", "text-caption", "text-muted-foreground"]));
  });

  it("cuts the folder at its start with the cell's own CSS and keeps its text whole and in order", () => {
    render(<WhereCell folder="~/projects/a/very/long/path/to/game-audio" own={false} />);
    const line = folder() as HTMLElement;
    expect(line.className.split(" ")).toEqual(expect.arrayContaining(["[direction:rtl]", "text-left"]));
    const inner = line.firstElementChild as HTMLElement;
    expect(inner.getAttribute("dir")).toBe("ltr");
    expect(inner.textContent).toBe("~/projects/a/very/long/path/to/game-audio");
    expect(line.childNodes.length).toBe(1);
  });

  it.each([
    ["the machine alone", "dev-laptop", undefined, "dev-laptop"],
    ["the folder alone", undefined, "~/x", "~/x"],
  ])("draws one line for %s", (_case, h, f, words) => {
    render(<WhereCell host={h} folder={f} own={false} />);
    expect(document.querySelector("[data-where]")?.textContent).toBe(words);
    expect(!!host()).toBe(!!h);
    expect(!!folder()).toBe(!!f);
  });

  it("draws nothing for a session with neither", () => {
    const { container } = render(<WhereCell own={false} />);
    expect(container.innerHTML).toBe("");
  });

  it("says your browser in muted words for the console's own session", () => {
    render(<WhereCell own />);
    const words = document.querySelector("[data-where-browser]") as HTMLElement;
    expect(words.textContent).toBe("your browser");
    expect(words.className).toContain("text-muted-foreground");
    expect(document.querySelector("[data-where]")).toBeNull();
  });
});

describe("the Health cell", () => {
  it("lets a long word wrap beside its icon instead of cutting it", () => {
    render(<HealthCell health={{ level: "danger", word: "Hook configuration not registered", title: "t" }} />);
    const word = document.querySelector("[data-health] > span") as HTMLElement;
    expect(word.textContent).toBe("Hook configuration not registered");
    expect(word.className).toContain("whitespace-normal");
    expect(word.className).not.toContain("truncate");
  });
});

describe("the session columns", () => {
  it("puts Where and then Waiting after Harness and leads with the user only when not grouped", () => {
    expect(sessionColumns(true, null).map((c) => c.id)).toEqual(["session", "harness", "where", "waiting", "health", "last_seen", "status", "client", "attestation", "wiring", "started"]);
    expect(sessionColumns(false, null)[0].id).toBe("user");
  });
});
