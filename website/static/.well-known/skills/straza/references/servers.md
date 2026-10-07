# Onboard an MCP server end to end

This file takes one MCP server from nothing to a role whose sessions call its tools, with the calls that need a person held for that person. You write the server's manifest, its roles and the role's approval set as YAML files, check them against live state, and hand them to the person as one draft. The person publishes the draft, and Straza applies all of it in one step, with the approval set in force before the access row it gates, so no half-built setup is ever live. A published role reaches nobody until it is assigned, so the assignment comes last.

Every command here acts as an administrator on the server that `strazactl status` names. You run the reads and checks on the person's login: `strazactl spec validate`, `strazactl policy validate`, `strazactl policy simulate`, `strazactl drafts check`, `strazactl drafts show` and every export and list. The person runs every change in their own terminal: `strazactl drafts create` and `strazactl drafts publish`, the secret of section 3, the phone enrollment of section 7 and every `assign`. Inside a coding agent `strazactl` refuses those on the person's login, as the SKILL.md section Reads, checks and changes says. The walks behind this file are https://docs.straza.ai/guides/serve-mcp-apps/add-a-server/, https://docs.straza.ai/guides/serve-mcp-apps/credentials/, https://docs.straza.ai/guides/serve-mcp-apps/caller-credentials/ and https://docs.straza.ai/guides/serve-mcp-apps/catalogs-per-role/.

## 0. Ask before you write

Get five answers from the person before the first file:

1. Where the server lives: a URL it serves MCP on, a command that speaks MCP over stdio, or a container image. The enterprise profile refuses a command server, so there a stdio server runs as its own service or pod and joins by its URL.
2. What credential it needs, and whether everyone shares one secret or each person uses their own token or sign-in.
3. Which tools each group of people needs. Read the real names later with `strazactl apps tools --app <server>`; never guess them.
4. Which calls need a person to decide first, and who that person is: a team of approvers, or the person themself on their own phone.
5. Who gets the role, by username.

## 1. The manifest

An app is one MCP server that Straza runs or reaches. Its manifest is one YAML document. A server published in the MCP registry converts from its `server.json`, offline:

```sh
strazactl apps import server.json -o github.yaml
```

`--runtime` picks remote, command or oci when the record offers several, and `--name` overrides the derived name. By hand, the shape is:

```yaml
apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: scout-tools           # the policy identity (apps: [scout-tools]) and the tool prefix scout-tools__<tool>; no underscore
server:                       # the server's registry record, kept verbatim; only name and version are checked
  name: scout-tools
  version: "0"
straza:
  runtime:
    kind: remote              # exactly one of remote, command, oci, with its block and no other
    remote: {url: http://demo-tools:3001/mcp}
  credential:
    kind: static              # none (default), static, token or oauth; see section 3
    inject: {as: header, name: X-Demo-Key, template: "{{secret}}"}
  exposure:
    tools: ["*"]              # cap on which tools exist at all, default ["*"]
  limits: {rps: 10, timeoutSeconds: 30}
```

| Runtime | Block | How Straza runs it |
|---|---|---|
| `remote` | `remote: {url, auth}` | Streamable HTTP to a server you run, health by MCP ping. `auth` defaults to `inject`, and `passthrough` is reserved and means no injection today. |
| `command` | `command: {exec, args, env, workdir}` | A child process over stdio, restarted with backoff. |
| `oci` | `oci: {image, sandbox, env}` | A container over stdio, with `sandbox` `default` or `none`. Refused at start where no container backend exists. |

Validate it before it goes in the draft. The check runs offline and answers `github.yaml: app OK`:

```sh
strazactl spec validate app -f github.yaml
```

The files `examples/app-remote.yaml`, `examples/app-command.yaml`, `examples/app-oci.yaml` and `examples/app-github-token.yaml` are validated shapes to start from. For a change to a server that runs, `strazactl apps export github > github.yaml` prints the stored manifest with every environment value and each secret masked, and installing it again keeps each masked value, so the change starts from what the server runs.

