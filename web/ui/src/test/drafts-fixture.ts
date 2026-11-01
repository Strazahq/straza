// One draft as GET /v1/admin/drafts/{id} answers it,
// holding every finding class and every kind of item the review page
// draws, for the Drafts area's suites. The risk keys are plain labels, not
// hashes, so a client that computed a key instead of echoing it fails.
import type { DraftCoverage, DraftDetail, DraftFinding, DraftSummary, DraftVerdict } from "@/lib/api";

export const GITHUB_APP = `apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: github
  description: GitHub's remote MCP server
straza:
  runtime:
    kind: remote
    remote:
      url: https://api.githubcopilot.com/mcp/
  credential:
    kind: token
    agents: sponsor
    inject:
      as: header
      name: Authorization
      template: "Bearer {{secret}}"
`;

export const demoTools = (rps: number) => `apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: demo-tools
straza:
  runtime:
    kind: remote
    remote:
      url: http://demo-tools:3001/mcp
  limits:
    rps: ${rps}
`;

export const OLD_TOOLS = `apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: old-tools
straza:
  runtime:
    kind: command
    command:
      exec: /usr/local/bin/old-tools
`;

export const role = (name: string, server: string, tools: string[]) => `apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: ${name}
spec:
    kind: application
    server: ${server}
    bindings:
        - app: ${server}
          tools:
${tools.map((t) => "            - " + t).join("\n")}
`;

export const WRITERS_SET = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: github-writers-access
spec:
  priority: 100
  match:
    roles: [github-writers]
  rules:
    - id: github-pr-hold
      tools: [mcp.call]
      apps: [github]
      toolNames:
        allow: ["create_pull_request"]
      effect: allow
      mode: approve
      approve:
        roles: [sec-approvers]
        timeoutSeconds: 600
`;

export const readersSet = (live: boolean) => `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: demo-tools-readers-access
spec:
  match:
    roles: [demo-tools-readers]
  rules:
${live ? `    - id: get-env
      tools: [mcp.call]
      apps: [demo-tools]
      toolNames:
        allow: ["get-env"]
      effect: allow
      mode: approve
      approve:
        deciders: [sponsor]
        class: ticket
        ticketTTLSeconds: 86400
        grantTTLSeconds: 3600
` : ""}    - id: get-sum
      tools: [mcp.call]
      apps: [demo-tools]
      toolNames:
        allow: ["get-sum"]
      effect: allow
      mode: approve
      approve:
