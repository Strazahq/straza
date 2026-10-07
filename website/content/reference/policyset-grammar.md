---
title: PolicySet grammar
description: Every key of a PolicySet document, with its type, its default and a one-line example.
pagetype: reference
weight: 7
draft: false
keywords: policyset policy grammar yaml keys match rules approve confirm serverCheck classify require harness deviceCert identity users events escape rego capture
---


A PolicySet is one YAML document that selects sessions and holds the rules for their tool calls. The grammar is version `straza.dev/v1beta1`, and the parser refuses a key it does not know, so a typo fails at once instead of matching nothing. `strazactl policy validate -f` checks a file on your machine with no server, and `strazactl drafts check -f` adds the server's checks of the roles the set names. [Policies]({{< relref "concepts/policy-model.md" >}}) explains how rules combine. Every example on this page passes the offline check.


```yaml
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: dev-rules
  description: Rules for sessions that hold the dev role
spec:
  priority: 10
  match:
    roles: [dev]
  rules:
    - id: no-force-push
      tools: [shell.exec]
      command:
        denyPatterns: ["git push --force*"]
      reason: "Straza: force-push is denied for dev"
    - id: hold-deploy
      tools: [mcp.call]
      apps: [deploy-tools]
      toolNames:
        allow: ["deploy_*"]
      mode: approve
      approve:
        roles: [release-approvers]
      reason: "Straza: a deploy needs a release approver"
```

## Document