## 2. One draft for the whole server

Nothing here is installed one command at a time. Put the manifest of section 1, the roles of section 4, the access set of section 5 and, for a team, the approver role of section 6 in one directory, one YAML file each, for example `onboard/`. Section 8 checks the directory against live state, and the person publishes it as one draft. A publish applies the whole draft or nothing, so a refused line leaves live state as it was.

A role names real tool names, and only a running server can list them. For a server that is not installed yet, the person first publishes a draft that holds the manifest alone. That publish acts at once: Straza contacts a remote server, and it starts a command or container server on the Straza host, so the person reads the manifest before publishing it. No session reaches the server until a role has access to it. A server with a shared static secret lists no tools until that secret is stored, so the person then runs `strazactl apps secret set <server>` in their own terminal and types the value at its hidden prompt, as section 3 says. `strazactl apps tools --app <server>` then lists the real names, and the roles and the access set follow in a second draft. A server that takes each person's own token or sign-in, such as GitHub, may list nothing, because its upstream refuses a call that carries no credential. Take its tool names from the server's own documentation together with the person.

The person can also change one object at a time in their own terminal with `strazactl apps install -f`, `strazactl roles create <server>-<word> --app <server> --tools <tool>,<tool>`, which makes a role of the server and its access row in one call, and `strazactl policy apply` followed by `strazactl policy activate`. Each of those changes goes live on its own, so a half-built setup is live between them, and the role's approval set must be in force before the role is assigned.

## 3. The credential

The secret never enters a manifest, a chat, a `!` command or a file the agent writes, because all of those end up in the transcript. The person types it into a terminal of their own. Pick the kind by who the upstream should see. The kind goes in the manifest. The person stores the value once the manifest is published, since the server exists only then, which for a new server is after the first draft of section 2. A draft refuses any document that holds a secret, so a manifest never carries one.

| Kind | The upstream sees | Where the value comes from |
|---|---|---|
| `none` | nobody in particular | nothing to store |
| `static` | one shared identity for everyone with access | the administrator stores it once, and may override it per role |
| `token` | each person, with their own pasted token | each person connects once |
| `oauth` | each person, through their own sign-in at a provider | each person signs in once |

The `inject` block is required for every kind except `none`. `inject.as: header` is valid only for `remote`, and `inject.as: env` only for `command` and `oci`. `template` must contain `{{secret}}`. A call with no usable secret for the caller is denied before anything leaves the gateway.

For a static secret, the person runs the command in their own terminal, and it asks for the value at a hidden prompt, so the secret never lands in a command line that shell history keeps:

```sh
strazactl apps secret set scout-tools
strazactl apps secret set scout-tools --role scout-tools-readers
```

The second form stores an override that only that role uses. A script fills `STRAZA_SECRET_VALUE` from a secret manager instead, and the command then reads it without a prompt. `--value` also exists, but it puts the secret in the process arguments, so do not suggest it.

A per-person token, `kind: token`, is valid on a `remote` server only. Each person connects from their own terminal, and the prompt hides the input:

```sh
straza connect github
straza connect github --allow-agents
```

The server tests the token once and keeps a sealed copy. The person can also paste it on the Credentials tab of the self-service page at `/self-service/credentials`, and `--expires YYYY-MM-DD` records when it stops working. The manifest's `agents` field says what an agent with no token of its own runs on: nothing (`own`, the default), its sponsor's connection once the sponsor ran `--allow-agents` (`sponsor`), or the server's shared static secret (`shared`). An administrator or a sponsor acts for another user with `strazactl connect github --user <username>` in their own terminal. `examples/app-github-token.yaml` is the shape.

An oauth server needs its provider in the server configuration first, under `oauth.providers.<name>` with `clientId`, `clientSecretFile`, `authUrl`, `tokenUrl` and `scopes`, and strazad reads it at start, so the change needs a restart. The manifest then names that provider in `oauth: {provider, scopes}`. Each person runs `straza connect <server>`, which prints their credentials page; they sign in there in a browser. `agents: client_credentials` gives each agent a token of its own client at the provider instead.

