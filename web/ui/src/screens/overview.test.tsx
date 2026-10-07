import { createHash } from "node:crypto";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Overview } from "./overview";
import { TooltipProvider } from "@/components/ui/tooltip";
import {
  type AppRow,
  type ApprovalRow,
  type AuditRow,
  type ConfigAnswer,
  type DecisionBucket,
  type DecisionsBlock,
  type OverviewAnswer,
  type RoleRow,
  type SessionRow,
  type SinkRow,
  getConfig,
  listApprovals,
  listApps,
  listAudit,
  listRoles,
  listSessions,
  listSinks,
  overview,
  replaySink,
} from "@/lib/api";
import {
  ALL_STRICT,
  CHAIN_INTACT,
  CHAIN_SEAT,
  CHANGED_TITLE,
  DECIDED_NONE,
  DECIDED_PANEL,
  DECIDED_UNREAD,
  LAST_HOUR_UNREAD,
  LAST_HOUR_UNSUPPORTED,
  LIVE_OFF,
  LIVE_TITLE,
  NOTHING_TO_DO,
  NOT_READ,
  STOPPED_TITLE,
  STOPPED_UNREAD,
  SUBJECT_DRAFTS,
  SUBJECT_LAST_HOUR,
  TILE,
  TILE_UNREAD,
  chainBrokenDetail,
  chainBrokenLine,
  configRows,
  degradedLine,
  draftsLine,
  failedLine,
  offDetail,
  fleetLine,
  lacksGrant,
  notesLine,
  postureBadge,
  postureOf,
  pushDetail,
  replayWord,
  seatLine,
  serversDetail,
  setupProgress,
  sinkLine,
  strictLine,
  summaryUnread,
  usersDetail,
  waitingLine,
} from "@/lib/config-words";
import { listDrafts } from "@/lib/drafts-api";
import { version as readVersion } from "@/lib/public";
import { SUMMARY } from "@/test/drafts-fixture";
import { readFailed, refused } from "@/lib/say";
import { absTime, relTimeText } from "@/lib/words";

// The Overview screen over seven fixture scenes: the open
// page, a quiet morning, a bad day, day zero, the live tail, the strip
// that is in, and a delegated seat. Every answer is stubbed at the api
// module, and the chain records are linked the way internal/audit.Link
// writes them, so the browser's re-hashing is the real check.

const STRAZAD = "v1.0.0-1137";
const OLD_CLIENT = "v1.0.0-1105";

const ago = (minutes: number) => new Date(Date.now() - minutes * 60000).toISOString();

// chain links seeds into contiguous records, each hashing the one before.
function chain(startSeq: number, seeds: { ce: unknown; username?: string }[]): AuditRow[] {
  let prev = "";
  return seeds.map((s, i) => {
    const ce = JSON.stringify(s.ce);
    const hash = createHash("sha256").update(prev + "\n" + ce).digest("hex");
    const row: AuditRow = { seq: startSeq + i, ce, hash, prevHash: prev, username: s.username };
    prev = hash;
    return row;
  });
}

const admin = (action: string, who: string, minutes: number) => ({ ce: { type: "straza.audit.admin", time: ago(minutes), data: { action } }, username: who });
const call = (tool: string, effect: string, who: string, minutes: number) =>
  ({ ce: { type: "straza.audit.mcp", time: ago(minutes), data: { app: "demo-tools", toolName: tool, effect, ruleId: "dev-guardrails" } }, username: who });

const CHAIN = chain(18426, [
  admin("installed scout-tools", "alice", 240),
  call("get-sum", "approve", "joe", 4),
  call("echo", "allow", "agent-sam", 6),
  call("get-env", "deny", "joe", 11),
  call("get-time", "allow", "agent-sam", 12),
  admin("published dev-guardrails v3", "alice", 120),
]);
const CHAIN_DESC = CHAIN.slice().reverse();

const ADMIN_ROWS = chain(18400, [
  admin("published dev-guardrails v3", "alice", 120),
  admin("gave dev-tools access to scout-tools", "alice", 180),
  admin("installed scout-tools", "alice", 1500),
]).reverse();
const IDENTITY_ROWS = chain(18410, [{ ce: { type: "straza.identity.assignment", time: ago(300), data: { action: "added judy to sec-approvers over SCIM" } }, username: "midpoint" }]);
const POLICY_ROWS = chain(18420, [{ ce: { type: "straza.policy.published", time: ago(4320), data: { action: "raised the gateway hold to 120 s" } }, username: "alice" }]);

const TYPED: Record<string, AuditRow[]> = {
  "straza.audit.admin": ADMIN_ROWS,
  "straza.identity": IDENTITY_ROWS,
  "straza.policy": POLICY_ROWS,
};

// The last 24 hours, one bucket an hour, oldest first.
const HOURS = [12, 8, 6, 4, 3, 5, 9, 22, 48, 71, 84, 90, 77, 69, 88, 93, 81, 64, 52, 40, 31, 24, 18, 15];
const HELD = [0, 0, 0, 0, 0, 0, 0, 1, 2, 1, 3, 2, 1, 0, 2, 3, 1, 1, 0, 1, 0, 0, 0, 0];
const REFUSED = [0, 0, 0, 0, 0, 0, 1, 1, 3, 4, 5, 4, 3, 2, 4, 5, 3, 2, 1, 1, 1, 0, 1, 0];
const sum = (xs: number[]) => xs.reduce((a, c) => a + c, 0);

