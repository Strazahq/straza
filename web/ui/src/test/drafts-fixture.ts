// One draft as GET /v1/admin/drafts/{id} answers it,
// holding every finding class and every kind of item the review page
// draws, for the Drafts area's suites. The risk keys are plain labels, not
// hashes, so a client that computed a key instead of echoing it fails.
import type { DraftCoverage, DraftDetail, DraftFinding, DraftGain, DraftSummary, DraftVerdict } from "@/lib/api";

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
    title: "Add server github, 2 roles and 1 policy set and then change server demo-tools",
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
// OWN_REACH_RISK is an agent's draft that widens a role that agent holds:
// the agent.own-reach line typed by the server's name.
export const OWN_REACH_RISK: DraftFinding = {
  code: "agent.own-reach", class: "risk", ack: "typed", typed: "github", object: "App/github", key: "k-own-reach",
  sentence: "This draft lets the agent that wrote it run delete_repo on github with no approval, which widens the agent's own reach.",
  after: "run delete_repo on github with no approval",
};

// ---- three drafts and their checks ----
// A draft that turns off a policy, one that adds a role and a policy for a
// server Straza has not registered, and one that changes a policy that
// applies to its proposer, as a server that sends the gates of each gain
// row, the rows of each access risk, the rule, server and tools of a local
// allow, and binds_proposer.

const SPONSOR_WORDS = "the agent's sponsor";
const draftOf = (id: string, title: string, items: DraftDetail["draft"]["items"]): DraftDetail["draft"] => ({
  id, revision: 1, state: "open", door: "console", authors: [ALICE], items, title,
  created_at: "2026-11-16T09:00:00Z", updated_at: "2026-11-16T09:00:00Z",
});
const verdictOf = (id: string, v: Partial<DraftVerdict>): DraftVerdict => ({
  draft: id, revision: 1, snapshot: "1188e760aa00", checked_at: "2026-11-16T09:01:00Z",
  refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: id.repeat(32).slice(0, 64), ...v,
});
const passed = (lines: string[]): DraftFinding[] => lines.map((sentence, i) => ({ code: "passed." + i, class: "passed", key: "k-pass-" + i, sentence }));

// Draft 91 turns off demo-tools-readers-access, whose two rules gate get-env
// with a ticket and get-sum with a hold for demo-tools-readers and operator.
export const READERS_91 = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: demo-tools-readers-access
spec:
  match:
    roles: [demo-tools-readers, operator]
  rules:
    - id: demo-tools-get-env-approve
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
    - id: demo-tools-get-sum-approve
      tools: [mcp.call]
      apps: [demo-tools]
      toolNames:
        allow: ["get-sum"]
      effect: allow
      mode: approve
      approve:
        deciders: [sponsor]
        timeoutSeconds: 120
        retryTTLSeconds: 120
