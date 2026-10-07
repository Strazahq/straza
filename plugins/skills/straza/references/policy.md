# Policy reference: what may an agent do

This file is the agent's reference for Straza policy. It covers the PolicySet document, the authoring loop, how to explain a deny, how to write the approval choices of a role, and the two rules that keep an agent away from the strazactl login. Manifests, credentials, access rows and application roles are in `servers.md`. Validated examples sit beside it in `examples/`. The public pages are linked where a reader needs the full walk.

## 1. The PolicySet document

A PolicySet is one YAML document that says what the sessions it matches may do. Every active set is compiled into one signed snapshot, and that snapshot is the only form in which policy reaches a decision point. Adding a set can only tighten what an agent may do, because a deny in any set overrides an allow in every other.

```yaml
apiVersion: straza.dev/v1beta1      # fixed
kind: PolicySet                     # fixed
metadata:
  name: dev-guardrails              # lowercase letters, digits and dashes, at most 64 characters
  description: one sentence
spec:
  priority: 150                     # 0 to 1000000, default 0, reporting order only
  match: {roles: [...], users: [...], identity: {...}}   # omitted or empty applies to every session
  capture: {conversations: true, mode: verbatim}         # optional
  rules: [...]                      # at least one rule
  escape: {rego: "..."}             # optional, may only add denies
```

### match: which sessions the set governs

| Field | Meaning | Constraints |
|---|---|---|
| `roles` | Role names the session must hold, OR within the list. | Every name that exists as a role must be of kind application. The server refuses a business role, an approver role or a Straza role. A name that is no role yet is legal and matches nobody until the role exists. |
| `users` | Usernames, OR within the list. | No existence check. |
| `identity` | `userType` (`human`, `agent`, `service`), `agencyMode` (`interactive`, `supervised`, `autonomous`), `swarmId` (deployment-defined). | A subject field that is unset matches no listed value, so unclassified identities fall outside every identity-scoped set. |

Lists are ANDed across the fields that are present. A set with no `match` applies to every session, and the console's By role table shows it as the Everyone row. `match.groups` is a parse error, because Straza has no group object: a role is the selector.

### rules: when a rule applies

A rule applies to a canonical event when all three hold.
1. `events` contains the event kind. Default `[tool.pre]`. `permission.request` is enforced like `tool.pre` and takes the same defaults. The other kinds, `session.start`, `prompt.submit`, `tool.post`, `subagent.start`, `subagent.stop`, `session.end` and `compact.pre`, are always allowed and only audited.
2. `tools` is omitted or contains the event's tool: `shell.exec`, `file.read`, `file.write`, `file.edit`, `net.fetch`, `mcp.call`, `task.spawn`, `other`.
3. `apps` is omitted, or the tool is `mcp.call` and the event's MCP server is listed. A rule with `apps` never applies to a local tool.

### rules: what verdict it produces

1. `require` present and any predicate failing gives deny with the rule's reason. Predicates: `attestation` (`managed` above `advisory` above `none`), `harness` (entries `name` or `name>=version`), `deviceCert: true`. The device certificate is not implemented and every subject reports `false`, so a rule that requires it denies wherever it gates an allow.
2. Matcher blocks, deny side first: `command.denyPatterns`, `paths.deny`, `toolNames.deny` and `interpreters.deny` give deny. Then `command.allowPatterns`, `paths.allow`, `toolNames.allow` and `interpreters.allow` give allow. A rule whose matcher blocks match nothing does not fire.
3. A rule with no matcher blocks fires with its `effect`. A rule with neither matchers, nor `require`, nor `effect` is rejected.
4. `effect` beside matchers must agree with the side used: `effect: allow` with only deny patterns is rejected, and the reverse.
5. Multi-path events: the deny side fires when any path matches, the allow side only when every path matches.

`reason` is the text the agent reads on a deny or a hold. When it is missing the engine writes one naming the set and the rule. `interpreters` is consulted only when the event carries a detected interpreter such as `python3` or `bash`, so `"*"` there never matches `git status`.

### Patterns

Patterns are globs, anchored to the whole subject. A `re:` prefix makes a Go regular expression, also anchored. In command and tool-name patterns `*` matches any run of characters. In path patterns `*` stops at `/`, `**` crosses it, and `${workspace}` is replaced by the session's workspace root. A command pattern is run against the raw command string, against the argv re-joined with single spaces, and against every single argv token, and any hit counts. Paths are cleaned before matching, and relative paths are resolved against the workspace. Matching is case-sensitive.

