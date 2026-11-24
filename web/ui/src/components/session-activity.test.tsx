import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import userEvent from "@testing-library/user-event";
import { FileIcon, GlobeIcon, LaptopIcon, ServerIcon, TerminalIcon } from "lucide-react";
import { SessionActivity } from "./session-activity";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { ApprovalRow, AuditRow } from "@/lib/api";
import { navigate } from "@/lib/router";
import { type ActivityFilter, sessionActivity } from "@/lib/session-activity";
import type { Read } from "@/lib/user-read";
import { absTime } from "@/lib/words";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const ID = "01a1da3d-934c-7be8-bd1a-92e51c045044";
const T = (s: number) => new Date(Date.parse("2026-11-14T14:27:10Z") + s * 1000).toISOString();
const rec = (seq: number, type: string, s: number, data: Record<string, unknown>): AuditRow =>
  ({ seq, ce: JSON.stringify({ type, time: T(s), data: { session: ID, user: "u-altair", ...data } }), username: "altair" });

const RECORDS: AuditRow[] = [
  rec(1, "straza.audit.mcp", 10, { tool: "mcp.call", app: "github", toolName: "list_issues", effect: "allow", role: "github-readers" }),
  rec(2, "straza.audit.tool", 20, { tool: "shell.exec", command: "rm -rf build/", effect: "deny", ruleId: "deny-rm-rf", setName: "agent-guardrails", reason: "Recursive force delete is refused." }),
  rec(3, "straza.audit.mcp", 30, { tool: "mcp.call", app: "", toolName: "github__delete_branch", effect: "deny", reason: 'unknown tool "github__delete_branch": no access row for this session\'s roles admits it' }),
  rec(4, "straza.audit.approval", 40, { phase: "request", approvalId: "apr-1", rule: "push-needs-yes", set: "agent-guardrails", lane: "hook", state: "pending", summary: "shell.exec: git push origin main" }),
  rec(5, "straza.audit.tool", 50, { tool: "mcp.call", app: "elevenlabs", toolName: "search_voices", effect: "allow", ruleId: "elevenlabs-audio-allow", setName: "audio-gen-access" }),
  rec(6, "straza.audit.tool", 60, { tool: "file.read", paths: ["/etc/hosts"], effect: "allow" }),
  rec(7, "straza.audit.tool", 70, { tool: "net.fetch", paths: ["https://example.com/x"], effect: "allow" }),
];
const ROWS = sessionActivity(RECORDS, [] as ApprovalRow[], ID);
const READY: Read<AuditRow[]> = { kind: "ready", data: RECORDS };

type Over = Partial<React.ComponentProps<typeof SessionActivity>>;
const draw = (over: Over = {}) => {
  const props = { onFilter: vi.fn(), onReload: vi.fn() };
  const all = { id: ID, allowed: true, approvals: true, read: READY, rows: ROWS, records: RECORDS.length, full: false, filter: "all" as ActivityFilter, registered: ["github"], ...props, ...over };
  render(<TooltipProvider><SessionActivity {...all} /></TooltipProvider>);
  return all;
};
const line = (seq: number) => document.querySelector('[data-activity="' + seq + '"]') as HTMLElement;
const shown = () => [...document.querySelectorAll("[data-activity]")].map((li) => Number(li.getAttribute("data-activity")));
const glyph = (icon: ReactElement) => { const box = document.createElement("div"); box.innerHTML = renderToStaticMarkup(icon); return (box.querySelector("svg") as SVGElement).innerHTML; };
const iconOf = (seq: number) => (line(seq).querySelector("[data-activity-call] svg") as SVGElement).innerHTML;

