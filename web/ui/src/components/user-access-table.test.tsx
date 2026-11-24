import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AccessTable } from "./user-access-table";
import type { RoleRow } from "@/lib/api";
import { navigate } from "@/lib/router";
import type { MachineAccess, ServerAccess } from "@/lib/user-access";
import { BY_PATTERN_ONLY, EVERY_TOOL, MORE_BY_PATTERN, NOT_RUNNING, ON_MACHINE, THROUGH, toolsNamed } from "@/lib/user-words";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const roles: RoleRow[] = [{ id: "r-gh", name: "github-readers", kind: "application" }, { id: "r-pe", name: "platform-engineer", kind: "business" }];
const row = (server: string, extra: Partial<ServerAccess>): ServerAccess => ({ server, running: true, total: 0, allowed: [], approval: [], denied: [], through: [], ...extra });
const local = (server: string, extra: Partial<MachineAccess>): MachineAccess => ({ server, onMachine: true, open: false, allowed: [], approval: [], denied: [], policies: [], ...extra });
const cell = (server: string) => document.querySelector("[data-server='" + server + "']") as HTMLElement;
const lines = (server: string) => [...cell(server).querySelectorAll("[data-line]")].map((l) => l.getAttribute("data-line"));

describe("the Access table", () => {
  it("puts the lines that need approval and are denied above the allowed one, each tool in its outcome", () => {
    render(<AccessTable roles={roles} policy rows={[row("github", { total: 21, allowed: ["list_issues", "get_file"], approval: ["create_pull_request"], denied: ["delete_repo"], through: ["github-readers"] })]} />);
    expect(cell("github").textContent).toContain("3 of 21 tools");
    expect(lines("github")).toEqual(["approval", "denied", "allowed"]);
    expect(within(cell("github")).getAllByTitle("create_pull_request: needs approval")[0].getAttribute("data-outcome")).toBe("approval");
    expect(within(cell("github")).getAllByTitle("delete_repo: denied")[0].getAttribute("data-outcome")).toBe("denied");
    expect(cell("github").querySelector("[data-line=allowed]")?.textContent).toContain("2 allowed");
  });

  it("shows five allowed tools and folds the rest", async () => {
    const allowed = ["echo", "now", "sum", "list_files", "read_file", "uuid", "hash"];
    render(<AccessTable roles={roles} policy rows={[row("demo-tools", { total: 14, allowed })]} />);
    const fold = within(cell("demo-tools")).getByRole("button", { name: "+2 more" });
    expect(within(cell("demo-tools")).queryAllByTitle("uuid: allowed")).toEqual([]);
    await userEvent.click(fold);
    expect(within(cell("demo-tools")).getAllByTitle("uuid: allowed")[0].getAttribute("data-outcome")).toBe("allowed");
  });

  it.each<[string, Partial<ServerAccess>, string]>([
    ["with an exception above it", { total: 3, allowed: ["a", "b"], approval: ["c"] }, EVERY_TOOL + "1 needs approvalc2 allowedab"],
    ["as the only line", { total: 2, allowed: ["a", "b"] }, EVERY_TOOL + "2 allowedab"],
    ["on a server of one tool", { total: 1, allowed: ["a"] }, EVERY_TOOL + "1 alloweda"],
  ])("says every tool once in the count pill and names each tool reached %s", (_name, extra, words) => {
    render(<AccessTable roles={roles} policy rows={[row("midpoint", extra)]} />);
    expect(cell("midpoint").textContent).toBe("midpoint" + words);
    expect(within(cell("midpoint")).getAllByTitle("a: allowed")[0].getAttribute("data-outcome")).toBe("allowed");
  });

  it("opens the names past five when a role grants every tool", async () => {
    const allowed = ["a", "b", "c", "d", "e", "f", "g"];
    render(<AccessTable roles={roles} policy rows={[row("midpoint", { total: 7, allowed })]} />);
    expect(within(cell("midpoint")).queryAllByTitle("g: allowed")).toEqual([]);
    await userEvent.click(within(cell("midpoint")).getByRole("button", { name: "+2 more" }));
    expect(within(cell("midpoint")).getAllByTitle("g: allowed")[0].getAttribute("data-outcome")).toBe("allowed");
  });

  it("shows a server that is not running with one pill and no line", () => {
    render(<AccessTable roles={roles} policy rows={[row("jira", { running: false, through: ["github-readers"] })]} />);
    expect(cell("jira").querySelector("[data-not-running]")?.textContent).toBe(NOT_RUNNING);
    expect(lines("jira")).toEqual([]);
  });

  it("links each role a server comes through to the role's page", async () => {
    render(<AccessTable roles={roles} policy rows={[row("github", { total: 1, allowed: ["a"], through: ["github-readers", "platform-engineer"] })]} />);
    const through = cell("github").querySelector("[data-through]") as HTMLElement;
    expect(through.textContent).toBe("throughgithub-readersplatform-engineer");
    await userEvent.click(screen.getByRole("link", { name: "platform-engineer" }));
    expect(navigate).toHaveBeenCalledWith("roles", ["r-pe"]);
  });

  it("packs the Through tags at the top of their cell however tall the row", () => {
    render(<AccessTable roles={roles} policy rows={[row("github", { total: 9, allowed: ["a"], denied: ["b"], approval: ["c"], through: ["github-readers", "platform-engineer"] })]} />);
    const through = cell("github").querySelector("[data-through]") as HTMLElement;
    expect(through.className).toContain("flex-wrap");
    expect(through.className).toContain("content-start");
  });
});