`;
const ENV_GATE = { rule: "demo-tools-get-env-approve", set: "demo-tools-readers-access", class: "ticket" as const, seconds: 86400, sponsor: true };
const SUM_GATE = { rule: "demo-tools-get-sum-approve", set: "demo-tools-readers-access", class: "hold" as const, seconds: 120, sponsor: true, within_seconds: 120 };
const ENV_WORDS = "a ticket good for 1 day, decided by " + SPONSOR_WORDS;
const SUM_WORDS = "a hold, up to 2 minutes, decided by " + SPONSOR_WORDS + ", retried within 2 minutes";
const gain91 = (role: string, tool: string, count: number): DraftGain => ({
  role, server: "demo-tools", tool, holders_count: count, before: "needs-approval", after: "runs",
  before_words: tool === "get-env" ? ENV_WORDS : SUM_WORDS, before_gate: tool === "get-env" ? ENV_GATE : SUM_GATE,
});
const ungated91 = (role: string): DraftFinding => ({
  code: "access.ungated", class: "risk", ack: "typed", typed: role, group: "access.ungated", object: "Role/" + role, key: "k91-" + role,
  sentence: "get-env on demo-tools will run for holders of " + role + " with no rule gating it, where today " + ENV_WORDS + ". get-sum on demo-tools will run for holders of " + role + " with no rule gating it, where today " + SUM_WORDS + ".",
  before: "demo-tools/get-env=" + ENV_WORDS + "; demo-tools/get-sum=" + SUM_WORDS, after: "demo-tools/get-env; demo-tools/get-sum",
  rows: [{ server: "demo-tools", tool: "get-env" }, { server: "demo-tools", tool: "get-sum" }],
});
export const GUARD_91: DraftFinding = {
  code: "policy.guardrail-changed", class: "risk", ack: "typed", typed: "demo-tools-readers-access", group: "policy.guardrail-changed", object: "PolicySet/demo-tools-readers-access", key: "k91-guard",
  sentence: "demo-tools-readers-access will stop gating mcp.call as rule demo-tools-get-sum-approve does today, because this draft turns it off. demo-tools-readers-access will stop gating mcp.call as rule demo-tools-get-env-approve does today, because this draft turns it off.",
  before: "demo-tools-get-env-approve@39dd4a83dffb; demo-tools-get-sum-approve@47230bc994d2", after: "off",
};
export const DRAFT_91: DraftDetail = {
  draft: draftOf("91", "Turn off policy demo-tools-readers-access", [{ kind: "PolicySet", name: "demo-tools-readers-access", op: "off", doc: READERS_91, existed: true, base: "fp91" }]),
  verdict: verdictOf("91", {
    risks: [GUARD_91, ungated91("demo-tools-readers"), ungated91("operator")],
    passed: passed(["No document holds a secret or the shape of one.", "The policy compiles with this draft."]),
    gains: [gain91("demo-tools-readers", "get-env", 1), gain91("demo-tools-readers", "get-sum", 1), gain91("operator", "get-env", 2), gain91("operator", "get-sum", 2)],
  }),
  revisions: [],
  live: { "PolicySet/demo-tools-readers-access": { op: "put", doc: READERS_91 } },
  may_publish: true,
};

// Draft 86 adds the role ux-walk-audio and a policy whose rule allows 13
// tools of elevenlabs, an MCP server Straza has not registered.
export const ELEVENLABS_TOOLS = [
  "check_subscription", "list_models", "search_voices", "search_voice_library", "get_voice", "create_composition_plan", "play_audio",
  "text_to_sound_effects", "compose_music", "text_to_speech", "text_to_voice", "create_voice_from_preview", "isolate_audio",
];
const AUDIO_86 = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: ux-walk-audio-access
spec:
  match:
    roles: [ux-walk-audio]
  rules:
    - id: elevenlabs-audio-allow
      tools: [mcp.call]
      apps: [elevenlabs]
      toolNames:
        allow: [${ELEVENLABS_TOOLS.join(", ")}]
      effect: allow
`;
const AUDIO_ROLE_86 = "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n  name: ux-walk-audio\nspec:\n  kind: application\n";
export const LOCAL_86: DraftFinding = {
  code: "policy.local-allow", class: "risk", ack: "tick", group: "policy.local-allow", object: "PolicySet/ux-walk-audio-access", key: "k86-local",
  sentence: "Rule elevenlabs-audio-allow of ux-walk-audio-access allows " + ELEVENLABS_TOOLS.slice(0, -1).join(", ") + " and isolate_audio of elevenlabs, an MCP server Straza has not registered, whose tools are denied until a rule allows them.",
  after: "elevenlabs-audio-allow: elevenlabs", rule: "elevenlabs-audio-allow", server: "elevenlabs", tools: ELEVENLABS_TOOLS,
};
export const NAMES_86: DraftFinding = {
  code: "ready.rule-names-nothing", class: "warning", object: "PolicySet/ux-walk-audio-access", key: "k86-names",
  sentence: "Rule elevenlabs-audio-allow of ux-walk-audio-access names elevenlabs, which is not a registered server, so it matches only an MCP server that a person's own harness runs under that name.",
  fix: "If you meant a registered server, fix the name.", rule: "elevenlabs-audio-allow", server: "elevenlabs", tools: ELEVENLABS_TOOLS,
};
export const DRAFT_86: DraftDetail = {
  draft: draftOf("86", "Add role ux-walk-audio and 1 policy", [
    { kind: "Role", name: "ux-walk-audio", op: "put", doc: AUDIO_ROLE_86, existed: false },
    { kind: "PolicySet", name: "ux-walk-audio-access", op: "put", doc: AUDIO_86, existed: false },
  ]),
  verdict: verdictOf("86", {
    risks: [LOCAL_86],
    warnings: [NAMES_86],
    passed: passed(["No document holds a secret or the shape of one.", "The policy compiles with this draft."]),
    info: [{ code: "info.nobody-holds", class: "info", object: "Role/ux-walk-audio", key: "k86-nobody", sentence: "Nobody holds ux-walk-audio yet, so it reaches nothing until someone is assigned it." }],
  }),
  revisions: [],
  live: {},
  may_publish: true,
};

