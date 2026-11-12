# Agent Governance Hook Profile, v1beta1

Status: **beta**. The profile began as v1alpha1, and the move to beta
changed no wire format. Schema: `canonical-event.schema.json`. Per-harness mapping
tables live in `mappings/`: Claude Code, Codex, Gemini (one table per
harness version: `antigravity` legacy camelCase, `v0.50` shipping
snake_case), and `python-sdk`, the shape straza-agentkit itself emits
from Python agents.
Conformance fixtures: `spec/conformance/hooks/<harness>/<version>/`.

Revision (2026-11-11): **the known interpreter set gains `eval`**. `eval`
is the shell builtin that runs its arguments as code, so a command that
starts with it hides its payload from a command pattern the way `bash -c`
does. The detector of section 1.1 now tags it, and the `interpreter`
attribute of such a `shell.exec` event is `eval`. A policy that denies or
classifies by `interpreters` reaches these commands from this revision on.
A PEP built before it leaves the attribute absent, and a server at this
revision computes it when the client sent none. Additive: no schema
change, existing fixtures unchanged, and one new fixture pins the tag.

Revision (2026-11-11): **a classified tool's input travels beside the
event**. The PEP that forwards a classified local tool for a decision sends
the harness `tool_input` beside the event as `toolInput` on the decide
request, as it sends `nativeTool`, so the `straza.audit.tool` record can
keep it (spec/events revision 65). `toolInput` is an object of `text`, the
`tool_input` as JSON text, shortened to 8 KiB the way that revision
shortens `arguments`, and of `hash`, `sha256:` followed by the hex SHA-256
of the whole text, present only when `text` was shortened. The client
shortens it, because a written file can be longer than the decide request
a server reads. The canonical event of a
classified tool is unchanged and still carries no `args`: the approval
fingerprint binds `args`, and a ticket opened for a described command is
keyed on the command alone, so a real call whose key also bound the rest
of its input could never use that ticket. No rule reads `toolInput` and no
fingerprint binds it. An implementation that sends none loses the input on
the record and nothing else. Additive: no schema change, existing fixtures
unchanged, and one new fixture pins the canonical event of a Claude Code
`Skill` call, which carries neither a command nor a path.

Revision (2026-11-10): **an unclassified tool carries its input**. An
`other` event MAY carry the `args` attribute of section 1.1a, the harness
`tool_input` object verbatim, as an `mcp.call` event does. Before this
revision an implementation dropped the input of a tool it could not
classify, so the event and its audit record named the tool and nothing it
was asked to do. A classified local tool still carries no `args`: its
`command` or `paths` describe the call, and its input can hold the content
of a file. The approval fingerprint of a hold on an `other` tool binds the
forwarded arguments, since every kind but `mcp.call` binds its concrete
call (spec/policyset revision 7), and the `straza.audit.tool` record of an
`other` tool keeps them (spec/events revision 64). An implementation that
forwards none keeps the fingerprint it had. The python-sdk dialect sends
the call's own arguments as `tool_input` for an `other` tool. Additive: no
schema change beyond the description of `args`. The expected event of the
three fixtures whose payload already carried a `tool_input` for an
unclassified tool gains `args`, and one fixture pins a Claude Code `LSP`
call.

Revision (2026-10-30): **an unclassified tool carries its own name**, and
the claude-code mapping table gains two built-ins that fell to `other`.
`SubagentHandback`, which returns a subagent's report to the agent that
spawned it, rides `task.spawn`, and `ArtifactCheck`, which renders a page
for review, rides `net.fetch`. An `other` event now MUST carry the
harness's own name of the tool in `toolName`, which the schema already
described, and an implementation drops a name longer than 128 bytes or one
that holds a control or format character, so the tool stays nameless and
denied. A classified local tool keeps its canonical event unchanged, so
events stay identical across dialects, and the PEP that forwards it for a
decision sends the harness's own name beside the event as `nativeTool` on
the decide request, where spec/policyset revision 26 by-name rules read
it. The python-sdk dialect gains the field `nativeTool: native_tool_name`,
because the kit sends a canonical kind as `tool_name`. Additive: no schema
change, existing fixtures unchanged, and new fixtures pin both tools, an
unclassified tool on claude-code and codex, and the kit's named tools.