| Key | What it takes | Example |
|---|---|---|
| `apiVersion` | Exactly `straza.dev/v1beta1`. Required. | `apiVersion: straza.dev/v1beta1` |
| `kind` | Exactly `PolicySet`. Required. | `kind: PolicySet` |
| `metadata.name` | Lowercase letters, digits and hyphens, 1 to 64 characters, starting and ending with a letter or a digit. Required. | `name: dev-rules` |
| `metadata.description` | Text. Empty by default. | `description: Rules for dev` |
| `spec.priority` | An integer from 0 to 1000000. The default is 0. | `priority: 10` |
| `spec.match` | The [selectors](#match). Empty by default, which selects every session. | `match: {roles: [dev]}` |
| `spec.rules` | A list of at least one [rule](#rule). Required. | The example above |
| `spec.capture` | The [recording](#capture) of conversations. Absent by default, so this set records nothing. | `capture: {conversations: true}` |
| `spec.escape` | A [Rego module](#escape). Absent by default. | The example under Escape |

Priority only picks which firing rule a decision reports, and it never turns a deny into an allow.

## Match


| Key | What it takes | Example |
|---|---|---|
| `match.roles` | A list of application role names. Empty by default. | `roles: [dev]` |
| `match.users` | A list of usernames. Empty by default. | `users: [alice]` |
| `match.identity.userType` | A list of `human`, `agent` and `service`. Absent by default. | `userType: [agent]` |
| `match.identity.agencyMode` | A list of `interactive`, `supervised` and `autonomous`. Absent by default. | `agencyMode: [autonomous]` |
| `match.identity.swarmId` | A list of the fleet names your identity manager sets. Absent by default. | `swarmId: [scan-fleet-1]` |

Values inside one list combine with OR, and the lists combine with AND. An empty `match` selects every session.

`match.roles` takes application roles, the roles that carry tool access. A session holds every role its roles compose, so naming the application role covers every way a person holds it. The server's check refuses a business role, an approver role and a Straza role in `match.roles`, and each refusal names the fix, such as "Name the application roles it composes instead". A name that is not a role yet stays legal and matches nobody until the role exists.

`match.identity` needs at least one of its three lists. Your identity manager sets the user type, agency mode and swarm ID of each identity, as [AI agents as identities]({{< relref "guides/connect-identity/nhi-provisioning.md" >}}) shows. An identity that carries none of them matches no identity selector, so a set scoped to `userType: [agent]` does not reach it. A deny that must also catch such identities belongs in a set with no identity selector, and it then applies to people as well, because a deny overrides every allow.

`match.groups` was retired, and the parser refuses it with a sentence that tells you to select on roles instead.

## Rule


| Key | What it takes | Example |
|---|---|---|
| `id` | The same form as a set name, unique in the set. Required. | `id: no-force-push` |
| `events` | A list of [event kinds](#events). The default is `[tool.pre]`. | `events: [permission.request]` |
| `tools` | A list of [tools](#tools). Absent by default, so the rule covers every tool. | `tools: [shell.exec]` |
| `apps` | A list of MCP server names. Absent by default, so the rule covers every server and every local tool. | `apps: [demo-tools]` |
| `toolNames.allow` | A list of [patterns](#patterns) over the MCP tool name. | `allow: ["read_*"]` |
| `toolNames.deny` | A list of patterns over the MCP tool name. | `deny: ["delete_*"]` |
| `command.allowPatterns` | A list of patterns over a shell command. | `allowPatterns: ["git *"]` |
| `command.denyPatterns` | A list of patterns over a shell command. | `denyPatterns: ["rm -rf *"]` |
| `paths.allow` | A list of patterns over the paths a file call touches. | `allow: ["${workspace}/**"]` |
| `paths.deny` | A list of patterns over the paths a file call touches. | `deny: ["**/.env*"]` |
| `interpreters.allow` | A list of patterns over the interpreter a shell command runs. | `allow: ["node"]` |
| `interpreters.deny` | A list of patterns over the interpreter a shell command runs. | `deny: ["python*"]` |
| `require.attestation` | `none`, `advisory` or `managed`. Absent by default. | `attestation: managed` |
| `require.harness` | A list of harness names, each with an optional lowest version after `>=`. Absent by default. | `harness: ["claude-code>=2.1"]` |
| `require.deviceCert` | A boolean. The default is false. | `deviceCert: true` |
| `mode` | `serverCheck`, `classify`, `approve` or `confirm`, as [Modes](#modes) describes. Absent by default. | `mode: approve` |
| `approve` | The [approve block](#approve). Absent by default. | `approve: {class: ticket}` |
| `effect` | `allow` or `deny`. Absent by default, so the side of the pattern that matched decides. | `effect: deny` |
| `reason` | Text the agent and the person deciding read. A deny without one reads "Straza: blocked by policy rule" followed by the set and the rule. | `reason: "Straza: no curl"` |

A rule applies to a call when the call's event kind is in `events`, its tool is in `tools` when the rule lists any, and, for a rule with `apps`, the call is an MCP call to one of those servers. A rule with `apps` never applies to a local tool, and `toolNames` is read only for an MCP call.

Once a rule applies, it checks `require` first. A session below the required attestation, or on a harness the list does not name, gets a deny from this rule with its reason. Harness names include `claude-code`, `codex` and `gemini`. `require.deviceCert` cannot be met today, because no device certificate is issued yet, so a rule that sets it denies wherever it gates an allow, and the server's check warns that "require.deviceCert is not implemented yet".

The pattern lists come next, deny side first. A match on a deny side is a deny, a match on an allow side is an allow, and a rule whose lists match nothing does not fire. A rule with no lists uses its `effect`. An interpreter list is read only when the shell command runs a known interpreter, such as `bash`, `python3` or `node`.

The parser refuses a rule that can never fire with "rule needs an effect, a matcher block, or require (it can never fire)". It also refuses `effect: allow` beside deny-side lists only, and the reverse.

`obligations` was retired. A set that carries it is refused, and the sentence names the two replacements, `capture.mode: redact` on the set and `approve.notify` on the rule.

## Modes


A mode acts only when the rule's allow wins, because a deny is final. Every mode fails closed.

| Mode | What happens before the call runs | When it fails |
|---|---|---|
| `serverCheck` | The client on the machine asks strazad to decide the call again. | The client cannot reach strazad, so the call is denied whatever the offline grace, with "Straza: security layer unreachable for a server-checked action". |
| `classify` | The built-in classifier inspects the call, with no model and no settings. | An error or a blown deadline is a deny. |
| `approve` | A person decides, on a hold or on a ticket, as the [approve block](#approve) sets. | A timeout, a refusal or an unreachable approval service is a deny. |
| `confirm` | The requester alone confirms the call, which guards against an agent's mistake or a prompt injection and not against the person. | A timeout or a refusal is a deny. An autonomous session gets a deny at once, because no person stands behind it to confirm. |

A confirm rule shares the approve block, but the requester is the only one who decides. The parser therefore refuses `roles`, `deciders` and `selfApproval` under it, with "mode confirm may not set approve.roles (the requester is the decider)" and the same sentence for the other two keys. A local tool call that a confirm rule holds is denied with a reason that starts "Straza: this call needs the requester's confirmation, on" and names where the requester confirms and the request's reference.

## Approve


| Key | What it takes | Example |
|---|---|---|
| `roles` | A list of approver roles, or `straza-admin`. Empty by default. | `roles: [release-approvers]` |
| `deciders` | A list with the one value `sponsor`. Empty by default. | `deciders: [sponsor]` |
| `selfApproval` | A boolean. The default is false. | `selfApproval: true` |
| `class` | `hold` or `ticket`. The default is `hold`. | `class: ticket` |
| `timeoutSeconds` | An integer from 1 to 3600, for a hold only. The default is 90. | `timeoutSeconds: 300` |
| `retryTTLSeconds` | An integer from 1 to 600, for a hold only. The default is 60. | `retryTTLSeconds: 120` |
| `ticketTTLSeconds` | An integer from 1 to 2592000, for a ticket only. The default is 86400, one day. | `ticketTTLSeconds: 259200` |
| `grantTTLSeconds` | An integer from 1 to 86400, for a ticket only. The default is 3600, one hour. | `grantTTLSeconds: 7200` |
| `binding` | `call` or `tool`. The default is `call`. | `binding: tool` |
| `bind` | `fingerprint` or `predicate`, for a ticket only. The default is `fingerprint`. | `bind: fingerprint` |
| `notify` | A list of `console`, `slack` and `push`. Empty by default, so every configured channel announces. | `notify: [console, push]` |

The approve block is legal only beside `mode: approve` or `mode: confirm`, and the parser refuses it elsewhere with "approve block requires mode: approve or mode: confirm".

An approve rule with no `roles`, no `deciders` and no `selfApproval` routes each request to the person behind the agent, which is the agent's sponsor, or the person themself when they run their own agent. An agent with no usable sponsor is denied at once, and the server's check warns about such a rule with "no approver named". The server's check also refuses an `approve.roles` entry that is not an approver role or `straza-admin`, with a sentence that says "only approver roles or straza-admin may decide". For an autonomous session the engine turns `selfApproval` off, whatever the rule says.

A hold waits for the decision. The held MCP call keeps its connection open, and a local tool call is denied at once with the request's reference, so the agent sends it again after the approval. One approval runs one call, either the held call or one identical retry by the same person in the same session within `retryTTLSeconds` of the decision. The parser refuses `timeoutSeconds` and `retryTTLSeconds` under `class: ticket`.

A ticket never waits. `ticketTTLSeconds` is the window in which a person decides. `grantTTLSeconds` is the time after the approval in which one later call that matches the approved one runs on the grant, from any session of the requester.

`binding` sets what an approval covers for an MCP call. `call` covers the tool and its exact arguments, and `tool` covers the tool with any arguments, for a tool whose arguments change on every call. `bind: predicate` is reserved and works exactly like `fingerprint`, and the server's check says so. `notify` narrows which channels announce a request, and every surface where a person decides keeps working whatever it lists. [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#approvals" >}}) says what happens when each window runs out.

## Patterns


- A pattern is a glob, or a Go regular expression when it starts with `re:`. Either one must match the whole value.
- In a glob, `?` matches one character. In a command or a tool name, `*` matches any run of characters, spaces included.
- In a path, `*` stops at `/` and `**` crosses it. `${workspace}` stands for the session's workspace, and a relative path in a call is read against it.
- A command pattern runs against the command as written, against its words joined with single spaces, and against each word alone, so quoting and extra spaces do not slip past a deny.
- A call that touches several paths is denied when any path matches a deny, and allowed only when every path matches an allow.
- Matching is case-sensitive, except the drive letter of a Windows path.

## Events


| Kind | When it fires | Can it stop a call | Harnesses that send it |
|---|---|---|---|
| `session.start` | A session starts | No | claude-code, codex, gemini, python-sdk |
| `prompt.submit` | A person sends a prompt | No | claude-code, codex, gemini, python-sdk |
| `tool.pre` | Before a tool runs | Yes | claude-code, codex, gemini, python-sdk, and the MCP gateway |
| `tool.post` | After a tool ran | No | claude-code, codex, gemini, python-sdk |
| `permission.request` | The harness asks permission for a tool | Yes | claude-code, codex |
| `subagent.start` | A subagent starts | No | claude-code, codex |
| `subagent.stop` | A subagent stops | No | claude-code, codex |
| `session.end` | A session or a turn ends | No | claude-code, codex, gemini, python-sdk |
| `compact.pre` | Before the harness compacts its context | No | claude-code |

A rule with no `events` fires on `tool.pre`, which every harness sends. The MCP gateway checks a call once, as `tool.pre`, so a rule for MCP calls whose events leave out `tool.pre` never fires there. The console's rule editor and the server's check both warn when a kind you pick never fires on a harness, and name the harnesses whose sessions then pass the rule. `GET /v1/admin/policies/event-support` serves the same table. An event kind Straza does not know is denied before any rule is read.

## Tools


| Tool | What it covers |
|---|---|
| `shell.exec` | A shell command |
| `file.read` | Reading a file |
| `file.write` | Writing a file |
| `file.edit` | Editing a file |
| `net.fetch` | Fetching a URL |
| `mcp.call` | A call to a tool of an MCP server |
| `task.spawn` | Starting a subagent or a task |
| `other` | A harness tool Straza cannot classify |

An MCP call that no rule matches runs when one of the caller's roles has access to the tool, and is denied when none has. Every other tool that no rule matches takes the profile default, as [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md#no-rule" >}}) shows.

## Capture


| Key | What it takes | Example |
|---|---|---|
| `capture.conversations` | A boolean. Required in the block. | `conversations: true` |
| `capture.mode` | `verbatim` or `redact`. The default is `verbatim`. | `mode: redact` |

Recording is off unless a set that matches the session turns it on. When matched sets disagree on the mode, `redact` wins. The parser refuses `mode` without `conversations: true`, and the server's check warns about a verbatim set with an empty `match`, because it records every session in full. [Record a conversation]({{< relref "guides/write-policy/capture.md" >}}) shows a recording set.

## Escape


| Key | What it takes | Example |
|---|---|---|
| `escape.rego` | A Rego module in `package straza.ext`. Absent by default. | The block below |

The module runs after the rules and can only add denies. Straza reads its `deny` rules and nothing else, and refuses a module that declares an `allow` rule. The input is the call as `input.event`, the identity behind the session as `input.subject` and the rules' decision as `input.decision`. A deny message reaches the agent with `Straza: ` in front of it.

```yaml
escape:
  rego: |
    package straza.ext

    deny contains msg if {
      input.event.tool == "shell.exec"
      contains(input.event.command, "sudo")
      msg := "sudo is not allowed"
    }
```

The modules that apply to one decision share a deadline of 100 ms, and a decision whose modules run past it is a deny. A module may not reach the network, the file system or the process environment, and may not call a built-in whose single call can run far past the deadline. The refused built-ins are `http.send`, `net.lookup_ip_addr`, `json.match_schema`, `json.verify_schema`, `opa.runtime`, `strings.render_template`, `rego.parse_module`, `graph.reachable_paths`, `bits.lsh`, `net.cidr_contains_matches`, `glob.match` and the six `graphql` built-ins. The refusal names the set, the built-in, its line and the reason:

```text
policy: set rego-glob: its Rego module calls glob.match on line 4, and Straza refuses that built-in because a single call of it can run far past the 100 ms decision deadline or crash the process, and Straza checks that deadline only between calls. Remove the call from the module
```