describe("a server on the person's machine in the Access table", () => {
  const speech = local("elevenlabs", { allowed: ["text_to_speech"], approval: ["clone_voice"], denied: ["delete_voice"], policies: ["audio-access", "audio-guard"] });

  it("draws the laptop tile, the name in mono and the caption under it, after a registered server", () => {
    render(<AccessTable roles={roles} policy rows={[row("github", { total: 1, allowed: ["a"] }), speech]} />);
    expect([...document.querySelectorAll("[data-server]")].map((r) => r.getAttribute("data-server"))).toEqual(["github", "elevenlabs"]);
    expect(cell("github").querySelector("[data-kind-icon]")?.getAttribute("data-kind-icon")).toBe("server");
    expect(cell("elevenlabs").querySelector("[data-kind-icon]")?.getAttribute("data-kind-icon")).toBe("local-server");
    expect((within(cell("elevenlabs")).getByTitle("elevenlabs")).className).toContain("font-mono");
    const caption = within(cell("elevenlabs")).getByText(ON_MACHINE);
    expect(caption.className).toContain("text-muted-foreground");
    expect(within(cell("github")).queryByText(ON_MACHINE)).toBeNull();
  });

  it("counts the tools named in a pill and draws each in its line, approval and denied above allowed, with no pill for every tool", () => {
    render(<AccessTable roles={roles} policy rows={[speech]} />);
    expect(cell("elevenlabs").querySelector("[data-named]")?.textContent).toBe(toolsNamed(3));
    expect(lines("elevenlabs")).toEqual(["approval", "denied", "allowed"]);
    expect(within(cell("elevenlabs")).getAllByTitle("clone_voice: needs approval")[0].getAttribute("data-outcome")).toBe("approval");
    expect(within(cell("elevenlabs")).getAllByTitle("delete_voice: denied")[0].getAttribute("data-outcome")).toBe("denied");
    expect(within(cell("elevenlabs")).getAllByTitle("text_to_speech: allowed")[0].getAttribute("data-outcome")).toBe("allowed");
    expect(cell("elevenlabs").querySelector("[data-every]")).toBeNull();
    expect(cell("elevenlabs").textContent).not.toContain(" of ");
    expect(cell("elevenlabs").querySelector("[data-by-pattern]")).toBeNull();
  });

  it("says 1 tool named for one tool, and lists every allowed tool by name with five shown", async () => {
    render(<AccessTable roles={roles} policy rows={[local("one", { allowed: ["speak"] }), local("seven", { allowed: ["a", "b", "c", "d", "e", "f", "g"] })]} />);
    expect(cell("one").querySelector("[data-named]")?.textContent).toBe("1 tool named");
    expect(cell("seven").querySelector("[data-named]")?.textContent).toBe("7 tools named");
    expect(cell("seven").querySelector("[data-every]")).toBeNull();
    expect(within(cell("seven")).queryAllByTitle("f: allowed")).toEqual([]);
    await userEvent.click(within(cell("seven")).getByRole("button", { name: "+2 more" }));
    expect(within(cell("seven")).getAllByTitle("f: allowed")[0].getAttribute("data-outcome")).toBe("allowed");
  });

  it.each<[string, Partial<MachineAccess>, string | null, string | null]>([
    ["open with tools named", { open: true, allowed: ["speak"] }, "1 tool named", MORE_BY_PATTERN],
    ["open with no tool named", { open: true }, null, BY_PATTERN_ONLY],
    ["closed with tools named", { allowed: ["speak"] }, "1 tool named", null],
  ])("words a server %s", (_name, extra, pill, caption) => {
    render(<AccessTable roles={roles} policy rows={[local("elevenlabs", extra)]} />);
    expect(cell("elevenlabs").querySelector("[data-named]")?.textContent ?? null).toBe(pill);
    const words = cell("elevenlabs").querySelector("[data-by-pattern]");
    expect(words?.textContent ?? null).toBe(caption);
    // The pattern beside named tools is a pill, the pattern alone a muted line.
    if (words) expect(words.className.includes("text-muted-foreground")).toBe(caption === BY_PATTERN_ONLY);
  });

  it("draws no line and no pill for a server a pattern reaches alone", () => {
    render(<AccessTable roles={roles} policy rows={[local("filesystem", { open: true })]} />);
    expect(lines("filesystem")).toEqual([]);
    expect(cell("filesystem").querySelector("[data-named]")).toBeNull();
  });

  it("links each policy to its page for a reader with the policy area", async () => {
    render(<AccessTable roles={roles} policy rows={[speech]} />);
    const through = cell("elevenlabs").querySelector("[data-through]") as HTMLElement;
    expect(through.textContent).toBe(THROUGH + "audio-accessaudio-guard");
    const tag = through.querySelector("[data-tag=audio-access]") as HTMLElement;
    expect(tag.querySelector("svg")?.getAttribute("class")).toContain("file-check");
    const link = within(through).getByRole("link", { name: "audio-guard" });
    expect(link.getAttribute("href")).toMatch(/policies\/audio-guard$/);
    await userEvent.click(link);
    expect(navigate).toHaveBeenCalledWith("policies", ["audio-guard"]);
  });

  it("names each policy as plain text for a reader without the policy area", () => {
    render(<AccessTable roles={roles} policy={false} rows={[speech]} />);
    const through = cell("elevenlabs").querySelector("[data-through]") as HTMLElement;
    expect(within(through).queryAllByRole("link")).toEqual([]);
    expect([...through.querySelectorAll("[data-tag]")].map((t) => t.getAttribute("data-tag"))).toEqual(["audio-access", "audio-guard"]);
  });

  it("opens the Through cell with the word through below the sm width, and leaves it out with no policy", () => {
    render(<AccessTable roles={roles} policy rows={[speech, local("filesystem", { open: true })]} />);
    const word = within(cell("elevenlabs").querySelector("[data-through]") as HTMLElement).getByText(THROUGH);
    expect(word.className).toContain("sm:hidden");
    expect((cell("filesystem").querySelector("[data-through]") as HTMLElement).textContent).toBe("");
  });
});