const DECISIONS: DecisionsBlock = {
  since: "2026-09-12T12:00:00Z",
  buckets: HOURS.map((allowed, i) => ({ start: new Date(Date.parse("2026-09-12T12:00:00Z") + i * 3600000).toISOString(), allowed, approval: HELD[i], denied: REFUSED[i], catalog: i < 2 ? 110 : 0 })),
  stopped: [
    { app: "demo-tools", tool: "get-env", outcome: "denied", count: 23, reason: "dumps the process environment: dev-guardrails, rule no-env" },
    { app: "demo-tools", tool: "get-sum", outcome: "approval", count: 18, reason: "held for sec-approvers" },
    { app: "demo-tools", tool: "read-file", outcome: "denied", count: 9, reason: "a path outside the workspace" },
    { app: "scout-tools", tool: "export", outcome: "denied", count: 5, reason: "no role of the session reaches scout-tools" },
    { app: "demo-tools", tool: "list-files", outcome: "denied", count: 4, reason: "dev-guardrails, rule workspace-only" },
  ],
};

// The last hour, one bucket a minute, oldest first: what a read that asks
// for the hour window adds to the block.
const MINUTES: DecisionBucket[] = Array.from({ length: 60 }, (_, i) => ({
  start: new Date(Date.parse("2026-09-13T11:00:00Z") + i * 60000).toISOString(), allowed: i % 5, approval: i === 40 ? 1 : 0, denied: i === 41 ? 2 : 0, catalog: i === 0 ? 3 : 0,
}));

const NORMAL: OverviewAnswer = {
  profile: "enterprise",
  users: { total: 12, active: 9 },
  sessions: { total: 61, active: 4 },
  apps: { total: 2, running: 2, failed: 0, degraded: 0, pending: 0 },
  policies: { total: 2, active: 1 },
  audit: { head_seq: 18431 },
  denylist: { entries: 1 },
  push: { lane_up: true, connected: 3 },
  decisions: DECISIONS,
};

const EVAL_CONFIG: ConfigAnswer = {
  profile: "enterprise",
  public_url: "http://localhost:8420",
  tls: false,
  store_driver: "postgres",
  events: { embedded: true },
  oidc: { external_issuer: "http://localhost:8080/realms/straza", jit_provision: false },
  governance: { min_attestation: "none", offline_grace_ttl_seconds: 300, local_tool_default: "deny", audit_backpressure: "block" },
  approval: { gateway_hold_seconds: 120, unsigned_own_decisions: false },
  apps: { gitops_dir_enabled: true, upstream_timeout_seconds: 30 },
  capture: { policy_sets: 1, mode: "verbatim", retention_hours: 720, body_store: "inline" },
};

// The strict deployment of the quiet morning: TLS terminated here, the
// floor at managed, no directory watcher and no offline grace.
const STRICT_CONFIG: ConfigAnswer = {
  ...EVAL_CONFIG,
  public_url: "https://straza.example.com",
  tls: true,
  governance: { min_attestation: "managed", offline_grace_ttl_seconds: 0, local_tool_default: "deny", audit_backpressure: "block" },
  apps: { gitops_dir_enabled: false, upstream_timeout_seconds: 30 },
  events: { embedded: false },
};

const SESSIONS: SessionRow[] = [
  { id: "s-joe", user_id: "u-joe", username: "joe", harness: "codex/1.2", client_version: STRAZAD, attestation: "advisory", status: "active", started_at: ago(60), last_seen: ago(4), wiring_status: "allowed" },
  { id: "s-ivan", user_id: "u-ivan", username: "ivan", harness: "gemini/0.9", client_version: STRAZAD, attestation: "managed", status: "active", started_at: ago(90), last_seen: ago(12), wiring_status: "current" },
  { id: "s-sam", user_id: "u-sam", username: "agent-sam", harness: "claude-code/2.0", client_version: OLD_CLIENT, attestation: "none", status: "active", started_at: ago(120), last_seen: ago(31), wiring_status: "unmeasured" },
  { id: "s-alice", user_id: "u-alice", username: "alice", harness: "console/1", attestation: "none", status: "active", started_at: ago(30), last_seen: ago(0) },
];

const WAITING: ApprovalRow[] = [
  {
    id: "ap-1", state: "pending", createdAt: ago(4), expiresAt: ago(-6), user: "u-joe", username: "joe", session: "s-joe",
    rule: "sensitive-tools", set: "dev-guardrails", lane: "mcp", summary: "mcp.call demo-tools:get-sum", justification: "",
    approverRoles: ["sec-approvers"], selfApproval: false, mode: "any", decidedBy: "", decidedByName: "",
  },
];

const APPS: AppRow[] = [
  { id: "a-demo", name: "demo-tools", runtime: "command", status: "running", reached_by: ["dev-tools"] },
  { id: "a-mid", name: "midpoint", runtime: "remote", status: "running", reached_by: ["iga"] },
];