### mode: escalating a winning allow

A mode acts only when the winning effect is allow. A deny is final and never escalates. Every escalation fails closed.

| mode | What happens |
|---|---|
| `serverCheck` | The local decision point asks the server before acting. An unreachable server means deny. |
| `classify` | The built-in heuristic inspects a shell command for indirection such as a pipe into a shell or a decode-then-execute chain. A clear signal denies with the signal in the reason. A missing classifier or a blown deadline denies. It reads one command with no memory of the session, and file, network and MCP events never reach it. |
| `approve` | A human decides before the effect is final. The gateway holds an `mcp.call` until the decision or the timeout. The hook lane answers deny at once with a reference, then honors one identical retry after approval. Timeout, a denial or an unreachable approval service means deny. |
| `confirm` | The same machinery, but only the requester may decide, root included may not. A `confirm` rule on an autonomous subject is a plain deny. `approve.roles`, `approve.selfApproval` and `approve.deciders` are rejected under `confirm`. |

### The approve block

The block is legal only beside `mode: approve` or `mode: confirm`.

| Field | Meaning | Bounds and default |
|---|---|---|
| `class` | `hold` pauses the call for a bounded wait. `ticket` answers at once and leaves a day-scale request whose grant a later, possibly fresh, session consumes once. | default `hold` |
| `roles` | Who may decide. Every name must be an approver-kind role, or `straza-admin` by name. The server checks this at activation. | |
| `deciders` | Decider kinds resolved per record. Only `sponsor`: the requester's accountable human from the identity manager, resolved at request time and persisted on the record. Composable with `roles`. | |
| `selfApproval` | Whether the requester may decide their own request. An autonomous subject never decides its own request, whatever the rule says. | default `false` |
| `timeoutSeconds` | hold only: how long the request stays open. | default 90, 1 to 3600 |
| `retryTTLSeconds` | hold only: how long after the decision the held call or one identical retry may use the approval, once. | default 60, 1 to 600 |
| `ticketTTLSeconds` | ticket only: the decision window. | default 86400, 1 to 2592000 |
| `grantTTLSeconds` | ticket only: how long an approved grant stays consumable. | default 3600, 1 to 86400 |
| `bind` | ticket only: `fingerprint` re-matches exactly this call. `predicate` is reserved: it parses, works exactly like `fingerprint`, and validate warns on it. For an MCP tool whose arguments change on every call, use `binding: tool`. | default `fingerprint` |
| `binding` | `mcp.call` only: `call` folds the canonicalized arguments into the approval key, `tool` covers the MCP server and the tool name only. | default `call` |
| `notify` | Channels that announce the request: `console`, `slack`, `push`. It narrows announcement only, never who may decide. `[console]` alone silences third-party notification. Unknown names and duplicates are rejected. | empty means every configured channel |

Setting `timeoutSeconds` or `retryTTLSeconds` under `class: ticket` is rejected. An approve block with no `roles`, no `deciders` and no `selfApproval` behaves as `deciders: [sponsor]`, which routes to the person behind the agent. For an agent that is its sponsor. A person with no sponsor confirms their own call on an enrolled phone or browser, and a person who has a sponsor is decided by that sponsor. When no decider resolves, because an agent has no usable sponsor, the request is denied at request time with the cause and the fix in the reason, and no pending record is created.

The gateway holds an `mcp.call` until the decision, the rule's `timeoutSeconds` or the server's `approval.gatewayHoldSeconds`, 120 seconds by default, whichever comes first. Past the cap the model gets a pending reason and the same retry path.

### capture: recording conversations

`capture.conversations: true` records prompts and replies for the matched sessions. `mode: verbatim` is the default, and `mode: redact` masks recognizable credential shapes on the agent's machine before upload. When matched sets disagree, redact wins. A `mode` without `conversations: true` is rejected as inert. The agent is told at session start that recording is active. The walk is at https://docs.straza.ai/guides/write-policy/capture/.

### priority, combining and defaults