describe("the activity", () => {
  beforeEach(() => {
    vi.mocked(navigate).mockReset();
  });

  it("says in one line that the role does not include Audit, and nothing else", () => {
    draw({ allowed: false, rows: null, read: { kind: "off" } });
    const card = document.querySelector("[data-activity-card]") as HTMLElement;
    expect(card.textContent).toBe("ActivityThe activity is not shown, because your role does not include Audit.");
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("says it reads what the session did", () => {
    draw({ read: { kind: "loading" }, rows: null, records: 0 });
    expect(screen.getByRole("status").textContent).toBe("Reading what the session did.");
    expect(screen.queryByRole("group")).toBeNull();
    expect(document.querySelector("[data-activity-foot]")).toBeNull();
  });

  it("says a failed read", () => {
    draw({ read: { kind: "failed", message: "The activity could not be read: boom. Reload to try again." }, rows: null, records: 0 });
    const block = document.querySelector("[data-fetch-error]") as HTMLElement;
    expect(block.textContent).toBe("The activity could not be read: boom. Reload to try again.");
  });

  it("says nothing is recorded yet", () => {
    draw({ read: { kind: "ready", data: [] }, rows: [], records: 0 });
    expect(screen.getByText("Nothing is recorded for this session yet.")).toBeTruthy();
    expect(screen.queryByRole("group")).toBeNull();
    expect(document.querySelector("[data-activity-foot]")).toBeNull();
  });

  it("says a filter leaves no call", () => {
    draw({ rows: ROWS.filter((r) => r.outcome === "allowed"), filter: "denied" });
    expect(screen.getByText("No call of this kind.")).toBeTruthy();
    expect(shown()).toEqual([]);
  });

  it.each([
    ["all", [7, 6, 5, 4, 3, 2, 1]],
    ["waiting", [4]],
    ["denied", [3, 2]],
  ] as const)("shows the calls of the filter %s, newest first, each option with its count", async (filter, seqs) => {
    const { onFilter } = draw({ filter });
    expect(shown()).toEqual(seqs);
    const group = screen.getByRole("group", { name: "Which calls to show" });
    expect(within(group).getAllByRole("button").map((b) => b.textContent)).toEqual(["All 7", "Waiting 1", "Denied 2"]);
    expect(within(group).getByRole("button", { pressed: true }).getAttribute("data-activity-filter")).toBe(filter);
    await userEvent.click(within(group).getByRole("button", { name: "Waiting 1" }));
    expect(onFilter).toHaveBeenLastCalledWith("waiting");
  });

  it("draws a call with its clock time, its words, its outcome, its line and its note", () => {
    draw();
    const denied = line(2);
    const time = denied.firstElementChild as HTMLElement;
    expect(time.textContent).toMatch(/^\d\d:\d\d:\d\d$/);
    expect(time.getAttribute("title")).toBe(absTime(T(20)));
    expect(within(denied).getByRole("button", { name: "shell rm -rf build/" })).toBeTruthy();
    expect(denied.querySelector("[data-outcome]")?.textContent).toBe("denied");
    expect(denied.querySelector("[data-activity-line]")?.textContent).toBe("Recursive force delete is refused.");
    expect(denied.querySelector("[data-activity-note]")?.textContent).toBe("Policy rule deny-rm-rf in agent-guardrails");
    expect(line(3).querySelector("[data-outcome]")?.textContent).toBe("no access");
    expect(line(3).querySelector("[data-activity-line]")?.textContent).toBe("No role of altair reaches this tool.");
    expect(line(1).querySelector("[data-activity-note]")?.textContent).toBe("Allowed through github-readers.");
    expect(line(1).querySelector("[data-activity-line]")).toBeNull();
    expect(within(line(1)).getByRole("button", { name: "github list_issues" })).toBeTruthy();
    expect(within(line(6)).getByRole("button", { name: "file read /etc/hosts" })).toBeTruthy();
    expect(within(line(7)).getByRole("button", { name: "network fetch example.com" })).toBeTruthy();
  });

  it("leads each call with its icon, a server on a person's machine with the laptop", () => {
    draw();
    expect(iconOf(1)).toBe(glyph(<ServerIcon />));
    expect(iconOf(5)).toBe(glyph(<LaptopIcon />));
    expect(iconOf(2)).toBe(glyph(<TerminalIcon />));
    expect(iconOf(6)).toBe(glyph(<FileIcon />));
    expect(iconOf(7)).toBe(glyph(<GlobeIcon />));
  });

  it("draws every server with the server icon while the registered servers are not known", () => {
    draw({ registered: null });
    expect(iconOf(5)).toBe(glyph(<ServerIcon />));
  });

  it("opens the waiting call's request with the approvals area, and offers no door without it", async () => {
    draw();
    expect(line(4).querySelector("[data-activity-line]")?.textContent).toBe("Waiting for a person's answer.");
    await userEvent.click(within(line(4)).getByRole("button", { name: "Open the request" }));
    expect(navigate).toHaveBeenCalledWith("approvals", ["requests", "apr-1"]);
  });

  it("offers no request door without the approvals area", () => {
    draw({ approvals: false });
    expect(within(line(4)).queryByRole("button", { name: "Open the request" })).toBeNull();
  });

  it("opens the record of a call in the audit record sheet", async () => {
    draw();
    await userEvent.click(within(line(2)).getByRole("button", { name: "shell rm -rf build/" }));
    expect((await screen.findByRole("dialog")).getAttribute("data-record-sheet")).toBe("2");
  });

  // The sheet has two buttons named Close: the one in its foot and the cross
  // in its corner.
  const closeIn = (slot: string) => {
    const foot = document.querySelector('[data-slot="sheet-footer"]') as HTMLElement;
    const all = within(screen.getByRole("dialog")).getAllByRole("button", { name: "Close" });
    return userEvent.click(all.find((b) => foot.contains(b) === (slot === "foot")) as HTMLElement);
  };

  it.each([
    ["Escape", () => userEvent.keyboard("{Escape}")],
    ["Close in the foot", () => closeIn("foot")],
    ["the cross in the corner", () => closeIn("corner")],
  ])("gives the focus back to the call that opened the sheet after %s", async (_how, close) => {
    draw();
    const call = within(line(2)).getByRole("button", { name: "shell rm -rf build/" });
    await userEvent.click(call);
    await screen.findByRole("dialog");
    await close();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(call));
  });

  it("asks for a fresh read from Reload", async () => {
    const { onReload } = draw();
    await userEvent.click(screen.getByRole("button", { name: "Reload the activity" }));
    expect(onReload).toHaveBeenCalledTimes(1);
  });

  it.each([
    [7, false, "All 7 records in Audit"],
    [1, false, "The 1 record in Audit"],
    [1000, true, "Open all records in Audit"],
  ])("links %i records to the session's search in Audit, cut %s", async (records, full, words) => {
    draw({ records, full });
    const foot = screen.getByRole("link", { name: words });
    expect(foot.getAttribute("href")).toContain("audit/" + ID);
    await userEvent.click(foot);
    expect(navigate).toHaveBeenCalledWith("audit", [ID]);
  });
});