## 4. The application role

Access attaches to a role of kind application, and an application role reaches exactly one server. A role is a Role document, the YAML that `strazactl roles export` prints, and its access row is the one entry of `spec.bindings`. An application role that reaches a server belongs to that server, so the identity manager shows it as that server's role. Its name starts with the server's name and a dash, `spec.server` names the server, and it goes away with the server. It names its tools one by one, and only a global admin may write `"*"` instead, which means every tool, including tools the server adds later:

```yaml
apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: github-readers
spec:
    kind: application
    server: github
    bindings:
        - app: github
          tools:
            - get_me
            - list_issues
```

A new application role that leaves `spec.server` out belongs to no server and is for policy rules only, so a draft that gives it an access row is refused. A second role of the same server carries the tools of another job:

```yaml
apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: github-devs
spec:
    kind: application
    server: github
    bindings:
        - app: github
          tools:
            - create_issue
            - get_me
            - list_issues
```

`spec.kind` is required, because a draft never picks a kind for you. A second entry in `spec.bindings` is refused with "Role github-devs lists 2 bindings, and an application role reaches one MCP server." People who work across servers get a business role that composes one application role per server in `spec.implies`:

```yaml
apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: developers
spec:
    kind: business
    implies:
        - github-devs
```

A business role is refused access of its own with "business role: it composes application roles and reaches tools through them. Give an application role access instead." A Role document is the whole role, its description, its access row and what it composes, so a document without `spec.bindings` takes the role's access row away when it is published. To change a role that exists, start from `strazactl roles export github-devs > github-devs.yaml`. `strazactl bindings list` shows every access row with its id.

## 5. What each call does: the role's access set

A tool the access row gives runs unless a policy denies or gates it. The gates of one role live in one PolicySet named after the role plus `-access`, priority 100, matching that role alone. The console's role editor reads and writes the same set, so a set written here shows up there. `policy.md` has the grammar, the approve block and the authoring loop, and `examples/scout-tools-readers-access.yaml` holds a hold and a ticket side by side. A hold on one tool for a team of approvers looks like this:

```yaml
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: github-devs-access
  description: Gates for the role github-devs
spec:
  priority: 100
  match:
    roles: [github-devs]
  rules:
    - id: github-create-issue-hold
      events: [tool.pre]
      tools: [mcp.call]
      apps: [github]
      toolNames:
        allow: ["create_issue"]
      effect: allow
      mode: approve
      approve:
        roles: [release-approvers]
        timeoutSeconds: 300
      reason: "Straza: creating a GitHub issue waits for a release-approvers decision"
```

Save it in the draft's directory and run the loop from `policy.md` on the local file:

```sh
strazactl policy validate -f onboard/github-devs-access.yaml
strazactl policy simulate --roles github-devs --tool mcp.call --app github --tool-name create_issue -f onboard/github-devs-access.yaml
```

The local validate checks the grammar and refuses a role the product reserves, a name that starts with straza- or mcp-admin-, in `match.roles`, and any of them but straza-admin in `approve.roles`. Any other name in `approve.roles` that is not an approver role, or a `match.roles` that names a business role, passes it, and `strazactl drafts check` in section 8 refuses it with a sentence that names the fix. Before the publish, simulate reads the access rows that are live, so a tool whose access row exists only in the draft reads as denied where no rule of the file fires. Simulate the tools the file gates now, and read the rest with the catalog preview of section 9 once the draft is published.

The built-in `straza` server has no access rows, so its tools `approval_request`, `approval_status` and `approval_await` need an explicit allow rule with `apps: [straza]`.

## 6. Who decides

There are two shapes, and picking the wrong one leaves a hold that nobody can decide.

For a team, put an approver role in the draft and name it in `approve.roles` as in section 5. After the publish, the person gives it to the people who decide. The requester never decides their own request while `selfApproval` is false, which is the default.