Every firing rule from every matched set is combined at once. An explicit deny overrides every allow. The reported `ruleId`, `setName` and `reason` come from the highest-priority firing rule of the winning effect, with ties broken by set name and then rule order, except that a rule holding the call for approval is always the one named. Priority and rule order never change the outcome, only which rule the record names. When no rule fires, a default decides and the record carries no rule id.

| Event | Standalone | Enterprise |
|---|---|---|
| Local tool (`shell.exec`, `file.*`, `net.fetch`, `task.spawn`, `other`) | allow | deny, the reason names the tool |
| `mcp.call` on a tool the subject's roles have access to | allow, the access row gives it | allow, the access row gives it |
| `mcp.call` without access | deny, the reason names the server and the tool | deny, the reason names the server and the tool |
| Non-tool events | allow, audited | allow, audited |

So an enterprise set for a role opens its lane with an allow rule for the local tools before it denies anything, and a standalone set needs no allow rule at all. The Rego escape hatch runs after the rules and may only add denies. A module that calls a built-in that reaches the network, the file system or the process environment, or one whose single call can run far past the deadline, does not compile: http.send, net.lookup_ip_addr, json.match_schema, json.verify_schema, opa.runtime, strings.render_template, rego.parse_module, graph.reachable_paths, bits.lsh, net.cidr_contains_matches, glob.match and the six graphql built-ins. The modules of one decision must finish within 100 ms, checked between evaluation steps, or the decision is a deny. The snapshot is signed, never encrypted, so secrets never belong in policy text. Tell the granted default by its empty `ruleId` and `default: true`, never by the text of its reason.

## 2. The authoring loop

Follow these steps in order. The walks with every command are at https://docs.straza.ai/guides/write-policy/first-deny/ and https://docs.straza.ai/guides/write-policy/simulate-and-coverage/. Steps 2 to 5 and step 7 read or check and run on the person's login. Step 6 changes Straza, so the person runs it in their own terminal, as the SKILL.md section Reads, checks and changes says. The person reads the set in the draft before publishing it, because it may govern your own sessions.

1. Write the document from the requirement. One rule per intent, one reason per rule, and the role in `match.roles` an application role. For the approval choices of one role, start from section 4.
2. Validate offline. No login is needed.

```sh
strazactl policy validate -f local-tools-guardrails.yaml
```

```text
local-tools-guardrails.yaml: PolicySet "local-tools-guardrails" OK (2 rules, priority 0)
note: validate refuses only the roles the product reserves, whose names start with straza- or mcp-admin-, in match.roles, and any of them but straza-admin in approve.roles. The server judges every other role a set names, in match.roles and approve.roles, and strazactl drafts check -f local-tools-guardrails.yaml runs those checks against live state.
```

   The offline check is the engine's own parser, so it catches shape errors and the rule-level refusals, such as an approve block without its mode or a ticket with hold knobs. It reads the document alone and cannot see the server's roles. It refuses a role the product reserves, a name that starts with straza- or mcp-admin-, in `match.roles`, and any of them but straza-admin in `approve.roles`, in the words activation uses. A business role in `match.roles`, and any other `approve.roles` name that is not an approver role or does not exist, pass it and are refused by the check of step 3.

3. Check the local file against live state. The check stores nothing, contacts nothing and runs on the person's login. It runs the checks the offline validate cannot, such as a business role in `match.roles` or an `approve.roles` name that is not an approver role, and it names each line that widens access.

```sh
strazactl drafts check -f local-tools-guardrails.yaml
```

   Each refused line names its fix. The command exits 0 when the set can be published as it is, 1 when a line is refused, and 2 when it could not run. Fix the file and check again until it exits 0. For a set that is live, `strazactl policy diff <name> -f <file>` shows the lines the file changes.

4. Simulate the calls that matter. With `-f` the server evaluates each call twice: against the live snapshot, and against the live sets with the local file in place of its live namesake, or added to them when the name is new.

```sh
strazactl policy simulate --user dana --tool shell.exec --command "git push --force origin main" -f local-tools-guardrails.yaml
```