Revision (2026-10-13): claude-code mapping table gains six built-ins of the
current harness that fell to `other`: `ListAgents` rides `task.spawn` beside
`SendMessage`, `ReadNotifications` and `FetchInboxMessage` read session
state and ride `file.read`, and `SendFeedback`, `ArtifactComments` and
`ArtifactData` send data beyond the machine and ride `net.fetch` beside
`Artifact`. Additive: no schema change, existing fixtures unchanged, six
fixtures added.

Revision (2026-09-07): decision semantics text only (no wire-format change).
The `obligations` list on a decision is always empty since policyset
revision 18: the `redact` and `notify` obligations were retired, never
having run on any lane. Clients ignore the list.

Revision (2026-07-31): acknowledgement contract corrected from LIVE harness
behavior (no wire-format change; the correction is what implementations must
EMIT, pinned by the extended Tier-1 suite): on non-enforceable events
claude-code accepts ONLY empty stdout (its per-event output schema union has
no variant for them: a decision document is rejected with "Hook JSON output
validation failed", claude-code 2.1.220), and codex accepts ONLY empty
stdout on EVERY success, enforceable included (codex 0.146.0 rejects even a
well-formed `permissionDecision:"allow"` as unsupported without
`updatedInput`); a codex block is exit code 2 with the reason on stderr,
and codex ignores stdout on exit 2, so a stdout-only deny FAILS OPEN there.
Tier-1 suite extended accordingly (`want.silent`, per-event cases for
UserPromptSubmit / SessionEnd / PermissionRequest, the python-sdk dialect,
and a required per-case `provenance` tag); both conformance corpora now
refuse an untagged case, and `spec/conformance/hooks/codex/0.146/` is the
first corpus directory minted from live sentinel captures rather than
hand-authored (test/harness-matrix/recordfixtures).

Revision (2026-07-26): codex mapping table extended in place (additive vendor
change, verified against openai/codex generated JSON schemas): +`SessionEnd`
(root-only: never fires for subagents; close or 30 min idle),
+`SubagentStart`/`SubagentStop` → the existing canonical `subagent.start`/
`subagent.stop`. Pinned divergence: codex's `transcript_path` is the CHILD's
rollout at SubagentStart and the PARENT's at SubagentStop (child's moves to
`agent_transcript_path`), while claude-code carries the parent's on both.
Consumers must resolve the child path per-dialect. Also: `task.spawn` gains
per-tool input extraction in the claude-code and python-sdk adapters (the
delegation target rides `command`), making delegation-target rules matchable;
canonical event shapes unchanged. Fixtures: codex session-end/subagent-start/
subagent-stop, python-sdk tool-pre-task-spawn.

Revision (2026-07-21): the `mcp__straza__` gateway prefix, the `straza`
registration key and the `straza.dev` id replace the pre-release names;
canonical event shapes and mappings unchanged.