// Draft 87 changes agent-guardrails, which applies to alice, its proposer:
// deploy-ticket gains a pattern and no-rm-rf goes from denied to a hold.
const guardrails87 = (draft: boolean) => `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: agent-guardrails
spec:
  match:
    roles: [demo-tools-sandbox, midpoint-self-service, midpoint-operations, demo-tools-readers]
  capture:
    conversations: true
    mode: verbatim
  rules:
    - id: deploy-ticket
      tools: [shell.exec]
      command:
        allowPatterns: ["./deploy*", "make deploy*"${draft ? ', "kubectl apply*"' : ""}]
      effect: allow
      mode: approve
      approve:
        class: ticket
        deciders: [sponsor]
        roles: [sec-approvers]
        ticketTTLSeconds: 86400
        grantTTLSeconds: 3600
    - id: no-rm-rf
      tools: [shell.exec]
      command:
${draft ? '        allowPatterns: ["rm -rf *"]\n      effect: allow\n      mode: approve\n      approve:\n        timeoutSeconds: 120' : '        denyPatterns: ["rm -rf *"]\n      effect: deny'}
      reason: "Straza: destructive commands need a person"
`;
export const GUARD_87: DraftFinding = {
  code: "policy.guardrail-changed", class: "risk", ack: "typed", typed: "agent-guardrails", group: "policy.guardrail-changed", object: "PolicySet/agent-guardrails", key: "k87-guard",
  sentence: "agent-guardrails will stop gating shell.exec as rule deploy-ticket does today, because this draft changes the rule. agent-guardrails will stop denying shell.exec as rule no-rm-rf does today, because this draft changes the rule.",
  before: "deploy-ticket@ccbf1adb51a1; no-rm-rf@21081ff3e4a2", after: "deploy-ticket@7463f095f50a; no-rm-rf@8d4f605ef67d", binds_proposer: true,
};
export const DRAFT_87: DraftDetail = {
  draft: draftOf("87", "Change policy agent-guardrails", [{ kind: "PolicySet", name: "agent-guardrails", op: "put", doc: guardrails87(true), existed: true, base: "fp87" }]),
  verdict: verdictOf("87", {
    risks: [GUARD_87],
    passed: passed(["No document holds a secret or the shape of one.", "Every approver role the draft's rules name exists and may decide.", "The policy compiles with this draft."]),
  }),
  revisions: [],
  live: { "PolicySet/agent-guardrails": { op: "put", doc: guardrails87(false) } },
  may_publish: true,
};

// oldServer is a draft as a server too old to send the gates, the rows,
// the rule, server and tools, and binds_proposer: the same lines and rows
// with none of those fields.
export function oldServer(d: DraftDetail): DraftDetail {
  const line = ({ rows: _rows, rule: _rule, server: _server, tools: _tools, binds_proposer: _binds, ...rest }: DraftFinding): DraftFinding => rest;
  const gain = ({ before_gate: _b, after_gate: _a, ...rest }: DraftGain): DraftGain => rest;
  const v = d.verdict;
  return {
    ...structuredClone(d),
    verdict: {
      ...structuredClone(v),
      risks: (v.risks || []).map(line), warnings: (v.warnings || []).map(line), info: (v.info || []).map(line), gains: (v.gains || []).map(gain),
    },
  };
}

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