```text
live    ALLOW  Decided by rule local-tools in policy local-tools-guardrails.
file    DENY   Decided by rule no-rm-rf in policy local-tools-guardrails: Straza: recursive force-delete is denied for this role. Delete the files one by one, or ask an admin to change the local-tools-guardrails policy.

the live policy says ALLOW; this file says DENY
```

   The row labelled file is the local file's answer, and the last line calls it "this file". Use `--roles` for a hypothetical subject instead of `--user`. Describe files and MCP calls with `--path`, `--app` and `--tool-name`. The subject's attestation defaults to `none`, so pass `--attestation managed` when a rule requires it. A human gate prints as a gate line. Without `-f` it reads `gate      needs approval: 5-minute approval hold, deciders release-approvers`, and with `-f` each row carries its own indented `gate: needs approval: ...` line. The last line agrees only when both answers have the same effect and the same gate lines, so a file that adds a hold reads `the live policy says ALLOW; this file says ALLOW after approval`. A sponsor pool reads `the person behind the agent`, a ticket reads `day-scale approval ticket` with its deciders and grant window, and a confirm rule reads `needs approval: the requester confirms on their own device`. Simulate does not run the checks of step 3, so an agreeing answer does not promise that the set can be published. Simulate detects the interpreter of a shell command from `--command` the way the live decision point does, so an `interpreters` rule fires from the per-field flags. An event file that already names an interpreter keeps that name.

5. Replay recorded calls. `scripts/replay-recent.sh <user> <file.yaml> [limit]` replays a user's recent tool decisions through the local file and counts the ones it would change, a file that adds, drops or changes a hold included, because the verdict line agrees only when the effect and every gate line match. It stops with an error when a simulate fails or prints no verdict line, so it never reports a count it could not read. One record by hand:

```sh
strazactl audit tail --user dana --limit 400 | grep '"command":"rm -rf' | head -1 | sed 's/^#[0-9]* \[[^]]*\] //' | jq -c '{kind: .data.event, tool: .data.tool, command: .data.command}' > recorded-event.json
strazactl policy simulate --user dana --event-json recorded-event.json
```

   A record holds most of the event it judged: the kind, the tool, the command, the paths and the workspace, and for an MCP call the server and the tool name. It does not hold the argv, the interpreter, the attestation or the harness. Simulate detects the interpreter again from the command, as the live decision point does, so an `interpreters` rule answers in a replay as it did live unless the live event's argv named another program than its command. A rule that needs the argv, the attestation or the harness can answer differently in a replay. `--limit` counts records of every type, not only decisions, and a limit above 1000 is served as 100. A held call was recorded as a deny with a reference and replays as an allow with a gate line, which is the same outcome. The answer is today's verdict against the current snapshot, not a replay of the decision the record holds.

6. The person creates and publishes the draft in their own terminal. The first command stores the draft and prints its check and its number, 41 here. The second checks it again, asks the person to acknowledge each line that widens access, then recompiles and signs the snapshot:

```sh
strazactl drafts create -f local-tools-guardrails.yaml --note "Deny recursive force-deletes for the dev role"
strazactl drafts publish 41
```

   `strazactl policy list` then shows the set as Live. `strazactl drafts revert 41` makes a new draft that undoes a published one, which the person publishes the same way. A file cannot say off, so to turn a set off and keep it stored the person runs `strazactl policy deactivate local-tools-guardrails`, which recompiles the snapshot without the set, and `policy list` then shows it as Off. `strazactl policy list --json` spells the same state `"status": "draft"`, the wire's word for a set that is off. `strazactl policy apply` followed by `strazactl policy activate` still stores and publishes one set directly, and `apply` on the name of a live set saves the text as edits that `policy show` and `policy list` mark as `Live, edits not published` until `activate` publishes them. A session that is already running keeps deciding from the snapshot it checked in with until its next check-in. Start a new session to see the change at once. A running client picks up a new snapshot within about 30 seconds, so a call made right after activation can still be judged by the old one.

7. Check the result. For an MCP gate, `strazactl catalog preview --role <role> --app <server>` shows each held tool as `approve_gated` with the set, the rule, the deciders and the wait, and each refused tool as `hidden_policy`.

## 3. Explaining a deny

Start from the record. Every hook decision is one audit record of type `straza.audit.tool`, and every gateway call is one of type `straza.audit.mcp`.

```sh
strazactl audit tail --user dana --limit 2
```

