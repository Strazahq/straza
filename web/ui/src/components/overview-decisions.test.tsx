import { describe, expect, it, vi } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Decisions, type HourRead, Stopped } from "./overview-decisions";
import { MINUTE_MS, chartCeiling, decisionHour } from "./overview-decision-chart";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { DecisionBucket, DecisionsBlock } from "@/lib/api";
import { DECIDED_NONE, DECIDED_PANEL, DECIDED_UNREAD, LAST_HOUR_NONE, LAST_HOUR_READING, LAST_HOUR_UNREAD, LAST_HOUR_UNSUPPORTED, STOPPED_NONE, bucketTitle, seatLine, stoppedRowLabel } from "@/lib/config-words";
import { take } from "@/lib/handoff";
import { navigate } from "@/lib/router";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const bucket = (hour: number, allowed: number, approval: number, denied: number) =>
  ({ start: "2026-09-13T" + String(hour).padStart(2, "0") + ":00:00Z", allowed, approval, denied });

const BLOCK: DecisionsBlock = {
  since: "2026-09-13T00:00:00Z",
  buckets: [bucket(0, 10, 0, 0), bucket(1, 4, 2, 1), bucket(2, 0, 0, 0)],
  stopped: [{ app: "demo-tools", tool: "get-env", outcome: "denied", count: 23, reason: "dumps the process environment" }],
};

const QUIET: DecisionsBlock = { since: "2026-09-13T00:00:00Z", buckets: [bucket(0, 0, 0, 0), bucket(1, 0, 0, 0)], stopped: [] };

// The last hour from 10:00: one call a minute, a busy minute at 10:30 and
// seven calls so far in the minute that is still running.
const minutesFrom = (first: number): DecisionBucket[] => Array.from({ length: 60 }, (_, i) => {
  const at = first + i;
  return { start: new Date(Date.parse("2026-09-13T10:00:00Z") + at * MINUTE_MS).toISOString(), allowed: at === 30 ? 5 : at === 59 ? 7 : 1, approval: at === 30 ? 2 : 0, denied: at === 30 ? 1 : 0, catalog: at === 30 ? 4 : 0 };
});
const MINUTES = minutesFrom(0);

const panel = (block: DecisionsBlock | null, hour: HourRead | null, onHour: (on: boolean) => void = () => {}) => <TooltipProvider><Decisions block={block} hour={hour} onHour={onHour} /></TooltipProvider>;
const strip = (block: DecisionsBlock | null, hour: HourRead | null = null) => render(panel(block, hour));
const table = (block: DecisionsBlock | null) => render(<TooltipProvider><Stopped block={block} /></TooltipProvider>);