${live ? "        deciders: [sponsor]\n        timeoutSeconds: 120" : "        roles: [sec-approvers]\n        timeoutSeconds: 300"}
`;

export const HOST_RISK: DraftFinding = {
  code: "host.new", class: "risk", ack: "typed", object: "App/github", key: "k-host", typed: "api.githubcopilot.com",
  sentence: "Straza sends each person's GitHub token to api.githubcopilot.com, a host no server uses today.",
};
export const REACH_RISK: DraftFinding = {
  code: "reach.new", class: "risk", ack: "tick", object: "Role/github-readers", key: "k-reach",
  sentence: "github-readers and github-writers reach 6 tools on github that no role reaches today.",
};
// CUT_RISK is a typed risk whose words verdictFor cut for this reader: it
// keeps its code, class, ack and key, and loses its typed text.
export const CUT_RISK: DraftFinding = {
  code: "host.changed", class: "risk", ack: "typed", key: "k-cut",
  sentence: "This line names a tool on a server you cannot read, so this view leaves its words out. Ask an administrator for the scope apps:read to see it.",
};
export const STALE: DraftFinding = {
  code: "draft.stale", class: "refused", object: "App/demo-tools", key: "k-stale",
  sentence: "App/demo-tools changed after this draft was checked, when dave published draft 44 at 2026-09-24 11:24 UTC.",
  fix: "Check the draft again with Check again on the console or strazactl drafts rebase 43. Straza keeps what this draft changed, takes every other field from live state, and asks you to pick where both changed.",
};

export const VERDICT: DraftVerdict = {
  draft: "41", revision: 2, snapshot: "7f3a91c2d4e5", checked_at: "2026-09-24T10:44:00Z",
  refused: [],
  risks: [HOST_RISK, REACH_RISK],
  warnings: [
    { code: "ready.nobody-holds", class: "warning", object: "Role/github-readers", key: "k-w1", sentence: "Nobody holds github-readers or github-writers.", fix: "Assign them after publishing, or let the identity manager do it." },
    { code: "narrow.role-loses", class: "warning", object: "Role/demo-tools-readers", key: "k-w2", sentence: "demo-tools-readers loses get-env.", before: "reached", after: "leaves the role" },
  ],
  unchecked: [
    { code: "unchecked.tools", class: "unchecked", object: "App/github", key: "k-u1", sentence: "Straza has not contacted api.githubcopilot.com, because an address in a draft is contacted only when a person asks, so the tool names of github are unknown." },
  ],
  passed: [{ code: "names.free", class: "passed", key: "k-p1", sentence: "The names github, github-readers and github-writers-access are free." }],
  info: [{ code: "info.nobody-holds", class: "info", object: "Role/github-readers", key: "k-i1", sentence: "Nobody holds github-readers yet, so it reaches nothing until someone is assigned it." }],
  gains: [
    { role: "github-readers", server: "github", tool: "get_me", holders_count: 0, holders: [], before: "not-reachable", after: "runs" },
    { role: "github-writers", server: "github", tool: "create_pull_request", holders_count: 0, before: "not-reachable", after: "needs-approval", after_words: "a hold, up to 10 minutes, decided by sec-approvers" },
    { role: "demo-tools-readers", server: "demo-tools", tool: "echo", holders_count: 1, holders: ["sam-sre-agent"], before: "runs", after: "runs" },
    { role: "demo-tools-readers", server: "demo-tools", tool: "get-env", holders_count: 1, holders: ["sam-sre-agent"], before: "needs-approval", after: "not-reachable", before_words: "a ticket, the person behind the agent" },
  ],
  needs: [
    { object: "App/github", standing: "the scope apps:write or the role straza-global-mcp-admin" },
    { object: "PolicySet/github-writers-access", standing: "the scope policy:write" },
  ],
  risk_digest: "d".repeat(64),
};

const JOE = { user_id: "u-joe", username: "joe-java-developer-agent", agent: true, via: "session" as const, client: "claude-code", sponsor_id: "u-alice", sponsor: "alice" };
export const ALICE = { user_id: "u-alice", username: "alice", agent: false, via: "session" as const, client: "console" };

export const DETAIL: DraftDetail = {
  draft: {
    id: "41", revision: 2, state: "open", door: "straza-app",
    note: "The platform team asked for read access to GitHub. <b>bold</b> https://evil.example",
    authors: [JOE],
    items: [
      { kind: "App", name: "github", op: "put", doc: GITHUB_APP, existed: false },
      { kind: "App", name: "demo-tools", op: "put", doc: demoTools(20), existed: true, base: "fp1" },
      { kind: "Role", name: "github-readers", op: "put", doc: role("github-readers", "github", ["get_me", "list_issues"]), existed: false },
      { kind: "Role", name: "demo-tools-readers", op: "put", doc: role("demo-tools-readers", "demo-tools", ["echo", "get-sum"]), existed: true, base: "fp2" },
      { kind: "PolicySet", name: "github-writers-access", op: "put", doc: WRITERS_SET, existed: false },
      { kind: "PolicySet", name: "demo-tools-readers-access", op: "put", doc: readersSet(false), existed: true, base: "fp3" },
      { kind: "App", name: "old-tools", op: "remove", existed: true, base: "fp4" },
      { kind: "Role", name: "secret-role", op: "put", existed: true, withheld: "Straza leaves out the document of Role/secret-role, because reading a role needs the scope identity:read. Ask an administrator for that grant." },
    ],
    title: "Add server github, 2 roles and 1 approval set and then change server demo-tools",
    created_at: "2026-09-24T10:40:00Z", updated_at: "2026-09-24T10:42:00Z", expires_at: "2026-10-08T10:42:00Z",
  },
  verdict: VERDICT,
  revisions: [
    { revision: 1, author: JOE, door: "straza-app", digest: "a1", created_at: "2026-09-24T10:40:00Z" },
    { revision: 2, author: JOE, door: "straza-app", digest: "a2", created_at: "2026-09-24T10:42:00Z" },
  ],
  live: {
    "App/github": { op: "remove", doc: "" },
    "App/demo-tools": { op: "put", doc: demoTools(10) },
    "Role/github-readers": { op: "remove", doc: "" },
    "Role/demo-tools-readers": { op: "put", doc: role("demo-tools-readers", "demo-tools", ["echo", "get-env", "get-sum"]) },
    "PolicySet/github-writers-access": { op: "remove", doc: "" },
    "PolicySet/demo-tools-readers-access": { op: "put", doc: readersSet(true) },
    "App/old-tools": { op: "put", doc: OLD_TOOLS },
    "Role/old-tools-readers": { op: "put", doc: role("old-tools-readers", "old-tools", ["run"]) },
  },
  may_publish: true,
};

// detailWith copies the fixture with the fields named replaced, the draft
// and the verdict merged one level deep.
export function detailWith(patch: { draft?: Partial<DraftDetail["draft"]>; verdict?: Partial<DraftVerdict> } & Partial<Omit<DraftDetail, "draft" | "verdict">>): DraftDetail {
  const { draft, verdict, ...rest } = patch;
  return { ...structuredClone(DETAIL), ...rest, draft: { ...structuredClone(DETAIL.draft), ...draft }, verdict: { ...structuredClone(VERDICT), ...verdict } };
}

// The tracker server pins an invented object type, ticket and tickets, so a
// view that hardcodes a noun fails. trackerRole is a role file on it with
// a covers line.
export const trackerRole = (name: string, tools: string[], tickets: string[]) => role(name, "tracker", tools) + "          covers:\n            objects: [" + tickets.join(", ") + "]\n";
const ticket = (n: number) => ({ id: "t" + n, value: "T-" + n });

// COVERAGE is tracker-triage, held by one person, widened from T-1 to T-1,
// T-2 and T-3 with the same tools: the case no gain row shows.
export const COVERAGE: DraftCoverage = {
  role: "tracker-triage", server: "tracker", type: "ticket", plural: "tickets", holders_count: 1,
  every_before: false, every_after: false, before: [ticket(1)], after: [ticket(1), ticket(2), ticket(3)],
};
export const COVERS_RISK: DraftFinding = {
  code: "role.covers-wider", class: "risk", ack: "tick", group: "role.covers-wider", object: "Role/tracker-triage", key: "k-covers",
  sentence: "tracker-triage will reach more tickets on tracker: T-2 and T-3.", before: "T-1", after: "T-1, T-2, T-3",
};
const TRIAGE_TOOLS = ["ticket_comment", "ticket_read", "ticket_search"];

// COVERS_DETAIL is draft 50, which widens tracker-triage's covers line and
// changes nothing else, so the verdict holds no gain row.
export const COVERS_DETAIL: DraftDetail = {
  ...DETAIL,
  draft: {
    ...DETAIL.draft, id: "50", door: "console", note: "", authors: [ALICE], title: "Change role tracker-triage",
    items: [{ kind: "Role", name: "tracker-triage", op: "put", doc: trackerRole("tracker-triage", TRIAGE_TOOLS, ["T-1", "T-2", "T-3"]), existed: true, base: "fp9" }],
  },
  verdict: { ...VERDICT, draft: "50", risks: [COVERS_RISK], warnings: [], unchecked: [], info: [], passed: [], gains: [], objects: [COVERAGE], needs: [] },
  live: { "Role/tracker-triage": { op: "put", doc: trackerRole("tracker-triage", TRIAGE_TOOLS, ["T-1"]) } },
  revisions: [],
};

// LOOSEN_RISK and GATE_LOST are an agent's draft that turns off a set that
// binds that agent: the agent.guardrail line typed by the set's name, and
// the line of the same set that says which gate stops applying.
export const LOOSEN_RISK: DraftFinding = {
  code: "agent.guardrail", class: "risk", ack: "typed", typed: "agent-guardrails", object: "PolicySet/agent-guardrails", key: "k-loosen",
  sentence: "agent-guardrails binds the agent that wrote this draft, and this draft turns it off.",
};
export const GATE_LOST: DraftFinding = {
  code: "policy.guardrail-changed", class: "risk", ack: "typed", typed: "agent-guardrails", object: "PolicySet/agent-guardrails", key: "k-gate",
  sentence: "agent-guardrails stops asking for a ticket before deploy runs, because this draft turns it off.", before: "rule deploy-ticket", after: "off",
};

// SUMMARY is draft 41 as a row of the queue.
export const SUMMARY: DraftSummary = {
  id: "41", title: DETAIL.draft.title, state: "open", door: "straza-app", revision: 2, proposer: JOE,
  items: [{ kind: "App", name: "github", op: "put" }],
  checks: { refused: 0, risks: 3, warnings: 0, unchecked: 1, revision: 2, checked_at: "2026-09-24T10:44:00Z" },
  created_at: new Date(Date.now() - 180000).toISOString(), updated_at: new Date(Date.now() - 180000).toISOString(),
};

// PUBLISHED_DETAIL is a published draft whose change record says what the
// publish did: demo-tools from 10 to 20 calls per second, the role
// old-readers removed with its set taken along, and the readers set with
// get-sum edited and get-env removed. Live state has moved on since (rps
// 30), so a card that read live instead of the record would show the wrong
// before and after.
export const PUBLISHED_DETAIL: DraftDetail = {
  ...DETAIL,
  draft: {
    ...DETAIL.draft,
    id: "40", state: "published", decided_at: "2026-09-24T10:51:00Z", decided_by: ALICE, snapshot: "3be0a1ff00",
    items: [
      { kind: "App", name: "demo-tools", op: "put", doc: demoTools(20), existed: true },
      { kind: "Role", name: "old-readers", op: "remove", existed: true },
      { kind: "PolicySet", name: "demo-tools-readers-access", op: "put", doc: readersSet(false), existed: true },
    ],
  },
  live: {
    "App/demo-tools": { op: "put", doc: demoTools(30) },
    "Role/old-readers": { op: "remove", doc: "" },
    "PolicySet/demo-tools-readers-access": { op: "put", doc: readersSet(false) },
  },
  changes: [
    { kind: "App", name: "demo-tools", implied: false, before_op: "put", before_doc: demoTools(10), before_fp: "a", after_op: "put", after_doc: demoTools(20), after_fp: "b" },
    { kind: "Role", name: "old-readers", implied: false, before_op: "put", before_doc: role("old-readers", "demo-tools", ["echo"]), before_fp: "c", after_op: "remove", after_doc: "", after_fp: "" },
    { kind: "PolicySet", name: "old-readers-access", implied: true, before_op: "put", before_doc: WRITERS_SET, before_fp: "d", after_op: "remove", after_doc: "", after_fp: "" },
    { kind: "PolicySet", name: "demo-tools-readers-access", implied: false, before_op: "put", before_doc: readersSet(true), before_fp: "e", after_op: "put", after_doc: readersSet(false), after_fp: "f" },
  ],
  may_publish: false,
};