const SICK_APPS: AppRow[] = [
  { id: "a-demo", name: "demo-tools", runtime: "command", status: "running", reached_by: ["dev-tools"] },
  { id: "a-scout", name: "scout-tools", runtime: "remote", status: "failed", reached_by: ["dev-tools"] },
  { id: "a-mid", name: "midpoint", runtime: "remote", status: "degraded", reached_by: ["iga"] },
];

const PARKED: SinkRow = {
  name: "elastic", type: "elasticsearch", target: "http://elastic:9200", batch: 50, subjects: ["straza.audit.>"], parked: 1204,
  streams: [{ stream: "audit", pending: 0, inflight: 0, delivered: 9000, duplicates: 0, parked: 1204, last_error: "Elasticsearch answered 400 to every batch", last_error_at: ago(180) }],
};

const ROLES: RoleRow[] = [
  { id: "r-admin", name: "straza-admin", kind: "straza" },
  { id: "r-dev", name: "dev-tools", kind: "application" },
];

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  overview: vi.fn(),
  getConfig: vi.fn(),
  listApprovals: vi.fn(),
  listAudit: vi.fn(),
  listSinks: vi.fn(),
  listSessions: vi.fn(),
  listApps: vi.fn(),
  listRoles: vi.fn(),
  replaySink: vi.fn(),
}));
vi.mock("@/lib/public", async (orig) => ({ ...(await orig<typeof import("@/lib/public")>()), version: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/drafts-api", async (orig) => ({ ...(await orig<typeof import("@/lib/drafts-api")>()), listDrafts: vi.fn() }));

const failure = (message: string, status: number, unreachable = false) => Object.assign(new Error(message), { status, unreachable });

// typedOf reads the record type a What changed read asks for: the screen
// sends q as the substring "type":"<type>", the way the store matches it.
const typedOf = (p: URLSearchParams): string | null => {
  const needle = p.get("q") || "";
  return needle.startsWith('"type":"') ? needle.slice('"type":"'.length) : null;
};

const serveAudit = (q: string): AuditRow[] => {
  const p = new URLSearchParams(q);
  const type = typedOf(p);
  if (type) return TYPED[type] || [];
  if (p.get("limit") === "25") return CHAIN_DESC;
  return CHAIN_DESC.slice(0, 5);
};

const mount = () => render(<TooltipProvider><Overview /></TooltipProvider>);

const panel = (name: string) => document.querySelector('[data-panel="' + name + '"]') as HTMLElement;
const tile = (key: string) => document.querySelector('[data-tile="' + key + '"]') as HTMLElement;
const tileValue = (key: string) => (tile(key).querySelector("[data-tile-value]") as HTMLElement).textContent;
const tileDetail = (key: string) => (tile(key).querySelector("[data-tile-detail]") as HTMLElement).textContent;
const attention = () => Array.from(panel("attention").querySelectorAll("[data-attention]")).map((r) => (r.querySelector("span:nth-child(2)") as HTMLElement).textContent);
const settled = () => waitFor(() => expect(panel("attention") || panel("setup")).toBeTruthy());
const showTab = (name: string) => userEvent.click(screen.getByRole("tab", { name }));

describe("the Overview screen", () => {
  beforeEach(() => {
    vi.mocked(overview).mockResolvedValue(NORMAL);
    vi.mocked(getConfig).mockResolvedValue(EVAL_CONFIG);
    vi.mocked(listApprovals).mockResolvedValue({ approvals: WAITING });
    vi.mocked(listAudit).mockImplementation((q: string) => Promise.resolve(serveAudit(q)));
    vi.mocked(listSinks).mockResolvedValue([]);
    vi.mocked(listSessions).mockResolvedValue({ items: SESSIONS, next_cursor: "" });
    vi.mocked(listApps).mockResolvedValue(APPS);
    vi.mocked(listRoles).mockResolvedValue(ROLES);
    vi.mocked(replaySink).mockResolvedValue({ sink: "elastic", replayed: 1204, remaining: 0 });
    vi.mocked(readVersion).mockResolvedValue({ version: STRAZAD, commit: "abc1234", go: "go1.25.1", profile: "enterprise" });
    vi.mocked(listDrafts).mockResolvedValue({ items: [], next_cursor: "" });
    window.localStorage.clear();
  });

  it("says how many drafts wait for review after the waiting calls, and drops the line for a seat that may not read drafts", async () => {
    const older = { ...SUMMARY, id: "39", created_at: ago(1500) };
    vi.mocked(listDrafts).mockResolvedValue({ items: [SUMMARY, older], next_cursor: "" });
    mount();
    await settled();
    await waitFor(() => expect(attention()).toEqual([
      waitingLine(1, WAITING[0], relTimeText(WAITING[0].createdAt)),
      draftsLine(2, false, "joe-java-developer-agent", relTimeText(older.created_at)),
    ]));
    expect(listDrafts).toHaveBeenCalledWith("state=open&limit=200");
    document.body.innerHTML = "";
    vi.mocked(listDrafts).mockRejectedValue(failure("this token does not hold drafts:read", 403));
    mount();
    await settled();
    await waitFor(() => expect(listDrafts).toHaveBeenCalledTimes(2));
    expect(attention()).toEqual([waitingLine(1, WAITING[0], relTimeText(WAITING[0].createdAt))]);
  });

  it("keeps the summary compact and preserves complete activity and system details behind tabs", async () => {
    mount();
    await settled();

    // Daily exceptions stay separate from configuration choices.
    const rows = configRows(EVAL_CONFIG);
    expect(attention()).toEqual([
      waitingLine(1, WAITING[0], relTimeText(WAITING[0].createdAt)),
    ]);
    expect((panel("attention").querySelector("[data-attention-count]") as HTMLElement).textContent).toBe("1");
    expect(panel("changed")).toBeNull();
    expect(panel("posture")).toBeNull();
    expect(panel("signed-in")).toBeNull();
    expect(panel("live")).toBeNull();

    // The six tiles, each a door to its area.
    await userEvent.click(screen.getByRole("button", { name: "Review settings" }));
    expect(document.activeElement).toBe(screen.getByRole("tab", { name: "System details" }));
    expect(Array.from(document.querySelectorAll("[data-tile]")).map((t) => t.getAttribute("data-tile")))
      .toEqual(["sessions", "users", "servers", "policies", "approvals", "audit"]);
    expect(tileValue("sessions")).toBe("4of 61");
    expect(tileDetail("sessions")).toBe(pushDetail(true, 3, 1));
    expect(tileValue("users")).toBe("9of 12");
    expect(tileDetail("users")).toBe(usersDetail(12, 9));
    expect(tileDetail("servers")).toBe(serversDetail(NORMAL.apps));
    expect(tileDetail("policies")).toBe(offDetail(2, 1));
    expect(tileValue("approvals")).toBe("1waiting");
    expect(tileValue("audit")).toBe("Verified");
    expect(tileDetail("audit")).toBe(CHAIN_INTACT);

    // The strip: the three totals as labels over 24 bars, and the catalog
    // reads counted apart from the tool calls.
    await showTab("Summary");
    const strip = panel("decisions");
    const total = (word: string) => (strip.querySelector('[data-total="' + word + '"] b') as HTMLElement).textContent;
    expect(total("allowed")).toBe(sum(HOURS).toLocaleString("en-US"));
    expect(strip.querySelector('[data-total="allowed"]')?.textContent).toContain("Tool calls allowed");
    expect(total("needed approval")).toBe(String(sum(HELD)));
    expect(total("denied")).toBe(String(sum(REFUSED)));
    expect(strip.querySelector('[data-total="catalog"]')?.textContent).toBe("220Catalog reads, counted apart");
    expect(strip.querySelectorAll("[data-bucket]").length).toBe(24);
    expect(strip.querySelector("[data-bucket]")?.getAttribute("aria-label"))
      .toBe("12:00 to 13:00 UTC: 12 allowed, 0 needed approval, 0 denied");

    // What was stopped, biggest first, each row a door to Audit.
    expect(Array.from(panel("stopped").querySelectorAll("[data-stopped]")).map((r) => r.getAttribute("data-stopped")))
      .toEqual(["demo-tools / get-env", "demo-tools / get-sum", "demo-tools / read-file", "scout-tools / export", "demo-tools / list-files"]);
    expect(within(panel("stopped")).getByText("needs approval")).toBeTruthy();

    // Posture: the badge, both relaxed rows with their cost, the strict
    // fold and the notes.
    await showTab("System details");
    const posture = panel("posture");
    expect(within(posture).getByText(postureBadge(2))).toBeTruthy();
    expect(Array.from(posture.querySelectorAll("[data-relaxed]")).map((r) => r.getAttribute("data-relaxed"))).toEqual(["tls", "floor"]);
    expect((posture.querySelector("[data-strict-line]") as HTMLElement).textContent).toBe(strictLine(postureOf(rows).strictWords));
    expect((posture.querySelector("[data-notes-line]") as HTMLElement).textContent).toBe(notesLine(postureOf(rows).notes));

    // Who is signed in: the fleet line over the governed rows, the console
    // marked as an admin harness, the older box marked.
    const fleet = panel("signed-in");
    expect((fleet.querySelector("[data-fleet-line]") as HTMLElement).textContent)
      .toBe(fleetLine(3, { allowed: 1, current: 1, unmeasured: 1 }, 1));
    expect(fleet.querySelectorAll("[data-session]").length).toBe(4);
    expect(within(fleet).getByText("admin")).toBeTruthy();
    expect(fleet.querySelectorAll("[data-version-differs]").length).toBe(1);

    // What changed: the newest control-plane records, merged from the
    // three type reads.
    await showTab("Activity");
    expect(document.querySelector("[data-inventory]")).toBeNull();
    expect(panel("decisions")).toBeNull();
    expect(panel("stopped")).toBeNull();
    expect(panel("changed").querySelectorAll("[data-changed]").length).toBe(5);
    expect(within(panel("changed")).getByText("added judy to sec-approvers over SCIM")).toBeTruthy();

    // Live is off until a person turns it on.
    expect(within(panel("live")).getByText(LIVE_OFF)).toBeTruthy();
    expect(vi.mocked(overview)).toHaveBeenCalledTimes(1);
  });

  it("never says nothing needs attention when the drafts could not be read, and names the failed read", async () => {
    vi.mocked(getConfig).mockResolvedValue(STRICT_CONFIG);
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [] });
    vi.mocked(listDrafts).mockRejectedValue(failure("Straza could not read the drafts. Try again, and read the strazad log if it keeps failing.", 500));
    mount();
    await settled();
    await waitFor(() => expect((panel("attention").querySelector("[data-attention-count]") as HTMLElement).textContent).toBe("partial"));
    expect((panel("attention").querySelector("[data-attention-none]") as HTMLElement).textContent).not.toBe(NOTHING_TO_DO);
    const block = document.querySelector("[data-read-errors]") as HTMLElement;
    expect(block.textContent).toContain(readFailed(SUBJECT_DRAFTS, failure("Straza could not read the drafts. Try again, and read the strazad log if it keeps failing.", 500) as never));
  });

  it("says nothing needs attention on a quiet morning, and reads the posture as strict", async () => {
    vi.mocked(getConfig).mockResolvedValue(STRICT_CONFIG);
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [] });
    mount();
    await settled();

    expect((panel("attention").querySelector("[data-attention-none]") as HTMLElement).textContent).toBe(NOTHING_TO_DO);
    expect((panel("attention").querySelector("[data-attention-count]") as HTMLElement).textContent).toBe("none");
    await showTab("System details");
    expect(within(panel("posture")).getByText(postureBadge(0))).toBeTruthy();
    expect(panel("posture").querySelectorAll("[data-relaxed]").length).toBe(0);
    expect((panel("posture").querySelector("[data-strict-line]") as HTMLElement).textContent)
      .toBe(strictLine(postureOf(configRows(STRICT_CONFIG)).strictWords));
    expect(tileDetail("approvals")).toBe("No pending requests");
  });

  it("stacks the bad day at the top, each line in its trust colour with its door", async () => {
    const broken = CHAIN_DESC.map((r, i) => (i === 2 ? { ...r, hash: "0".repeat(64) } : r));
    vi.mocked(listAudit).mockImplementation((q: string) => {
      const p = new URLSearchParams(q);
      const type = typedOf(p);
      if (type) return Promise.resolve(TYPED[type] || []);
      return Promise.resolve(p.get("limit") === "25" ? broken : CHAIN_DESC.slice(0, 5));
    });
    vi.mocked(overview).mockResolvedValue({
      ...NORMAL,
      apps: { total: 3, running: 1, failed: 1, degraded: 1, pending: 0 },
      push: { lane_up: false, connected: 0 },
    });
    vi.mocked(listApps).mockResolvedValue(SICK_APPS);
    vi.mocked(listSinks).mockResolvedValue([PARKED]);
    mount();
    await settled();

    const brokenSeq = broken[2].seq;
    await waitFor(() => expect(attention()[0]).toBe(chainBrokenLine(brokenSeq)));
    expect(attention()).toEqual([
      chainBrokenLine(brokenSeq),
      failedLine(1, ["scout-tools"]),
      degradedLine(1, ["midpoint"]),
      waitingLine(1, WAITING[0], relTimeText(WAITING[0].createdAt)),
      "The push lane is down, so every daemon polls for revocations and approvals every 30 s instead of hearing them at once.",
      sinkLine(PARKED, absTime(PARKED.streams[0].last_error_at).slice(11, 16)),
    ]);
    await showTab("Activity");
    expect(attention()[0]).toBe(chainBrokenLine(brokenSeq));
    await showTab("System details");
    expect(tileDetail("audit")).toBe(chainBrokenDetail(brokenSeq));
    expect(tileDetail("servers")).toBe("1 failed, 1 degraded");
    expect(tileDetail("sessions")).toBe(pushDetail(false, 0, 1));
  });

  it("replaces the attention list with the setup path on day zero, and is honest about the empty panels", async () => {
    vi.mocked(overview).mockResolvedValue({
      users: { total: 1, active: 1 },
      sessions: { total: 1, active: 1 },
      apps: { total: 0, running: 0 },
      policies: { total: 1, active: 1 },
      audit: { head_seq: 41 },
      push: { lane_up: true, connected: 0 },
    });
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [] });
    vi.mocked(listSessions).mockResolvedValue({ items: [SESSIONS[3]], next_cursor: "" });
    vi.mocked(listApps).mockResolvedValue([]);
    vi.mocked(listRoles).mockResolvedValue([ROLES[0]]);
    mount();
    await waitFor(() => expect(panel("setup")).toBeTruthy());

    expect(panel("attention")).toBeNull();
    expect(Array.from(panel("setup").querySelectorAll("[data-setup]")).map((s) => s.getAttribute("data-setup")))
      .toEqual(["server", "role", "policy", "approver", "floor"]);
    await waitFor(() => expect((panel("setup").querySelector("[data-setup-progress]") as HTMLElement).textContent).toBe(setupProgress(1, 5)));
    expect(panel("setup").querySelector('[data-setup="policy"][data-done="true"]')).toBeTruthy();
    expect(panel("setup").querySelector('[data-setup="approver"] [aria-hidden="true"]')?.tagName).toBe("SPAN");
    expect(within(panel("decisions")).getByText(DECIDED_UNREAD)).toBeTruthy();
    expect(within(panel("stopped")).getByText(STOPPED_UNREAD)).toBeTruthy();
    expect(panel("changed")).toBeNull();
  });

  it("retires the setup list once a server, a role and a policy are done", async () => {
    vi.mocked(listSessions).mockResolvedValue({ items: [SESSIONS[3]], next_cursor: "" });
    mount();
    await settled();
    await waitFor(() => expect(panel("setup")).toBeNull());
    expect(panel("attention")).toBeTruthy();
  });

  it("keeps a broken audit chain visible beside first-setup guidance", async () => {
    vi.mocked(overview).mockResolvedValue({ ...NORMAL, apps: { total: 0, running: 0 } });
    vi.mocked(listSessions).mockResolvedValue({ items: [SESSIONS[3]], next_cursor: "" });
    vi.mocked(listApps).mockResolvedValue([]);
    vi.mocked(listRoles).mockResolvedValue([ROLES[0]]);
    const broken = CHAIN_DESC.map((r, i) => i === 2 ? { ...r, hash: "0".repeat(64) } : r);
    vi.mocked(listAudit).mockImplementation(q => Promise.resolve(new URLSearchParams(q).get("limit") === "25" ? broken : serveAudit(q)));
    mount();
    await settled();
    expect(panel("setup")).toBeTruthy();
    expect(attention()[0]).toBe(chainBrokenLine(broken[2].seq));
  });

  it("follows the newest records when the switch is on, and remembers the choice", async () => {
    mount();
    await settled();
    await showTab("Activity");
    expect(panel("live").querySelectorAll("[data-live]").length).toBe(0);

    await userEvent.click(screen.getByRole("switch", { name: "Follow" }));
    await waitFor(() => expect(panel("live").querySelectorAll("[data-live]").length).toBe(5));
    expect(panel("live").querySelector("[data-pulse]")).toBeTruthy();
    expect(within(panel("live")).getAllByText("deny").length).toBe(1);
    expect(window.localStorage.getItem("straza.overview.live")).toBe("on");
    await showTab("System details");
    await showTab("Activity");
    expect(screen.getByRole("switch", { name: "Follow" }).getAttribute("aria-checked")).toBe("true");

    await userEvent.click(screen.getByRole("switch", { name: "Follow" }));
    expect(window.localStorage.getItem("straza.overview.live")).toBe("off");
  });

  it("opens with the switch on when this browser left it on", async () => {
    window.localStorage.setItem("straza.overview.live", "on");
    mount();
    await settled();
    await showTab("Activity");
    await waitFor(() => expect(panel("live").querySelectorAll("[data-live]").length).toBe(5));
    expect((screen.getByRole("switch", { name: "Follow" }) as HTMLElement).getAttribute("aria-checked")).toBe("true");
  });

  it("keeps the decisions strip on the page: the picture is in, not an alternative", async () => {
    mount();
    await settled();
    const strip = panel("decisions");
    expect(within(strip).getByRole("group", { name: "Calls decided per hour over the last 24 hours" })).toBeTruthy();
    expect(strip.querySelectorAll("[data-bucket]")).toHaveLength(24);
    expect(within(strip).queryByText(DECIDED_NONE)).toBeNull();
  });

  it("degrades only the approvals tile and the audit panels for a delegated seat", async () => {
    vi.mocked(listApprovals).mockRejectedValue(failure("this token does not hold approvals:read", 403));
    vi.mocked(listAudit).mockRejectedValue(failure("this token does not hold audit:read", 403));
    mount();
    await settled();

    await showTab("Activity");
    expect(within(panel("changed")).getByText(seatLine(CHANGED_TITLE, "audit:read"))).toBeTruthy();
    await showTab("System details");
    expect(tileValue("approvals")).toBe(NOT_READ);
    expect(tileDetail("approvals")).toBe(lacksGrant("approvals:read"));
    expect(tileDetail("audit")).toBe(CHAIN_SEAT);
    // The tiles that do not need those grants still read, and no line
    // claims a waiting call or a broken chain.
    expect(tileValue("sessions")).toBe("4of 61");
    expect(attention()).toEqual([]);
    expect(document.querySelectorAll("[data-fetch-error]").length).toBe(0);

    await showTab("Activity");
    await userEvent.click(screen.getByRole("switch", { name: "Follow" }));
    await waitFor(() => expect(within(panel("live")).getByText(seatLine(LIVE_TITLE, "audit:read"))).toBeTruthy());
  });

  it("blanks no tile when the overview answer carries only some of its fields", async () => {
    vi.mocked(overview).mockResolvedValue({ sessions: { active: 2 }, audit: { head_seq: 7 } });
    mount();
    await settled();
    expect(within(panel("decisions")).getByText(DECIDED_UNREAD)).toBeTruthy();
    await showTab("System details");
    expect(Array.from(document.querySelectorAll("[data-tile]")).length).toBe(6);
    expect(tileValue("sessions")).toBe("2");
    expect(tileValue("users")).toBe("no answer");
    expect(tileDetail("servers")).toBe(TILE_UNREAD);
    expect(tileValue("audit")).toBe("Verified");
  });

  it("prints the server's own sentence in the row when a replay is refused", async () => {
    vi.mocked(listSinks).mockResolvedValue([PARKED]);
    vi.mocked(replaySink).mockRejectedValue(failure("no such sink: sinks are named in strazad config (sinks[].name)", 404));
    mount();
    await settled();

    await userEvent.click(await screen.findByRole("button", { name: replayWord("elastic") }));
    const row = document.querySelector('[data-replay-refused="elastic"]') as HTMLElement;
    await waitFor(() => expect(row.textContent).toBe(refused(failure("no such sink: sinks are named in strazad config (sinks[].name)", 404))));
  });

  it("keeps the last good data behind the sentence when a read fails", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    const firstRead = new Date("2026-09-21T10:00:00Z");
    vi.setSystemTime(firstRead);
    const interval = vi.spyOn(globalThis, "setInterval");
    const view = mount();
    try {
      await settled();
      await showTab("System details");
      expect(tileValue("users")).toBe("9of 12");
      const poll = interval.mock.calls.find(([, delay]) => delay === 30000)?.[0] as () => void;
      expect(poll).toBeTypeOf("function");
      vi.mocked(overview).mockRejectedValue(failure("unreachable", 0, true));
      vi.setSystemTime(new Date("2026-09-21T10:01:00Z"));
      act(() => poll());
      await waitFor(() => expect(document.querySelector("[data-fetch-error]")?.textContent).toContain("last successful read " + firstRead.toLocaleTimeString([], { hour12: false })));
      expect(tileValue("users")).toBe("9of 12");
      expect(tileDetail("users")).toContain("Last read 1 m ago");
      expect(document.querySelector("[data-read-line]")?.textContent).toContain("Summary last read 1 m ago");

      // A later permission refusal must clear retained counts and their timestamp.
      vi.mocked(overview).mockRejectedValue(failure("no longer permitted", 403));
      act(() => poll());
      await waitFor(() => expect(tileValue("users")).toBe(NOT_READ));
      expect(document.querySelector("[data-read-line]")?.textContent).toBe(summaryUnread(true));
      await showTab("Summary");
      expect(document.querySelector('[data-inventory="users"]')?.textContent).toContain("Permission required");
      expect(document.querySelector('[data-inventory="users"]')?.textContent).not.toContain("9");
      expect(within(panel("decisions")).getByText(seatLine(DECIDED_PANEL, "config:read"))).toBeTruthy();
      expect(within(panel("stopped")).getByText(seatLine(STOPPED_TITLE, "config:read"))).toBeTruthy();
    } finally {
      view.unmount();
      interval.mockRestore();
      vi.useRealTimers();
    }
  });

  it("shows the last hour by the minute, and reads it every 5 s only while it is on screen", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    let minutes = MINUTES;
    vi.mocked(overview).mockImplementation((window?: "hour") => Promise.resolve(window ? { ...NORMAL, decisions: { ...DECISIONS, minutes } } : NORMAL));
    const hourReads = () => vi.mocked(overview).mock.calls.filter(([window]) => window === "hour").length;
    const view = mount();
    try {
      await settled();
      let strip = panel("decisions");
      const total = (word: string) => strip.querySelector('[data-total="' + word + '"]')?.textContent;
      const pressed = () => within(within(strip).getByRole("group", { name: "Window" })).getAllByRole("button").map((b) => b.textContent + ":" + b.getAttribute("aria-pressed"));
      const lastBar = () => strip.querySelector('[data-bucket="59"]')?.getAttribute("aria-label");

      // The panel opens on the day, and nothing asks for the hour.
      expect(pressed()).toEqual(["Last 24 hours:true", "Last hour:false"]);
      expect(strip.querySelectorAll("[data-bucket]")).toHaveLength(24);
      await act(async () => { vi.advanceTimersByTime(5000); });
      expect(hourReads()).toBe(0);

      // Last hour reads at once, and the totals and the chart count the hour.
      await userEvent.click(within(strip).getByRole("button", { name: "Last hour" }));
      await waitFor(() => expect(strip.querySelectorAll("[data-bucket]")).toHaveLength(60));
      expect(hourReads()).toBe(1);
      expect(pressed()).toEqual(["Last 24 hours:false", "Last hour:true"]);
      expect(total("allowed")).toBe("120Tool calls allowed");
      expect(total("denied")).toBe("2Denied");
      expect(total("needed approval")).toBe("1Required approval");
      expect(total("catalog")).toBe("3Catalog reads, counted apart");
      expect(within(strip).getByRole("group", { name: "Calls decided per minute over the last hour" })).toBeTruthy();
      expect(lastBar()).toBe("11:59 to 12:00 UTC, so far: 4 allowed, 0 needed approval, 0 denied");

      // Five seconds on the newest bar has grown. A hidden page skips its read.
      minutes = MINUTES.map((m, i) => (i === 59 ? { ...m, allowed: 9 } : m));
      await act(async () => { vi.advanceTimersByTime(5000); });
      expect(hourReads()).toBe(2);
      await waitFor(() => expect(lastBar()).toBe("11:59 to 12:00 UTC, so far: 9 allowed, 0 needed approval, 0 denied"));
      expect(total("allowed")).toBe("125Tool calls allowed");
      Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
      await act(async () => { vi.advanceTimersByTime(5000); });
      expect(hourReads()).toBe(2);
      delete (document as { hidden?: boolean }).hidden;

      // Another tab takes the panel off screen and the reads wait. Back on
      // Summary the view is still the last hour and it reads at once.
      await showTab("Activity");
      await act(async () => { vi.advanceTimersByTime(5000); });
      expect(hourReads()).toBe(2);
      await showTab("Summary");
      strip = panel("decisions");
      expect(hourReads()).toBe(3);
      expect(pressed()).toEqual(["Last 24 hours:false", "Last hour:true"]);
      expect(strip.querySelectorAll("[data-bucket]")).toHaveLength(60);

      // Back on the day the totals are the day's again and the reads stop.
      await userEvent.click(within(strip).getByRole("button", { name: "Last 24 hours" }));
      expect(strip.querySelectorAll("[data-bucket]")).toHaveLength(24);
      expect(total("allowed")).toBe(sum(HOURS).toLocaleString("en-US") + "Tool calls allowed");
      await act(async () => { vi.advanceTimersByTime(15000); });
      expect(hourReads()).toBe(3);
    } finally {
      delete (document as { hidden?: boolean }).hidden;
      view.unmount();
      vi.useRealTimers();
    }
  });

  it("says in the panel why the last hour has no chart, and keeps the last good minutes behind a failed refresh", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const unreachable = failure("unreachable", 0, true);
    const view = mount();
    try {
      await settled();
      const strip = panel("decisions");
      const hourButton = () => within(strip).getByRole("button", { name: "Last hour" });

      // A server that knows no window answers the day alone.
      await userEvent.click(hourButton());
      expect(await within(strip).findByText(LAST_HOUR_UNSUPPORTED)).toBeTruthy();
      expect(strip.querySelectorAll("[data-bucket]")).toHaveLength(0);

      // A read that fails before any minutes arrived says so in the panel.
      await userEvent.click(within(strip).getByRole("button", { name: "Last 24 hours" }));
      vi.mocked(overview).mockImplementation((window?: "hour") => (window ? Promise.reject(unreachable) : Promise.resolve(NORMAL)));
      await userEvent.click(hourButton());
      expect(await within(strip).findByText(LAST_HOUR_UNREAD)).toBeTruthy();
      expect(document.querySelector("[data-fetch-error]")?.textContent).toContain(readFailed(SUBJECT_LAST_HOUR, unreachable));

      // The next read answers, then one fails: the minutes stay on screen.
      vi.mocked(overview).mockImplementation((window?: "hour") => Promise.resolve(window ? { ...NORMAL, decisions: { ...DECISIONS, minutes: MINUTES } } : NORMAL));
      await act(async () => { vi.advanceTimersByTime(5000); });
      await waitFor(() => expect(strip.querySelectorAll("[data-bucket]")).toHaveLength(60));
      expect(document.querySelector("[data-fetch-error]")).toBeNull();
      vi.mocked(overview).mockImplementation((window?: "hour") => (window ? Promise.reject(unreachable) : Promise.resolve(NORMAL)));
      await act(async () => { vi.advanceTimersByTime(5000); });
      await waitFor(() => expect(document.querySelector("[data-fetch-error]")?.textContent).toContain(readFailed(SUBJECT_LAST_HOUR, unreachable) + " last successful read "));
      expect(strip.querySelectorAll("[data-bucket]")).toHaveLength(60);
      expect(within(strip).queryByText(LAST_HOUR_UNREAD)).toBeNull();
    } finally {
      view.unmount();
      vi.useRealTimers();
    }
  });

  // The audit overview behaviours this screen keeps.
  it("names MCP servers, states the 25-record cap, and leaves the version to the sidebar", async () => {
    mount();
    await settled();
    await showTab("System details");

    expect(within(tile("servers")).getByText(TILE.servers.label)).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/apps running/i);
    expect(TILE.audit.help).toContain("newest 25");
    expect(TILE.audit.help).toContain("strazactl audit verify");
    expect(tileDetail("audit")).toBe(CHAIN_INTACT);
    // The server's own version belongs to the sidebar. The only place it
    // appears here is a box's client build, inside the fleet table.
    expect(document.body.textContent).not.toContain("strazad");
    expect(panel("signed-in").textContent).toContain(STRAZAD);
    const outside = document.body.textContent?.split(panel("signed-in").textContent || "").join("");
    expect(outside).not.toContain(STRAZAD);
  });

  it("gives every tile its detail from the words module, in one grammar", async () => {
    mount();
    await settled();
    await showTab("System details");
    expect(tileDetail("sessions")).toBe(pushDetail(true, 3, 1));
    expect(tileDetail("users")).toBe(usersDetail(12, 9));
    expect(tileDetail("servers")).toBe(serversDetail(NORMAL.apps));
    expect(tileDetail("policies")).toBe(offDetail(2, 1));
    expect(tileDetail("approvals")).toBe("Oldest request: " + relTimeText(WAITING[0].createdAt));
    expect(tileDetail("audit")).toBe(CHAIN_INTACT);
  });

  it("reads the strict line as one sentence when nothing is relaxed and nothing is strict", async () => {
    vi.mocked(getConfig).mockResolvedValue({ ...STRICT_CONFIG, governance: undefined, oidc: { jit_provision: true } });
    mount();
    await settled();
    await showTab("System details");
    expect((panel("posture").querySelector("[data-strict-line]") as HTMLElement).textContent).toMatch(
      new RegExp("^(" + strictLine(postureOf(configRows({ ...STRICT_CONFIG, governance: undefined, oidc: { jit_provision: true } })).strictWords).replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "|" + ALL_STRICT + ")$"),
    );
  });
});