Revision (2026-07-17): OPTIONAL `interpreter` attribute on `shell.exec`
events (§1.1; https://docs.straza.ai/guides/write-policy/classify/). Additive
and optional: events without it stay valid, existing fixtures are unchanged.

Revision (2026-07-20): claude-code mapping table extended to the modern
built-in inventory (an unmapped built-in falls to `other`, which the
enterprise profile default-denies: a harness update must not lock
governed sessions out), and NORMATIVE gateway re-attribution for proxied MCP
names (§1.2). Additive: no schema change, existing fixtures unchanged.

## 1. Model

Coding-agent harnesses invoke hooks the same way: spawn a command, JSON on
stdin, decision on stdout/exit code. This profile defines the **canonical
event** every dialect normalizes into, the **tool taxonomy**, and the
**decision semantics**, so one enforcement engine (Tier 1 PEP) serves every
harness.

### Events

`session.start`, `prompt.submit`, `tool.pre`, `tool.post`,
`permission.request`, `subagent.start`, `subagent.stop`, `session.end`,
`compact.pre`.

Only `tool.pre` and `permission.request` may block. All other events are
observational (audit) or context-injection points (`session.start`).

### Tool taxonomy

| Canonical | Meaning | Claude Code examples |
|---|---|---|
| `shell.exec` | run a command; lane companions (read/kill a background shell) ride along | `Bash`, `PowerShell`, `BashOutput`, `KillShell` |
| `file.read` | read file/session/catalog state; pure-UI built-ins ride here pending a dedicated meta kind | `Read`, `Grep`, `Glob`, `ToolSearch`, `AskUserQuestion` |
| `file.write` | create/overwrite a file (incl. worktree create/remove) | `Write`, `EnterWorktree` |
| `file.edit` | modify a file in place | `Edit`, `NotebookEdit` |
| `net.fetch` | outbound network egress | `WebFetch`, `WebSearch`, `Artifact`, `PushNotification` |
| `mcp.call` | MCP tool call; carries `app`, `toolName`, `args` | `mcp__<app>__<tool>` |
| `task.spawn` | spawn/manage a subagent, task, or scheduled agent run | `Task`, `Agent`, `Workflow`, `TaskStop`, `CronCreate` |
| `other` | anything unmapped; carries the harness's own name in `toolName` and its input in `args` | none |

The full per-harness assignment lives in the mapping tables (`mappings/`).
Assignment rule: the kind is the tool's side effect outside the
conversation; a lane's lifecycle companions ride the lane's kind so one
grant keeps the lane coherent. Known pure-conversation/UI built-ins
(`AskUserQuestion`, `TodoWrite`, plan-mode switches, `ToolSearch`, …)
classify with `file.read` (they read or present session state and must keep
working under an enterprise profile) until the taxonomy grows a dedicated
meta kind, which needs an enum revision of this schema and of
spec/policyset. `other` remains reserved for the UNKNOWN: implementations
MUST NOT map a recognized built-in to `other`, so deny-what-you-can't-
classify keeps meaning exactly that.

#### 1.1 The `interpreter` attribute (revision 2026-07-17)

`shell.exec` events MAY carry an `interpreter` attribute naming the
interpreter the command invokes (`python3`, `bash`, …) so policy can match
interpreter indirection without inspecting script content (consumed by
the policyset `interpreters` matcher). Both PEPs (straza
normalization and strazad `POST /v1/decide`) MUST compute it with the SAME
shared detector, so client- and server-side tagging cannot drift:

- Input is `argv` when present; otherwise the raw `command` string split by
  the same POSIX-ish word splitter the command matcher uses (`'`, `"`, `\`).
- Leading `VAR=val` assignments and the wrapper launchers `env`, `sudo`,
  `nohup`, `nice` (plus each wrapper's own `-flags`) are skipped.
- The value is the lowercased, `.exe`-trimmed basename of the first
  non-wrapper token WHEN it is a known interpreter; otherwise the attribute
  is absent. Known set: `sh`, `bash`, `zsh`, `dash`, `ksh`, `fish`,
  `eval`, `python`, `python2`, `python3`, `node`, `nodejs`, `deno`, `bun`,
  `ruby`, `perl`, `php`, `pwsh`, `powershell`, `lua`. `eval` is the shell
  builtin, in the set since revision 2026-11-11. Version-suffixed pythons
  (`python3.12`) match by prefix (digits and dots only, so `python-config`
  does not); everything else is exact on the basename.

The detector is deliberately conservative: a wrapper option that consumes a
separate argument (`sudo -u alice python3 …`) and backslash-separated
Windows paths inside a raw command string under-report (attribute absent).
The attribute MAY under-report; it MUST never mis-report.

#### 1.1a The `args` attribute (revision 2026-07-25)

`mcp.call` events MAY carry an `args` attribute: the call's `tools/call`
arguments verbatim (the harness `tool_input` object), forwarded so the
server-side approval fingerprint can bind the exact call
(spec/policyset revision 7, `approve.binding: call`). Since revision
2026-11-10 an `other` event MAY carry it too, with the same three values,
because nothing else on that event says what the tool was asked to do. Its
fingerprint binds the concrete call whether `args` is absent or not, so
absent and `{}` key alike there. Semantics are three-valued and
load-bearing:

- **absent**: the arguments were NOT observed (an older client, an
  argless dialect). The server keys the approval at tool scope and the
  `binding_scope` wire field says so; a client MUST NOT fabricate `{}`.
- **`{}`**: the observed no-argument call (binds at call scope).
- **object**: the observed arguments, forwarded verbatim with no
  normalization client-side (the server canonicalizes at hash time), and
  number literals preserved through any re-marshal (a float64 round trip
  that rewrites `9007199254740993` would split the fingerprint across
  lanes).

`args` is fingerprint and audit input only: v1beta1 policy matchers never
read it, and the local PDP ignores it. Emitted by straza normalization for
`mcp.call` and `other` events whose dialect payload carries a `tool_input`
object; the gateway PEP populates the same attribute server-side from the
actual call body.

#### 1.2 Gateway-proxied MCP calls (revision 2026-07-20)

The Straza gateway registers with a harness as ONE MCP server (key
`straza`, written by `straza install`) whose tool names are namespaced
`<app>__<tool>`. A harness therefore presents a proxied call as (server
`straza`, tool `<app>__<tool>`), e.g. Claude Code's
`mcp__straza__demo-tools__echo`, and naive name-based classification
attributes the event to app `straza` with an unsplit tool name, a
vocabulary no policy grant uses (grants are written against the app's own
name). A conforming implementation MUST re-attribute:

- When the MCP server key equals the gateway registration (`straza`) and
  the inner tool name contains `__`, split it at the FIRST `__` into
  (`app`, `toolName`). The split is exact, not heuristic: app names
  cannot contain underscores (spec/app-manifest `name` pattern), so the
  first `__` always terminates the app, the same identity the gateway's
  own catalog map resolves server-side.
- When the inner name is un-namespaced or degenerate (either side of the
  first `__` empty), keep app `straza` and the name verbatim: the
  defer-to-gateway posture. The gateway still enforces precisely via its
  target map, and the hook lane must not guess.

Every PEP MUST produce the same canonical event for the same logical call
(one action, one identity); the `tool-pre-mcp-gateway*` fixtures pin the
hook-lane half of that contract.

### Decision semantics

A blocking hook responds `allow` (silently) or `deny` with a
human-actionable `reason` the model can relay. Encodings are per-dialect
data (mapping tables), e.g. Claude Code `hookSpecificOutput.permissionDecision`
JSON or the exit-2/stderr convention. The `obligations` list on a decision
is always empty since policyset revision 18 (the `redact` and `notify`
obligations were retired, never having run); clients ignore it.

The acknowledgement side is as normative as the deny side (revision
2026-07-31, live-corrected): **silence is the ack** wherever the harness
strict-parses hook stdout: empty stdout on exit 0 for every claude-code
non-enforceable event and for every codex success. A decision document
printed where the harness demands silence is rendered to the user as a hook
FAILURE by the real binaries; the Tier-1 suite scores it non-conformant
(`want.silent`). Codex blocks are exit code 2 + reason on stderr only.
Gemini is the opposite pole: strict JSON on stdout for every event, exit
code ignored.

### Conformance

- **Tier 1** is the full hook PEP: normalizes all events, enforces
  `tool.pre`, injects context at `session.start`, spools audit for every
  event.
- **Tier 2** is gateway-only (any MCP client); this profile does not apply.

Two corpora are the contract, and every case in both carries a
**`provenance` tag** (revision 2026-07-31): `live ...` = captured from or
validated against the real harness binary (named, dated), `doc ...` =
derived from vendor docs/schemas and unconfirmed against a binary. The
corpus tests and the Tier-1 runner refuse an untagged case. A fixture
states how it was verified or it does not run:

- `spec/conformance/hooks/` (normalization): a conforming implementation
  replayed against it MUST produce the expected canonical events and
  decision encodings byte-for-byte (modulo timestamps).
- `spec/conformance/tier1/` (decisions): the runnable suite behind
  `strazactl spec conformance --suite hook-profile --cmd <command>`. Per
  case the runner spawns the command with `STRAZA_HARNESS` set to the
  case's dialect and the payload on stdin, and judges the response
  encoding (`cases.yaml` header is normative); the implementation MUST
  enforce `tier1/policy.yaml` with standalone profile defaults.
  `{policy}` in the command line is replaced with that file's path. The
  suite scores dialect normalization, decision encoding, reason relay, and
  fail-closed behavior; `session.start` context injection and audit
  spooling are not externally observable and remain the implementation's
  own test obligation. `tier1/hello-hook/` is the reference implementation
  written against nothing but this document.

## 2. Change control

Changes follow the additive-change rule in the spec README: schema +
fixtures + version bump in the same PR, named in the release notes. Harness payloads drift
with vendor releases: new harness version ⇒ new fixture directory, never
edits to an existing one.