describe("the decisions strip", () => {
  it("draws one bar an hour, with the hour's numbers on it", () => {
    strip(BLOCK);
    const bars = document.querySelectorAll("[data-bucket]");
    expect(bars.length).toBe(3);
    expect(bars[1].getAttribute("aria-label")).toBe(bucketTitle("01:00", "02:00", 4, 2, 1));
    // The hour that held and denied carries three bars; a quiet hour one.
    expect(bars[1].querySelectorAll(".chart-stack > span").length).toBe(3);
    expect(bars[0].querySelectorAll(".chart-stack > span").length).toBe(1);
  });

  it("scales low and high traffic without clipping, and labels midnight in UTC", () => {
    for (const [count, ceiling] of [[0, 2], [3, 5], [11, 20], [625, 1000], [15001, 20000]]) {
      expect(chartCeiling(count)).toBe(ceiling);
    }
    expect(decisionHour("2026-09-13T23:00:00Z", 1)).toBe("00:00");
  });

  it("supports keyboard inspection, exact hourly evidence and return focus", async () => {
    strip(BLOCK);
    const bars = screen.getAllByRole("button", { name: /UTC:/ });
    await userEvent.click(bars[0]);
    expect(screen.getByRole("dialog").textContent).toContain("Hourly tool decisions");
    await userEvent.keyboard("{Escape}");
    expect(document.activeElement).toBe(bars[0]);
    await userEvent.keyboard("{ArrowRight}");
    expect(document.activeElement).toBe(bars[1]);
    expect(screen.getByRole("tooltip").textContent).toContain("Tool calls allowed4Denied1Required approval2");
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
    await userEvent.keyboard("{Enter}");
    const evidence = screen.getByRole("dialog");
    expect(evidence.textContent).toContain("Tool calls allowed4Denied1Required approval2");
    expect(within(evidence).getByRole("link", { name: "Open audit" })).toBeTruthy();
  });

  it("says what would be here when no call was decided and when the server sends no block", () => {
    strip(QUIET);
    expect(screen.getByText(DECIDED_NONE)).toBeTruthy();
    strip(null);
    expect(screen.getByText(DECIDED_UNREAD)).toBeTruthy();
    expect(screen.getAllByText(DECIDED_NONE)).toHaveLength(1);
  });

  it.each<[string, (number | undefined)[], string | null]>([
    ["the sum of the hours beside the totals", [200, 20, 0], "220Catalog reads, counted apart"],
    ["nothing when no catalog was read", [0, 0, 0], null],
    ["nothing when the server sends no catalog count", [undefined, undefined, undefined], null],
    ["the hours that carry a count when others carry none", [undefined, 1500, undefined], "1,500Catalog reads, counted apart"],
  ])("counts catalog reads apart from the tool calls: %s", (_name, reads, want) => {
    strip({ ...BLOCK, buckets: BLOCK.buckets.map((b, i) => (reads[i] === undefined ? b : { ...b, catalog: reads[i] })) });
    const total = (word: string) => document.querySelector('[data-total="' + word + '"]')?.textContent ?? null;
    expect(total("allowed")).toBe("14Tool calls allowed");
    expect(total("denied")).toBe("1Denied");
    expect(total("needed approval")).toBe("2Required approval");
    expect(total("catalog")).toBe(want);
    expect(document.querySelectorAll("[data-decided-totals] > [data-total]")).toHaveLength(3);
  });

  it("reads a day of catalog reads alone as no tool decisions", () => {
    strip({ ...QUIET, buckets: QUIET.buckets.map((b) => ({ ...b, catalog: 40 })) });
    expect(screen.getByText(DECIDED_NONE)).toBeTruthy();
    expect(document.querySelector("[data-total]")).toBeNull();
  });

  it("names the grant when this session may not read the counts", () => {
    render(<TooltipProvider><Decisions block={null} denied hour={null} onHour={() => {}} /></TooltipProvider>);
    expect(screen.getByText(seatLine(DECIDED_PANEL, "config:read"))).toBeTruthy();
  });
});

