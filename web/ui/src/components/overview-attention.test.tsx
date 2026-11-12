import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Attention, attentionItems } from "./overview-attention";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type AppRow, type SinkRow, replaySink } from "@/lib/api";
import { NOTHING_TO_DO, configRows, draftsLine, postureOf, replayWord, replayedWords } from "@/lib/config-words";
import type { SigningKeyRow } from "@/lib/keys-api";
import { certExpiredLine, certExpiringLine, stagedWaitingLine } from "@/lib/keys-words";
import { KEYS_TAB } from "@/lib/settings-words";
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

  describe("the signing key lines", () => {
    const DAY = 86_400_000;
    const at = (ms: number) => new Date(Date.now() + ms).toISOString();
    const key = (over: Partial<SigningKeyRow>): SigningKeyRow => ({ kid: "6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10", purpose: "client_assertion", status: "active", created_at: at(-400 * DAY), ...over });

    it.each([
      { name: "a certificate that expired", keys: [key({ certificate_expires_at: at(-DAY) })], key: "cert:6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10", tone: "danger" },
      { name: "a certificate within 30 days", keys: [key({ certificate_expires_at: at(12 * DAY - 1000) })], key: "cert:6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10", tone: "warn" },
      { name: "a key staged over a day ago", keys: [key({ status: "staged", created_at: at(-2 * DAY), certificate_expires_at: at(700 * DAY) })], key: "staged:6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10", tone: "warn" },
    ])("names $name", ({ keys, key: want, tone }) => {
      const items = attentionItems({ ...QUIET, keys });
      expect(items.map((i) => [i.key, i.tone])).toEqual([[want, tone]]);
    });

    it("stays quiet for a healthy key, a fresh staged key and a retired key", () => {
      const keys = [
        key({ certificate_expires_at: at(31 * DAY) }),
        key({ kid: "3a9e41c2", status: "staged", created_at: at(-3600_000), certificate_expires_at: at(729 * DAY) }),
        key({ kid: "1b7d0a93", status: "retired", created_at: at(-800 * DAY) }),
      ];
      expect(attentionItems({ ...QUIET, keys })).toEqual([]);
      expect(attentionItems({ ...QUIET, keys: null })).toEqual([]);
    });

    it("drops the certificate lines when no provider uses Entra, and keeps the staged line", () => {
      const keys = [
        key({ certificate_expires_at: at(-DAY) }),
        key({ kid: "3a9e41c2", status: "staged", created_at: at(-2 * DAY), certificate_expires_at: at(700 * DAY) }),
      ];
      expect(attentionItems({ ...QUIET, keys, entra: false }).map((i) => i.key)).toEqual(["staged:3a9e41c2"]);
    });

    it("puts an expired certificate with the danger lines and opens the Agent signing keys tab", async () => {
      const expired = key({ certificate_expires_at: "2026-11-01T09:14:00Z" });
      const staged = key({ kid: "3a9e41c2-58d0-4f7b-b1e6-7c2d90a4f513", status: "staged", created_at: at(-2 * DAY), certificate_expires_at: at(700 * DAY) });
      const items = attentionItems({ ...QUIET, apps: APPS, keys: [expired, staged] });
      expect(items.map((i) => i.key)).toEqual(["failed", "cert:" + expired.kid, "degraded", "staged:" + staged.kid]);
      expect(items[1].text).toBe(certExpiredLine(expired.kid, "2026-11-01T09:14:00Z"));
      expect(items[3].text).toBe(stagedWaitingLine(staged.kid, 2));
      mount({ ...QUIET, keys: [expired] });
      await userEvent.click(screen.getByRole("link", { name: new RegExp(KEYS_TAB) }));
      expect(navigate).toHaveBeenCalledWith("settings", ["keys"]);
    });

    it("counts whole days left on an expiring certificate", () => {
      const at12 = at(12 * DAY - 1000);
      const [item] = attentionItems({ ...QUIET, keys: [key({ certificate_expires_at: at12 })] });
      expect(item.text).toBe(certExpiringLine("6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10", 12, at12));
    });
  });
});
