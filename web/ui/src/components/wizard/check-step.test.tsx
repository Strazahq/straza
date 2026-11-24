import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CheckStep, agentSnippet } from "./check-step";
import type { Row } from "./parts";
import { take } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { healthRead } from "@/lib/words";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
// The session's areas: every area by default, and a narrower set in the
// cases that prove a tile leads only where the session may go.
const sess = vi.hoisted(() => ({ areas: null as Record<string, true> | null }));
vi.mock("@/lib/session", () => ({ adminAreas: () => sess.areas, adminServers: () => 0 }));

const URL_ = "http://demo-tools:3001/mcp";
const DONE: Row[] = [
  { key: "install", label: "Publish scout-tools", state: "done" },
  { key: "secret", label: "Store its secret", state: "done" },
  { key: "check", label: "Check scout-tools", state: "done" },
];
const running = (tools: string[]) => healthRead({ status: "running", tools }, URL_);
const onOpen = vi.fn();
const mount = (rows: Row[], read: ReturnType<typeof healthRead> | null, tools: string[] = ["echo", "get-sum"]) =>
  render(<CheckStep rows={rows} read={read} name="scout-tools" tools={tools} problem={null} onOpen={onOpen} />);
const tiles = () => [...document.querySelectorAll("[data-next-tile]")].map((t) => t.getAttribute("data-next-tile"));

describe("the Check step of Add MCP server", () => {
  beforeEach(() => {
    sess.areas = null;
    vi.mocked(navigate).mockReset();
    onOpen.mockReset();
  });

  it("ends on What next for a server that runs and lists a tool, with the three doors", async () => {
    mount(DONE, running(["echo", "get-sum"]));
    const live = document.querySelector("[data-live]") as HTMLElement;
    expect(live.textContent).toBe("scout-tools is live. Straza reached it and read 2 tools.");
    expect(live.querySelector("svg.lucide-circle-check")).toBeTruthy();
    expect(screen.getByRole("heading", { level: 3, name: "What next" }).className).toContain("text-section");
    expect(document.querySelector("[data-landed]")).toBeNull();
    expect(document.querySelector("[data-health]")).toBeNull();
    expect(document.querySelector("[data-nobody-reaches]")).toBeNull();
    expect(tiles()).toEqual(["roles", "policies", "servers"]);
    const role = document.querySelector('[data-next-tile="roles"]') as HTMLElement;
    expect(role.querySelector('[data-kind-icon="role-application"]')).toBeTruthy();
    expect(role.textContent).toBe("Give access to itMake an application role that reaches some or all of its 2 tools.");
    expect(document.querySelector('[data-next-tile="policies"] [data-kind-icon="policy-set"]')).toBeTruthy();
    expect(document.querySelector('[data-next-tile="servers"] [data-kind-icon="server"]')).toBeTruthy();
    expect(screen.getByRole("button", { name: "As a strazactl command" })).toBeTruthy();

    await userEvent.click(role);
    expect(navigate).toHaveBeenCalledWith("roles", ["new"]);
    expect(take("roles-new")).toEqual({ server: "scout-tools" });
    await userEvent.click(within(document.querySelector("[data-what-next]") as HTMLElement).getByRole("button", { name: /^Write a policy for it/ }));
    expect(navigate).toHaveBeenCalledWith("policies", ["new"]);
    expect(take("policies-new")).toEqual({ lane: "mcp", app: "scout-tools" });
    await userEvent.click(screen.getByRole("button", { name: /^Open scout-tools/ }));
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  // Each case is a landed run that shows the check from the agent's
  // machine: What next, and a server that runs without its secret stored.
  const snippets: [string, Row[]][] = [
    ["What next", DONE],
    ["a run with a row that did not land", DONE.map((r) => (r.key === "secret" ? { ...r, state: "failed" } : r))],
  ];
  it.each(snippets)("folds the check from the agent's machine on %s, and opening it shows the snippet", async (_case, rows) => {
    mount(rows, running(["echo", "get-sum"]));
    const fold = screen.getByRole("button", { name: "Call it from an agent's machine, once a role reaches it" });
    expect(fold.getAttribute("aria-expanded")).toBe("false");
    expect(document.body.textContent).not.toContain("scout-tools__echo");
    await userEvent.click(fold);
    expect(fold.getAttribute("aria-expanded")).toBe("true");
    const snippet = [...document.querySelectorAll("pre")].map((p) => p.textContent || "").find((t) => t.includes("tools/call"));
    expect(snippet).toBe(agentSnippet("scout-tools", "echo"));
  });

  it("counts one tool in the singular", () => {
    mount(DONE, running(["echo"]), ["echo"]);
    expect(document.querySelector("[data-live]")?.textContent).toBe("scout-tools is live. Straza reached it and read 1 tool.");
    expect(document.querySelector('[data-next-tile="roles"]')?.textContent).toBe("Give access to itMake an application role that reaches some or all of its 1 tool.");
  });

  // Each case is a session's areas and the tiles it is shown: a tile whose
  // page the areas do not open is not drawn.
  const areas: [string, Record<string, true>, string[]][] = [
    ["the servers area alone", { apps: true }, ["servers"]],
    ["servers and policies", { apps: true, policy: true }, ["policies", "servers"]],
    ["servers and identity", { apps: true, identity: true }, ["roles", "servers"]],
  ];
  it.each(areas)("draws only the tiles a session with %s may open", (_case, held, want) => {
    sess.areas = held;
    mount(DONE, running(["echo"]));
    expect(tiles()).toEqual(want);
  });

  // Each case is a check that did not land whole, and what the Check step
  // says for it.
  const others: [string, ReturnType<typeof healthRead>, string][] = [
    ["runs and lists no tool", healthRead({ status: "running", tools: [] }, URL_), "empty"],
    ["is refused", healthRead({ status: "degraded", detail: "ping: upstream answered 401 Unauthorized" }, URL_), "refused"],
    ["is not reached", healthRead({ status: "degraded", detail: "connect failed: dial tcp 172.20.0.3:3001: connect: connection refused" }, URL_), "unreachable"],
    ["reads another state", healthRead({ status: "starting", detail: "" }, URL_), "other"],
  ];
  it.each(others)("stays on the Check step for a server that %s", (_case, read, kind) => {
    mount(DONE.map((r) => (r.key === "check" ? { ...r, state: kind === "empty" ? "done" : "failed" } : r)), read);
    expect(document.querySelector("[data-what-next]")).toBeNull();
    expect(document.querySelector("[data-landed]")).toBeTruthy();
    expect(document.querySelector("[data-health]")?.getAttribute("data-health")).toBe(kind);
    expect(tiles()).toEqual([]);
  });

  it("stays on the Check step, with its door to a role, when the secret was not stored on a server that runs", () => {
    mount(DONE.map((r) => (r.key === "secret" ? { ...r, state: "failed" } : r)), running(["echo"]));
    expect(document.querySelector("[data-what-next]")).toBeNull();
    expect(document.querySelector("[data-landed]")?.textContent).toContain("failedStore its secret");
    const card = document.querySelector("[data-nobody-reaches]") as HTMLElement;
    expect(within(card).getByRole("button", { name: "Create a role" })).toBeTruthy();
  });
});
