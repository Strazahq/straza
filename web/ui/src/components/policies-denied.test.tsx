import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PoliciesDenied } from "./policies-denied";
import { ApiError, type DefaultDenials, type DeniedTool, listDefaultDenials } from "@/lib/api";
import { take } from "@/lib/handoff";
import {
  DENIED_COLUMN, DENIED_CUT, DENIED_NONE, DENIED_SERVERS, DENIED_SERVERS_LINE, DENIED_TOOLS, DENIED_TOOLS_LINE, NAMELESS_HELP, NAMELESS_TOOL,
  SUBJECT_DENIED, WRITE_RULE, harnessWords, openRecord, writeRuleFor,
} from "@/lib/harness-words";
import { navigate } from "@/lib/router";
import { readFailed } from "@/lib/say";
import { snapshot } from "@/lib/session";
import { absTime } from "@/lib/words";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), listDefaultDenials: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));

const tool = (name: string, count: number, lastSeq: number, harnesses: string[]): DeniedTool => ({ name, harnesses, count, lastSeq, lastTime: "2026-11-11T08:00:00Z" });

// One server a harness runs itself with the two tools people asked for,
// two harness tools, and one harness tool the harness sent no name for.
const answer: DefaultDenials = {
  since: "2026-11-10T10:00:00Z",
  servers: [{ server: "claude-in-chrome", harnesses: ["claude-code"], count: 7, lastSeq: 9001, lastTime: "2026-11-11T09:30:00Z", tools: [tool("navigate", 4, 9001, ["claude-code"]), tool("form_input", 3, 8990, ["claude-code"])] }],
  tools: [tool("LSP", 2, 9002, ["claude-code"]), tool("NotebookEdit", 5, 9003, ["claude-code", "codex"]), tool("", 1, 9004, ["python-sdk"])],
};

const mount = (needle = "") => render(<PoliciesDenied needle={needle} />);
const row = (label: string) => document.querySelector('[data-denied="' + label + '"]') as HTMLElement;
const cells = (label: string) => Array.from(row(label).querySelectorAll("td")).map((c) => c.textContent || "");
const labels = () => Array.from(document.querySelectorAll("[data-denied]")).map((r) => r.getAttribute("data-denied"));
const ready = () => waitFor(() => expect(document.querySelector("[data-denied]")).toBeTruthy());

describe("the Denied by default view", () => {
  beforeEach(() => {
    vi.mocked(listDefaultDenials).mockReset().mockResolvedValue(answer);
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: "s1" });
    vi.mocked(navigate).mockClear();
    take("policies-new");
    take("audit");
  });

  it("groups the servers and the harness tools, a row each", async () => {
    mount();
    await ready();
    const view = document.querySelector("[data-denied-view]") as HTMLElement;
    expect(within(view).getByText(DENIED_SERVERS)).toBeTruthy();
    expect(within(view).getByText(DENIED_SERVERS_LINE)).toBeTruthy();
    expect(within(view).getByText(DENIED_TOOLS)).toBeTruthy();
    expect(within(view).getByText(DENIED_TOOLS_LINE)).toBeTruthy();
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual([DENIED_COLUMN.asked, DENIED_COLUMN.runs, DENIED_COLUMN.count, DENIED_COLUMN.last, DENIED_COLUMN.record]);
    expect(labels()).toEqual(["claude-in-chrome", "LSP", "NotebookEdit", NAMELESS_TOOL]);
  });

  it("reads a server's row: its tools, the harness, the count and the last denial", async () => {
    mount();
    await ready();
    const c = cells("claude-in-chrome");
    expect(c[0]).toBe("claude-in-chromenavigateform_input");
    expect(c[1]).toBe(harnessWords(["claude-code"]));
    expect(c[2]).toBe("7");
    expect(c[3]).toBe(absTime("2026-11-11T09:30:00Z"));
  });

  it("reads a harness tool's row with every harness it ran in", async () => {
    mount();
    await ready();
    const c = cells("NotebookEdit");
    expect(c[0]).toBe("NotebookEdit");
    expect(c[1]).toBe(harnessWords(["claude-code", "codex"]));
    expect(c[2]).toBe("5");
  });

  it("says a tool sent without a name cannot get a rule, and offers its record alone", async () => {
    mount();
    await ready();
    const nameless = row(NAMELESS_TOOL);
    expect(nameless.textContent).toContain(NAMELESS_TOOL);
    expect(nameless.textContent).toContain(NAMELESS_HELP);
    expect(within(nameless).queryByRole("button", { name: writeRuleFor(NAMELESS_TOOL) })).toBeNull();
    expect(within(nameless).getByRole("button", { name: openRecord(9004) })).toBeTruthy();
  });

  it.each([
    ["a server", "claude-in-chrome", { lane: "mcp", app: "claude-in-chrome", tools: ["navigate", "form_input"] }],
    ["a harness tool", "LSP", { lane: "harness", tools: ["LSP"] }],
  ])("opens the New policy wizard on %s with Which calls answered", async (_, label, door) => {
    mount();
    await ready();
    await userEvent.click(within(row(label)).getByRole("button", { name: writeRuleFor(label) }));
    expect(take("policies-new")).toEqual(door);
    expect(navigate).toHaveBeenCalledWith("policies", ["new"]);
  });

  it("hands Audit the newest record of a row", async () => {
    mount();
    await ready();
    await userEvent.click(within(row("LSP")).getByRole("button", { name: openRecord(9002) }));
    expect(take("audit")).toEqual({ seq: 9002 });
    expect(navigate).toHaveBeenCalledWith("audit");
  });

  it("says so when nothing was denied by default", async () => {
    vi.mocked(listDefaultDenials).mockResolvedValue({ since: answer.since, servers: [], tools: [] });
    mount();
    expect(await screen.findByText(DENIED_NONE)).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("says what could not be read and what to do next", async () => {
    const err = new ApiError("HTTP 503", 503);
    vi.mocked(listDefaultDenials).mockRejectedValue(err);
    mount();
    const block = await waitFor(() => {
      const el = document.querySelector("[data-fetch-error]");
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(block.textContent).toContain(SUBJECT_DENIED);
    expect(block.textContent).toContain(readFailed(SUBJECT_DENIED, err));
    expect(document.querySelector("[data-denied-none]")).toBeNull();
  });

  it("says the list was cut when the scan met more than it shows", async () => {
    vi.mocked(listDefaultDenials).mockResolvedValue({ ...answer, truncated: true });
    mount();
    await ready();
    expect((document.querySelector("[data-denied-cut]") as HTMLElement).textContent).toBe(DENIED_CUT);
  });

  it.each([
    ["a tool a server was asked for", "form", ["claude-in-chrome"]],
    ["a server's name", "CHROME", ["claude-in-chrome"]],
    ["a harness tool", "lsp", ["LSP"]],
  ])("narrows the rows on the search, by %s", async (_, needle, shown) => {
    mount(needle);
    await ready();
    expect(labels()).toEqual(shown);
    expect(screen.queryByText(shown[0] === "LSP" ? DENIED_SERVERS : DENIED_TOOLS)).toBeNull();
  });

  it("offers no rule to a session that may not write policy", async () => {
    vi.mocked(snapshot).mockReturnValue({ user: "joe", roles: [], grants: "audit:read", expiresIn: 300, sessionID: "s2" });
    mount();
    await ready();
    expect(screen.queryAllByRole("button", { name: new RegExp("^" + WRITE_RULE) })).toEqual([]);
    expect(screen.getByRole("button", { name: openRecord(9001) })).toBeTruthy();
  });
});