```yaml
apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: release-approvers
spec:
    kind: approver
```

```sh
strazactl assign release-approvers --user bob
```

For one person governing their own agent, leave `roles` and `deciders` out of the approve block. The request then goes to the person behind the call: an agent's sponsor, or for a person with no sponsor, that person themself, who confirms it on their own enrolled phone or browser.

```yaml
      mode: approve
      approve:
        timeoutSeconds: 300
      reason: "Straza: git push waits for your confirmation on your phone"
```

Do not give that person an approver role and name it in `approve.roles`: they would be the only holder, they may not decide their own request, and the hold would expire to a deny. The console and `strazactl approvals approve` refuse a person's own request too, because an agent on the same machine could run them; the enrolled phone or browser is the only place it is decided. The server setting `approval.unsignedOwnDecisions` lifts that refusal, and in doing so it lets an agent on the machine approve its own holds, so never suggest it.

## 7. Make the phone work

A phone needs three things: the Straza approver app, an enrollment, and an address it can reach. Push is a fourth, which brings the request to the phone while the app is closed.

To enroll a phone, the person mints an enroll token in their own terminal for whoever decides, with that person and the phone at hand, never you: an enrollment makes a device that decides holds. The token is single use and expires in ten minutes. The fields go to stdout and the QR is drawn on stderr.

```sh
strazactl approvals enroll-token alice
strazactl approvers list alice
```

The person scans the QR in the app, which keeps its signing key in the phone's secure hardware. `strazactl approvers revoke <device-id>` retires a lost phone, and a new one needs a fresh token.

The QR's `Servers:` line is the address the phone dials, and it must open from wherever the person stands. The standalone profile starts a phone listener on port 8443 with a certificate it made and the machine's LAN address, which works while the phone is on the same Wi-Fi. When that address is wrong, set `server.approverTLS.publicUrl` in the server configuration, or `STRAZA_APPROVER_TLS_PUBLIC_URL`, to an https address the phone can open, such as a VPN or tunnel hostname, restart strazad and mint again. A proxy that terminates TLS on a public hostname takes `server.approverPublicUrl` instead. The mint refuses only when the server found no address at all. A configured or loopback address is printed as it is, so read the `Servers:` line yourself: `127.0.0.1` or `localhost` there means the phone cannot reach the server, and the setting above must change before the person scans.

Push delivery is off until it is configured. Without it the app still finds a held call by polling every 15 seconds while it is open, so a person who is not looking misses the request and it expires. The hosted relay needs no vendor account and sends only a reference, never the command. strazad reads `straza.yaml` from the directory it starts in, or the file `--config` names, and every setting here also has an environment face:

```yaml
approval:
  push:
    relay:
      enabled: true
      tokenFile: /var/lib/straza/push-relay-token   # any path strazad may write; it mints the token there at first boot
```

The environment faces are `STRAZA_APPROVAL_PUSH_RELAY_ENABLED` and `STRAZA_APPROVAL_PUSH_RELAY_TOKEN_FILE`, and a change takes a restart. The direct lanes are at https://docs.straza.ai/guides/approve/push-and-connectivity/#choose-how-push-reaches-the-phone.

A browser can stand in for a phone. `strazactl assign straza-enroll-browser --user alice` lets alice open `/self-service/` on the server, sign in, and choose Enable this browser on the This browser tab. The same tab offers Add a phone to an account that holds `straza-enroll-mobile`.

When a rule holds a call, a local tool is answered at once with a deny that names the reference and where a person decides, and the agent retries the exact same call once after the approval. An MCP call through the gateway is held open instead, up to 120 seconds, and then gets the same pending reason.

## 8. Check the draft, then the person publishes it

Check the whole directory against live state. The check stores nothing, contacts no server and runs on the person's login:

```sh
strazactl drafts check -f onboard/
```