```text
#309 [dana] {"data":{"command":"rm -rf /home/dana/work/build","effect":"deny","event":"tool.pre","reason":"Straza: recursive force-delete is denied for this role. Delete the files one by one, or ask an admin to change the local-tools-guardrails policy","ruleId":"no-rm-rf","session":"01a0b93f-357d-7ed9-978c-3a6e907ea550","setName":"local-tools-guardrails","snapshot":"21a867daf5fbc884d7d7dec0d64861d3763713b595070f0a0797a75808aead75","tool":"shell.exec","workspace":"/home/dana/work"},"type":"straza.audit.tool"}
```

1. Read `effect`, `reason`, `ruleId` and `setName`. They name the reported rule, which is the highest-priority firing rule of the winning effect. Tell a policy decision by `ruleId` and `setName`, not by the text of the reason: an author writes any reason, and the standalone starter policy's reasons start with `Straza starter policy:`.
2. Open that set's document with `strazactl policy show <set>` and find the rule. Another set may also have fired a deny. The record names one rule, and the deny stands as long as any active set denies the call.
3. Replay the event with `--event-json` as in step 5 of section 2 to get today's answer, then again with `-f` to test the fix.
4. An empty `ruleId` means no rule fired. The deny then came from a default: the enterprise profile denying a local tool no rule allows, or an `mcp.call` on a tool the subject's roles have no access to. A gateway refusal of an unknown tool name, a hidden tool or a throttled call is also a deny with no rule. The reason names the tool, or the server and the tool. The fix is an allow rule for the lane, or an access row, never a looser deny.
5. A `straza.audit.mcp` deny can name a rule whose effect is allow. A later gate refused a call that rule allowed: a hold that was denied, expired or is still pending, or no credential the caller could use. The reason carries the refusal, so fix what it names, the decision or the caller's credential, and leave the rule alone.
6. A classifier deny keeps the firing rule's id and its reason starts with `Straza: classifier:`. A held call answers the agent with a deny whose reason starts with `Straza: approval requested` or `Straza: approval ticket`, or, when a rule routes to the sponsor and the requester is a person with no sponsor, `Straza: this call needs the requester's confirmation`. Each writes an approval record of type `straza.audit.approval` with `phase` `request`, `resolution` and, for tickets, `consumed`. The walks are at https://docs.straza.ai/guides/write-policy/hold-for-a-human/ and https://docs.straza.ai/guides/write-policy/tickets/.

A developer without admin rights cannot read the audit log. Their own machine keeps a journal of its hook decisions, and `straza trace show -n 10` prints the latest ones with the effect, the rule and the set, without the command text. Gateway calls are not in that journal.

## 4. A role's approval choices

Access and policy are two layers. An access row gives a role tools of one server, and a tool without a row does not exist for that role's sessions. Policy then denies or gates what the row gives, and no allow rule is needed for a tool a row already gives. Manifests, credentials, access rows, application roles and the rule that an application role reaches one server are in `servers.md`. This section writes the gates.

The console keeps one set per application role, named `<role>-access`, with priority 100 and `match.roles: [<role>]`. The role page and the server page read that set back and edit it in place, so a set written from the CLI in the shapes below shows there as the role's choices. Because an application role reaches one server, every rule in its set names that server in `apps`. A business role carries no approval choices. Write them on the application roles it composes. `examples/scout-tools-readers-access.yaml` is a validated set with a hold and a ticket.

Every gate rule has the same frame: `tools: [mcp.call]`, `apps: [<server>]`, `effect: allow`, `mode: approve`, an `approve` block and a `reason`. The choices differ in the tools the rule names and in the approve block.

| Choice | How the rule says it | Who decides |
|---|---|---|
| Some tools wait for an approver | `toolNames.allow: [<tools>]`, `approve: {roles: [<approver role>], timeoutSeconds: 300}` | A holder of the approver role who is not the requester, on an enrolled phone or in the console. The role must exist and be of kind approver, or be `straza-admin`. |
| Some tools wait for the person behind the agent | `approve: {deciders: [sponsor], timeoutSeconds: 300}` | The agent's sponsor from the identity manager. |
| One person governs their own agent | an `approve` block with no `roles`, no `deciders` and no `selfApproval` | An agent's sponsor. A person with no sponsor confirms their own call on their enrolled phone or browser. |
| Some tools need a day-scale ticket | `approve: {class: ticket, ticketTTLSeconds: 86400, grantTTLSeconds: 3600}` with `roles` or `deciders` | The first call is refused and raises the ticket. After approval the same call runs once within the grant window, in this session or a later one. |
| Every call needs approval | the same rule with no `toolNames` | As its approve block says. Tools the server adds later are held too. |
| Some tools are refused | `effect: deny` with `toolNames.deny: [<tools>]` and no mode | Nobody. The reason tells the agent why. |

Put the rules that name tools first and a rule with no `toolNames` last. Among firing approve rules of one set the earlier rule decides, so a per-tool choice placed after the every-call rule never applies. `mode: confirm` makes the requester the only decider, root included, on their own device. It takes no `roles`, `deciders` or `selfApproval`, and on an autonomous agent it is a plain deny.

One person cannot approve their own calls through an approver role. A person who creates an approver role, holds it and names it in `approve.roles` gets a hold that nobody else can decide, because `selfApproval` defaults to false, so the call ends in a deny when the wait runs out. For one person governing their own agent, use the bare approve block and enroll that person's phone as in `quickstart.md`. The console and strazactl refuse to decide a person's own request, because an agent on the same machine could do the same. The server setting `approval.unsignedOwnDecisions` removes that protection. It is an operator's risk decision, never a setup step.

The built-in `straza` server has no access rows, so its tools `approval_request`, `approval_status` and `approval_await` need an explicit allow rule with `apps: [straza]`. An explicit allow rule for any other server adds nothing the access row does not already give. `examples/mcp-tool-gate.yaml` shows the same frame with a sponsor hold and a deny. The walk is at https://docs.straza.ai/guides/serve-mcp-apps/catalogs-per-role/.

## 5. Gotchas

- One reason per rule. A rule reports one reason for every pattern it holds, so a force-push pattern added to a force-delete rule is denied with the force-delete reason. Give each pattern family its own rule.
- Globs match whole tokens and whole strings. `rm -rf *` does not match `rm -r -f build`. Add a second pattern or a `re:` expression when the variants matter.
- An approve rule only marks an allow. A deny from any set still wins, so a held command that another set denies is denied, never held.
- An approve block with no `roles` routes to the person behind the agent: an agent's sponsor, or the person themself when they run their own agent and have no sponsor. An agent with no usable sponsor is denied at request time with the fix in the reason. An approver role held only by the requester decides nothing, as section 4 explains.
- Business roles are never matched. `match.roles` must name application roles, the kind that carries access rows. Name the application roles the business role composes, or use `match.identity` or `match.users`.
- The approve block needs `mode: approve` or `mode: confirm`, and `class: ticket` rejects `timeoutSeconds` and `retryTTLSeconds`.
- `apply` on the name of a live set saves unpublished edits and leaves the published version deciding. Only `strazactl policy activate <name>` publishes them, after the checks, as step 6 of section 2 says.

## 6. Keep the strazactl login away from agents

The person's strazactl login lives in `~/.straza/credentials.json`, and a program that reads it can act as that person on every admin route. strazactl already refuses to change Straza on that login inside a coding agent, and these two rules refuse the plain ways of reading or changing the file. Add them to the `rules:` of a set that applies to the agents' sessions, a set with no `match` on a standalone server or the set that matches the agents' application roles in an enterprise deployment, and hand the changed file to the person as a draft like any other. They only add denies, so the publish shows no line that widens access.

```yaml
    - id: protect-straza-login
      events: [tool.pre]
      tools: [file.read, file.write, file.edit]
      paths:
        deny: ["**/.straza/credentials.json"]
      effect: deny
      reason: "Straza: an agent may not read or change the strazactl login in ~/.straza/credentials.json. For automation, use an admin API token in STRAZA_API_TOKEN, and an agent proposes config changes through the built-in straza MCP server's drafting tools."
    - id: protect-straza-login-shell
      events: [tool.pre]
      tools: [shell.exec]
      command:
        denyPatterns: ["*.straza/credentials.json*"]
      effect: deny
      reason: "Straza: an agent may not read or change the strazactl login in ~/.straza/credentials.json. For automation, use an admin API token in STRAZA_API_TOKEN, and an agent proposes config changes through the built-in straza MCP server's drafting tools."
```

The first rule refuses file.read, file.write and file.edit of the file, and the second refuses a shell command that names it with forward slashes. The pair refuses nothing else. A search tool run over the folder, a command run inside `~/.straza` such as `cat credentials.json`, and a path written with backslashes all reach the file, so the pair is a guardrail against a mistake, not a boundary.