describe("the last hour view", () => {
  const bars = () => Array.from(document.querySelectorAll<HTMLButtonElement>("[data-bucket]"));
  const total = (word: string) => document.querySelector('[data-total="' + word + '"]')?.textContent ?? null;
  const pressed = () => within(screen.getByRole("group", { name: "Window" })).getAllByRole("button").map((b) => b.textContent + ":" + b.getAttribute("aria-pressed"));

  it("offers the two windows as a switch and reports the one that was picked", async () => {
    const onHour = vi.fn();
    const view = render(panel(BLOCK, null, onHour));
    expect(pressed()).toEqual(["Last 24 hours:true", "Last hour:false"]);
    await userEvent.click(screen.getByRole("button", { name: "Last hour" }));
    expect(onHour).toHaveBeenLastCalledWith(true);
    view.rerender(panel(BLOCK, { minutes: MINUTES, note: "" }, onHour));
    expect(pressed()).toEqual(["Last 24 hours:false", "Last hour:true"]);
    await userEvent.click(screen.getByRole("button", { name: "Last 24 hours" }));
    expect(onHour).toHaveBeenLastCalledWith(false);
  });

  it("draws one bar a minute and counts the same hour in the totals", () => {
    strip(BLOCK, { minutes: MINUTES, note: "" });
    expect(bars()).toHaveLength(60);
    expect(total("allowed")).toBe("70Tool calls allowed");
    expect(total("denied")).toBe("1Denied");
    expect(total("needed approval")).toBe("2Required approval");
    expect(total("catalog")).toBe("4Catalog reads, counted apart");
    const chart = screen.getByRole("group", { name: "Calls decided per minute over the last hour" });
    expect(chart.querySelector(".chart-caption")?.textContent).toBe("Decisions per minuteUTC");
    expect(Array.from(chart.querySelectorAll(".chart-x span")).map((x) => x.textContent)).toEqual(["10:00", "10:15", "10:30", "10:45", "11:00"]);
    expect(bars()[30].getAttribute("aria-label")).toBe(bucketTitle("10:30", "10:31", 5, 2, 1));
    expect(decisionHour("2026-09-13T23:59:00Z", 1, MINUTE_MS)).toBe("00:00");
  });

  it("says so far on the minute that is still running, on its bar, its tooltip and its sheet", async () => {
    strip(BLOCK, { minutes: MINUTES, note: "" });
    expect(bars()[59].getAttribute("aria-label")).toBe("10:59 to 11:00 UTC, so far: 7 allowed, 0 needed approval, 0 denied");
    act(() => bars()[59].focus());
    expect(screen.getByRole("tooltip").textContent).toContain("10:59 to 11:00 UTC, so far");
    await userEvent.keyboard("{Enter}");
    const running = screen.getByRole("dialog");
    expect(running.textContent).toContain("Tool decisions in one minute");
    expect(running.textContent).toContain("2026-09-13 · 10:59 to 11:00 UTC, so far");
    expect(running.textContent).toContain("Tool calls allowed7Denied0Required approval0");
    expect(within(running).getByRole("link", { name: "Open audit" })).toBeTruthy();
    await userEvent.keyboard("{Escape}");
    expect(document.activeElement).toBe(bars()[59]);

    await userEvent.click(bars()[30]);
    const ended = screen.getByRole("dialog");
    expect(ended.textContent).toContain("2026-09-13 · 10:30 to 10:31 UTC");
    expect(ended.textContent).not.toContain("so far");
  });

  it("moves between minutes with the arrow keys and keeps the tooltip on its minute when the window moves on", async () => {
    const view = strip(BLOCK, { minutes: MINUTES, note: "" });
    act(() => bars()[58].focus());
    await userEvent.keyboard("{ArrowRight}");
    expect(document.activeElement).toBe(bars()[59]);
    await userEvent.keyboard("{Home}");
    expect(document.activeElement).toBe(bars()[0]);
    await userEvent.keyboard("{End}");
    expect(screen.getByRole("tooltip").textContent).toContain("10:59 to 11:00 UTC, so far");
    // A refresh one minute later: 10:59 has ended and sits one column to the left.
    view.rerender(panel(BLOCK, { minutes: minutesFrom(1), note: "" }));
    expect(document.activeElement).toBe(bars()[58]);
    expect(screen.getByRole("tooltip").textContent).toContain("10:59 to 11:00 UTC");
    expect(screen.getByRole("tooltip").textContent).not.toContain("so far");
    expect(bars()[58].getAttribute("aria-describedby")).toBe(screen.getByRole("tooltip").id);
  });

  it.each<[string, HourRead, string]>([
    ["the first read has not answered", { minutes: null, note: LAST_HOUR_READING }, LAST_HOUR_READING],
    ["the server sends no minutes", { minutes: null, note: LAST_HOUR_UNSUPPORTED }, LAST_HOUR_UNSUPPORTED],
    ["the read failed before any minutes arrived", { minutes: null, note: LAST_HOUR_UNREAD }, LAST_HOUR_UNREAD],
    ["no call was decided in the hour", { minutes: MINUTES.map((m) => ({ ...m, allowed: 0, approval: 0, denied: 0 })), note: "" }, LAST_HOUR_NONE],
  ])("says one sentence in place of the chart when %s", (_name, hour, sentence) => {
    strip(BLOCK, hour);
    expect(screen.getByText(sentence)).toBeTruthy();
    expect(bars()).toHaveLength(0);
    expect(total("allowed")).toBeNull();
  });
});

describe("what was stopped", () => {
  it("opens Audit filtered to the tool of the row", async () => {
    table(BLOCK);
    await userEvent.click(screen.getByRole("button", { name: stoppedRowLabel("demo-tools / get-env") }));
    expect(screen.getByRole("dialog").textContent).toContain("dumps the process environment");
    await userEvent.click(screen.getByRole("button", { name: "Open audit for this tool" }));
    expect(take<{ q: string }>("audit")).toEqual({ q: "demo-tools / get-env" });
    expect(vi.mocked(navigate)).toHaveBeenCalledWith("audit");
  });

  it("reads the outcome as a word in its hue", () => {
    table({ ...BLOCK, stopped: [{ app: "", tool: "shell.exec", outcome: "approval", count: 3, reason: "held for sec-approvers" }] });
    const row = document.querySelector("[data-stopped]") as HTMLElement;
    expect(row.getAttribute("data-stopped")).toBe("shell.exec");
    expect(within(row).getByText("needs approval").getAttribute("data-tone")).toBe("warn");
  });

  it("says nothing was stopped yet when the block carries no rows", () => {
    table(QUIET);
    expect(screen.getByText(STOPPED_NONE)).toBeTruthy();
  });
});
