import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Attention, attentionItems } from "./overview-attention";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type AppRow, type SinkRow, replaySink } from "@/lib/api";
import { NOTHING_TO_DO, configRows, draftsLine, postureOf, replayWord, replayedWords } from "@/lib/config-words";
import { navigate } from "@/lib/router";
import { relTimeText } from "@/lib/words";
import { notify } from "@/lib/notify";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), replaySink: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const APPS: AppRow[] = [
  { id: "a", name: "scout-tools", runtime: "remote", status: "failed", reached_by: [] },
  { id: "b", name: "midpoint", runtime: "remote", status: "degraded", reached_by: [] },
  { id: "c", name: "demo-tools", runtime: "command", status: "running", reached_by: [] },
];

const SINK: SinkRow = {
  name: "elastic", type: "elasticsearch", target: "http://elastic:9200", batch: 50, subjects: [], parked: 12,
  streams: [{ stream: "audit", pending: 0, inflight: 0, delivered: 1, duplicates: 0, parked: 12, last_error: "Elasticsearch answered 400" }],
};

const QUIET = { chain: { word: "intact" as const, seq: 4 }, apps: [], approvals: [], push: { lane_up: true, connected: 1 }, sinks: [], relaxed: [] };

const OLDEST = new Date(Date.now() - 2 * 3600 * 1000).toISOString();
const DRAFTS = { count: 3, more: false, who: "joe-java-developer-agent", at: OLDEST };

const mount = (props: Parameters<typeof Attention>[0]) => render(<TooltipProvider><Attention {...props} /></TooltipProvider>);

describe("Needs attention", () => {
  it("says nothing needs doing when every read came back clean", () => {
    mount(QUIET);
    expect(screen.getByText(NOTHING_TO_DO)).toBeTruthy();
  });

  it("orders the lines by what a person should look at first", () => {
    const relaxed = postureOf(configRows({ public_url: "http://box:8420", governance: { min_attestation: "none" } })).relaxed;
    const items = attentionItems({ ...QUIET, chain: { word: "broken", seq: 91 }, apps: APPS, sinks: [SINK], relaxed, push: { lane_up: false }, drafts: DRAFTS });
    expect(items.map((i) => i.key)).toEqual(["chain", "failed", "degraded", "drafts", "lane", "sink:elastic", "relaxed"]);
    expect(items[0].tone).toBe("danger");
    expect(items[1].tone).toBe("danger");
    expect(items[2].tone).toBe("warn");
  });

  it("says how many drafts wait for review, names the oldest's proposer, and opens Drafts (CD-2)", async () => {
    mount({ ...QUIET, drafts: DRAFTS });
    const row = document.querySelector('[data-attention="drafts"]') as HTMLElement;
    expect(row.textContent).toContain("3 drafts wait for review, the oldest from joe-java-developer-agent, drafted " + relTimeText(OLDEST) + ".");
    expect(draftsLine(1, false, "you", "5 m ago")).toBe("1 draft waits for review, the oldest from you, drafted 5 m ago.");
    expect(draftsLine(200, true, "alice", "1 h ago")).toBe("200 or more drafts wait for review, the oldest from alice, drafted 1 h ago.");
    await userEvent.click(row.querySelector("button, a") as HTMLElement);
    expect(navigate).toHaveBeenCalledWith("drafts", []);
    expect(attentionItems({ ...QUIET, drafts: { ...DRAFTS, count: 0 } })).toEqual([]);
    expect(attentionItems({ ...QUIET, drafts: null })).toEqual([]);
  });

  it("drops the lines a read could not answer rather than guessing", () => {
    const items = attentionItems({ ...QUIET, apps: null, approvals: null, sinks: null });
    expect(items).toEqual([]);
  });

  it("does not claim all checks are clear when audit verification is unavailable", () => {
    mount({ ...QUIET, compact: true, chain: { word: "unverified", seq: 4 } });
    expect(screen.queryByText(NOTHING_TO_DO)).toBeNull();
  });

  it("replays a sink's parked events and toasts what the server answered", async () => {
    vi.mocked(replaySink).mockResolvedValue({ sink: "elastic", replayed: 12, remaining: 0 });
    mount({ ...QUIET, sinks: [SINK] });
    await userEvent.click(screen.getByRole("button", { name: replayWord("elastic") }));
    await waitFor(() => expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(replayedWords("elastic", 12, 0)));
  });
});