It lists each document with a mark, `+` for an object that does not exist yet, `~` for one that does and `-` for a removal. Then comes one line per finding: `refused` blocks the publish and says the fix, `widens` is a line the person acknowledges when they publish, `warning` says what will not work yet, and `unchecked` says what Straza could not check without contacting the server. The command exits 0 when the draft can be published as it is, 1 when a line is refused, and 2 when it could not run, and `--json` prints the server's whole answer instead. Fix every refused line in the files and check again.

Then hand the person two commands for their own terminal. The first stores the draft and prints its check and its number, 41 here, and the second publishes it:

```sh
strazactl drafts create -f onboard/ --note "Add the GitHub server for the platform team"
strazactl drafts publish 41
```

The note is the person's own words for whoever publishes, and Straza does not check it. Before it publishes, `drafts publish` lists each line that widens access and asks the person to acknowledge it. A line that publishing the old state again cannot undo asks them to type a word instead, such as the host that will receive the server's credential, `api.githubcopilot.com` for this server, and that word is theirs to type, never yours. The publish applies the whole draft or nothing. Its answer names each server with its status and the steps that remain, such as assigning the roles and each person's `straza connect`, and ends with the command that undoes it. `strazactl drafts show 41` prints the draft, its check and who gains which tool, and it runs on the person's login too, so you can read what the person is about to publish.

When a line is refused after the create, fix the files, and the person sends them again with `strazactl drafts update 41 -f onboard/`. When live state changed after the check, the draft is refused as stale with the object that moved, and no update clears that. The person checks it again with `strazactl drafts rebase 41`, which keeps what the draft changed and takes every other field from live state. A field that the draft and live state both changed needs the person's pick, `--pick "App/github metadata.description=draft"` to keep the draft's value or `=live` to take live's. `strazactl drafts revert 41` makes a new draft that undoes a published one, and the person publishes it the same way.

After the publish, `strazactl apps list` gives each server's status with the reason behind it, and `strazactl apps show github` gives one server in full: health, tools, who has access and how each tool runs, and the stored secrets as fingerprints. A server that needs a credential and has none stays degraded until section 3 is done. When a server stays down, `strazactl apps logs github` prints its recent log lines and `strazactl apps recheck github` probes it again at once. When a server stalls, the recheck can wait longer than strazactl's 30 seconds. strazactl then says that it sent the request in full and got no answer, and `strazactl apps show github` reads the reason the check recorded. A file in the server's apps directory, the `apps.dir` setting, proposes a draft when it appears or changes and a removal draft when it goes, and the person publishes each with `strazactl drafts publish`. Such a server takes changes from the direct commands like any other.

## 9. Check, then assign

Preview what the role's sessions will see before anyone holds it. `--role` follows what a role composes, and `--app` filters to one server.

```sh
strazactl catalog preview --role github-devs --app github
```

| Status | Meaning |
|---|---|
| `visible` | The role has access and no policy gates the tool. |
| `approve_gated` | The role has access and a rule gates the tool behind a person. The reason names the set, the rule, the deciders and the wait, or for a ticket the decision window and how long the grant stays good. |
| `hidden_policy` | The role has access but a rule denies the tool. The reason names the set and the rule. |
| `matcher_miss` | The tool is not in the role's access row on this server. |
| `no_binding` | No role of the subject has access to the server. |
| `not_running` | The role has access, but the server is degraded, for example because it needs a credential and none is stored. |

When every tool reads as intended, assign the role last, then preview the real person:

```sh
strazactl assign github-devs --user alice
strazactl catalog preview --user alice --app github
```

A new session sees the tools as `github__<tool>`, and a running session picks them up at its next check-in. `strazactl unassign github-devs --user alice` takes the role back, as the console's Users page and the identity manager do. In an enterprise deployment the identity manager usually owns assignments, and `identity.md` covers it.

## The console does the same

The console's MCP servers page runs a four-step wizard: Server, Credential, Review, Check. It ends with a button that creates a role for the new server, and the role editor asks which tools the role reaches and what happens on each call, which writes the same `-access` set as section 5.
