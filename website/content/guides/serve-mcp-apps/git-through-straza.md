---
title: Git through Straza
description: Let agents run plain git against GitHub with no GitHub credential on their machines, while Straza decides each fetch and push on ghapp with the roles, repositories and approvals of any tool call.
summary: >-
  An agent runs plain git against GitHub with no GitHub credential on its machine. `straza
  service` asks Straza for a grant per fetch or push, which Straza decides as a call of ghapp's
  `git_fetch` or `git_push` with the roles, repositories and approvals of any tool call. ghapp
  then runs it with a GitHub token that never leaves ghapp.
pagetype: how-to
weight: 33
draft: false
tested:
  version: v1.0.0-1592-g4ede0baf
  platform: The Linux steps were fired on the enterprise eval stack on 2026-10-24 at v1.0.0-1592-g4ede0baf with a ghapp build of the same day, as the agent joe-java-developer-agent in a Linux harness container with git 2.39.5 and no GitHub credential. The clone, the push, the hand-off of a public repo, a submodule, the refusal of a push to an uncovered repo, a held push with its wait lines and its expiry, and the grant records all ran. The git block and the roles went in through the admin API rather than this page's drafts, and the hold used a 90-second window. The manifest and the PolicySet on this page passed strazactl spec validate built from the working tree of that version, and the two manifest refusals quoted here are that build's own output. The Windows lines were written on 2026-10-24 from the working tree before their commit. On 2026-10-26 a Windows 10 machine with the MSI ran git clone, a push, the expiry of a held push and an approved push through the helper, in a run that did not follow this page's steps. The macOS lines were written on 2026-10-26 from the working tree. On 2026-10-26 a macOS 26.6.2 virtual machine with an unsigned package built from the source tree at v1.0.0-1610-g366fac50 pushed to GitHub through the helper, in a run that did not follow this page's steps. The ghapp settings and start lines were read from ghapp's code on 2026-11-01
  except: the Windows and macOS steps, run only outside this page, and ghapp's settings and start lines, read from its code
  date: 2026-10-24
applies_to: both
who: Admin, and the people whose agents run git
where: Terminal with strazactl, ghapp's settings, and an agent's Linux, macOS or Windows machine
steps: true
keywords: git push fetch clone git grant ghapp github git-remote-straza remote helper host file approval repo force push ruleset windows msi path macos pkg
---


An agent runs plain `git clone`, `git fetch` and `git push` against GitHub, and no GitHub credential exists on its machine. Git hands each command to `straza service`, which asks your Straza server for a git grant for that one repository. Straza decides each fetch and push as a call of ghapp's `git_fetch` or `git_push` tool, with the same roles, repositories and approvals as any other tool call. ghapp then runs the fetch or the push with a GitHub token that never leaves it.


This page turns that on for ghapp, the GitHub App server whose repositories [Limit a role to some objects]({{< relref "guides/serve-mcp-apps/limit-a-role-to-some-objects.md" >}}) pins and lists. You map git to two of ghapp's tools, give roles those tools on the right repositories, and can hold each push for a person. Nothing is configured on the agents' machines. At the end, an agent pushes to a repository its role covers, and a push to any other repository is refused before git sends any of it.

## Before you start {.nostep}


- ghapp added to Straza with its repositories pinned and listed, and the roles `ghapp-read` and `ghapp-write-payments`, as [Limit a role to some objects]({{< relref "guides/serve-mcp-apps/limit-a-role-to-some-objects.md" >}}) sets them up.
- An https address at which the agents' machines reach ghapp's git endpoint, such as `https://ghapp.example.com`. ghapp's MCP endpoint stays where strazad reaches it.
- Every replica of your Straza server at this version. A replica of an earlier version refuses a manifest with a git block, and one that reopens a stored manifest drops the block.
- Agents on Linux machines with `straza service` from the `straza` deb or rpm package, which also installs `/usr/bin/git-remote-straza`, or on Windows machines with `straza service` from the Straza MSI. The MSI also installs `C:\ProgramData\straza\bin\git-remote-straza.exe` and adds that folder to the machine PATH. Macs run `straza service` from the Straza package for macOS, which links `/usr/local/bin/git-remote-straza` to its binary, and git there finds the helper only through the PATH of the agent's processes. A Terminal's PATH holds `/usr/local/bin`. launchd's bare PATH does not, so git started with it stops with git's own "'remote-straza' is not a git command", as a fire on macOS 26 showed. Claude Code is wired as [Claude Code]({{< relref "guides/govern-an-agent/claude-code.md" >}}) shows, with either install, and git is version 2.31 or later.
- After the MSI installs, people sign out and in once, so agent processes started afterward see the new PATH. An agent window that was open before the install cannot find `git-remote-straza.exe`, and its git fails until the window starts again.
- A shell policy that allows the agents' `git` commands. The harness hook decides `git push` as a shell command before git starts, and under the enterprise profile a local tool that no rule allows is denied, as [the profile comparison]({{< relref "reference/standalone-and-enterprise.md#no-rule" >}}) shows. The starter of [Give agents their local tools]({{< relref "guides/operate/enterprise-shape.md#give-agents-their-local-tools" >}}) allows shell commands for the holders of its role.

## Turn on Straza's git grants in ghapp


ghapp checks every git grant itself, so it needs two settings beside the ones it already runs with:

| Setting | Value |
|---|---|
| `GHAPP_PUBLIC_URL` | The address of ghapp's git endpoint, here `https://ghapp.example.com`. Every grant names it as its audience, so the manifest's `straza.git.url` in the next step names the same address. |
| `GHAPP_GIT_GRANT_ISSUER` | Your Straza server's `server.publicUrl`, character for character. ghapp reads Straza's signing keys at that address followed by `/.well-known/straza/jwks.json`, so ghapp must be able to reach it. |

Restart ghapp with them. Its start log then names the git endpoint in a line of the form `git transport at <url>/git/ to <upstream>, pushes up to <n> bytes, <t> per request`, and the grants in a line of the form `git transport takes Straza grants from <issuer>, with keys from <jwks url>`. ghapp's git endpoint takes Straza's git grants only. Its `git_fetch` and `git_push` tools hand out no ticket: they check the repository and answer with the plain git command for it.

ghapp no longer has git tickets, so leave `GHAPP_GIT_TICKETS` out. A deployment that still sets it to `false` starts with a log line that says the setting does nothing and can be removed, and ghapp refuses to start when it is `true`.


With its git endpoint on, ghapp also lists `git_fetch` and `git_push`, each marked with the argument `repo`. Until the next step pins them, Straza reads both as reaching every repository, so only a role on every repository could call them.

## Map git to ghapp's tools


A manifest's `straza.git` block tells Straza which git host a server serves and which tool decides each git operation. Add it to ghapp's manifest, and pin `git_fetch` and `git_push` to the argument `repo` in the objects block, because each git grant names one repository. Start from the manifest file you keep for ghapp, or from `strazactl apps export ghapp`, whose masked values [The apps directory]({{< relref "guides/serve-mcp-apps/gitops-apps-directory.md" >}}) tells you how to restore. With the rest of the manifest as it was, its objects block and the new git block read:

```yaml
straza:
  objects:
    type: repo
    list:
      tool: repo_list
      attributes:
        - {name: team, setBy: org}
    tools:
      branch_list: {argument: repo}
      file_get: {argument: repo}
      git_fetch: {argument: repo}
      git_push: {argument: repo}
      issue_comment: {argument: repo}
      issue_create: {argument: repo}
      issue_get: {argument: repo}
      issue_list: {argument: repo}
      pr_comment: {argument: repo}
      pr_create: {argument: repo}
      pr_get: {argument: repo}
      pr_list: {argument: repo}
      pr_merge: {argument: repo}
      repo_get: {argument: repo}
      repo_list: all
      search_code: {argument: repo}
      search_issues: {argument: repo}
  git:
    url: https://ghapp.example.com
    upstream: https://github.com
    ops:
      fetch: git_fetch
      push: git_push
```

`url` is ghapp's git address, the value of `GHAPP_PUBLIC_URL`. `upstream` is the git host whose repositories ghapp serves. Straza takes a repository URL as ghapp's when it starts with `https://github.com/`, reads the owner and the name from the next two path segments, and finds that repository in ghapp's list without regard to case. `ops` decides a fetch as a call of `git_fetch` and a push as a call of `git_push`. Both URLs use https, or http on a loopback host only, and Straza stores them with a lower-case host, no default port and no trailing slash.

Check the file, then propose it as a draft:

{{< command terminal="Your terminal" purpose="administration" >}}
```sh
strazactl drafts check -f ghapp.yaml
strazactl drafts create -f ghapp.yaml --note "Serve git for github.com through Straza's git grants"
```
{{< /command >}}

`drafts create` prints the new draft's number. A person publishes the draft in the console under **Drafts**, because strazactl runs on an admin API token and a token never publishes.


strazad's manifest parser, which the check and `strazactl spec validate app` both run, refuses a git block that names a tool the objects block does not pin to an argument. For a manifest that leaves `git_push` out of `objects.tools`, it says `git.ops.push names git_push, which objects.tools does not pin, so it reaches every repo. A git grant names one repo, so pin git_push under objects.tools to {argument: <name>} with the argument that carries the repo`. Without an objects block it says `the git block needs an objects block beside it, because each git grant names one repo from the server's object list. Add objects with the object type, the listing tool and {argument: <name>} for each tool under git.ops`.


From the publish on, every session start and session renewal answers each agent machine with `https://github.com` as a git host, and `straza service` writes it into the machine's host file. [How git reaches Straza](#how-git-reaches-straza) says what that file does.

## Give roles the git tools


A role that binds `git_fetch` lets its holders clone and fetch the repositories it covers, and `git_push` lets them push there. Add `git_fetch` to `ghapp-read`, which covers every repository, and `git_push` to `ghapp-write-payments`, which covers the two repositories of the team `payments`:

{{< command terminal="Your terminal" purpose="administration" >}}
```sh
mkdir -p ghapp-roles
cat > ghapp-roles/ghapp-read.yaml <<'EOF'
apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: ghapp-read
spec:
    kind: application
    description: Read tools of ghapp on every repo
    server: ghapp
    bindings:
        - app: ghapp
          tools:
            - branch_list
            - file_get
            - git_fetch
            - issue_get
            - issue_list
            - pr_get
            - pr_list
            - repo_get
            - search_code
            - search_issues
          covers: every
EOF
cat > ghapp-roles/ghapp-write-payments.yaml <<'EOF'
apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: ghapp-write-payments
spec:
    kind: application
    description: Write tools of ghapp on the payments repos
    server: ghapp
    bindings:
        - app: ghapp
          tools:
            - git_push
            - issue_comment
            - issue_create
            - pr_comment
            - pr_create
            - pr_merge
          covers:
            objects:
                - ghapp-straza-test/pay-api
                - ghapp-straza-test/pay-web
EOF
strazactl drafts check -f ghapp-roles/
strazactl drafts create -f ghapp-roles/ --note "Git fetch on every repo, git push on the payments repos"
```
{{< /command >}}

Publish the draft as before. An agent that holds both roles can now clone every repository ghapp lists and push to `ghapp-straza-test/pay-api` and `ghapp-straza-test/pay-web`.


A push to any other repository is refused at its first step, before git sends a commit, with the object check's sentence, such as `Straza: git_push on ghapp-straza-test/site was refused: none of your roles on ghapp covers that repo. Your roles cover git_push on ghapp-straza-test/pay-api and ghapp-straza-test/pay-web. Check the repo's name, or ask an admin for a role that covers it.` A person whose roles bind `git_fetch` and not `git_push` reads `Straza refused this git push: none of your roles allows git_push on the MCP server ghapp. Ask an administrator for a role that allows git_push on ghapp, then run git again.`


Straza serves a repository to a person only when one of their roles binds a git tool on ghapp and ghapp's list holds the repository. Any other repository goes to GitHub directly, through git's own https transport. A public repository then clones as it always did, and a private one or a push fails at GitHub with git's own message, because the machine holds no GitHub credential. When ghapp could serve the repository but its list does not hold it yet, Straza also asks ghapp for its list again, at most once a minute, so a repository created a moment ago works when git runs again a minute later.

## Hold a push for a person


This step is optional. A rule on `git_push` with `mode: approve` holds each push until a person allows it, here a holder of the approver role `release-approvers` that [Hold a call for a person]({{< relref "guides/write-policy/hold-for-a-human.md" >}}) sets up:

```yaml
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: ghapp-git-push-hold
  description: A release approver allows each git push through ghapp
spec:
  priority: 10
  match:
    roles: [ghapp-write-payments]
  rules:
    - id: hold-git-push
      events: [tool.pre]
      tools: [mcp.call]
      apps: [ghapp]
      toolNames:
        allow: ["git_push"]
      effect: allow
      mode: approve
      approve:
        roles: [release-approvers]
        timeoutSeconds: 300
      reason: "Straza: a release approver allows each git push through ghapp"
```

{{< command terminal="Your terminal" purpose="administration" >}}
```sh
strazactl policy apply -f ghapp-git-push-hold.yaml
strazactl policy activate ghapp-git-push-hold
```
{{< /command >}}

A push asks once. Its first grant opens ghapp's list of branches and runs every check but the approval, and the approval runs for the grant that names the branch updates. The approval's arguments carry the repository, the branch when the push names one, and each update with its old and new commit id, so the approver's card shows them unless `approval.preview.enabled` is false. Keep the rule's default `binding: call`, which ties the approval to exactly those updates. With `binding: tool`, one approval would cover `git_push` on any arguments, so a different push to the same repository could run on it. A rule on `git_fetch` holds a clone or a fetch the same way.


{{< note title="The card cannot show a force push" >}}
Straza and ghapp see only commit ids. A force push that rewrites a branch names the branch's current commit as its old id, exactly as a push that only adds commits does, so it looks like any other update on the card and in the grant. Block force pushes at GitHub, with a branch rule on the branches you protect that the App cannot bypass.
{{< /note >}}


While the approval is open, git keeps waiting and prints a wait line at once and then every 5 seconds, such as `Straza: waiting for an approver to allow refs/heads/main on github.com/ghapp-straza-test/pay-api (295s left).` Once the approver allows it, the push goes on. A denial ends it with `Straza: approval denied by`, the approver's name and the request's reference, and a window that ends with no decision ends it with `Straza: approval request expired after 300s with no decision`, followed by the reference. git prints that sentence beside each branch of the push, on a `[remote rejected]` line.


Only a hold keeps git waiting. A rule with `class: ticket` answers the push at once with a refusal that names the pending ticket and the time a person has to decide, and tells the person to `Run the same git command again once it is approved.` The same push run again after the approval goes through.

## What a person sees {.nostep}


A person sets up nothing. Their agent runs git as before, and git prints its usual output. A wait line shows while an approval is open, and a refusal is one sentence from Straza that says what failed and what to do next. These come from the machine's side:

{{< fails >}}
``Straza: git cannot reach `straza service` on this machine, so this git command did not run. Run `straza doctor` to see why.``
: The service is not running, or the machine has none. `straza doctor` names the cause.

``Straza: the `straza service` on this machine has no git route, because it is older than this straza or has not restarted since an upgrade, so this git command did not run. Restart `straza service` or upgrade Straza on this machine, then run git again.``
: `straza service` runs an older binary than git's helper, such as a service that has not restarted since an upgrade.

`Straza: the Straza server at https://straza.example.com does not give git grants yet. Ask an administrator to upgrade it, then run git again.`
: The machine's Straza server runs a version without git grants, with your server's address in place of the example.

`Straza: git archive --remote does not go through Straza. Clone the repo and run git archive locally.`
: Straza relays clones, fetches and pushes only.
{{< /fails >}}


Each git grant is bound to the key of the session that asked for it, taken from the verified session and never from the request.

## How git reaches Straza {#how-git-reaches-straza .nostep}


On Linux and macOS both Claude Code installs, and on Windows the managed install, write the same three entries into the `env` table of the Claude Code settings they already manage: `GIT_CONFIG_COUNT` `1`, `GIT_CONFIG_KEY_0` `include.path` and `GIT_CONFIG_VALUE_0` with the path of the host file. That path is `/run/straza-service/git/hosts.conf` on Linux, `/opt/straza/run/git/hosts.conf` on macOS and `C:\ProgramData\straza-service\git\hosts.conf` on Windows. Claude Code passes that table to the processes it starts, so git includes the file in agent processes only, and a person's own terminal never carries it. An install leaves every other entry of the table as it was.


`straza service` writes that file, readable by every local user and writable only by its own OS account, and it survives a restart of the service. On macOS the file sits in the `git` folder of `/opt/straza/run`, the socket folder that the package made for the service's account. On Windows the file sits in the `git` folder of the service's own state folder. SYSTEM and Administrators may change it too, and the service checks that folder's owner and rules before every write there. It writes nothing when another account could change the file, because git can start a program that the file names. With `https://github.com` as the one git host, the file reads:

```ini
[url "straza::https://github.com/"]
	insteadOf = https://github.com/
	insteadOf = git@github.com:
	insteadOf = ssh://git@github.com/
[protocol "straza"]
	allow = always
```


Git therefore hands every URL under `https://github.com/` to `git-remote-straza`, the `straza` binary under a second name, and an SSH remote of the same host too, so an agent needs no SSH key either. The protocol line lets submodules use the same transport. The helper asks `straza service` over its socket, or its named pipe on Windows, whether Straza serves the repository, and hands a repository Straza does not serve to git's own https transport untouched. For one Straza serves, the service holds the git grant in memory, never gives it to git, and streams git's requests to ghapp with the grant and a fresh proof from the machine's key.


An install whose server has no git block changes nothing for git: the host file stays empty, git rewrites no URL, and the helper never runs. The file lists exactly the hosts that some server's git block names, never every https URL, so Git LFS keeps working for every other host. A server added or removed later reaches a machine at its next session start or renewal, and a session that keeps working renews about 3 minutes after its last token was minted.

When the settings already held another value for one of the three entries, the install replaces it and says so on stderr, as in `NOTE: Straza replaced GIT_CONFIG_COUNT=2 in the env of /etc/claude-code/managed-settings.json with its own git settings, and uninstalling Straza does not put them back, so if you still need them, restore them from a backup of that file before you rely on this install.` A machine that upgrades the package or the MSI keeps its old wiring until you run the install again, `sudo straza install --managed --server https://straza.example.com claude-code` or `straza install claude-code`. On Windows the managed install runs as [Install the service on Windows]({{< relref "guides/operate/install.md#install-the-service-on-windows" >}}) shows, and the `install.cmd` of [Deploy with Intune]({{< relref "guides/govern-an-agent/deploy-with-intune.md" >}}) runs it with each upgrade. Until then, the `managed-content` line of `straza doctor` reports that the managed wiring does not match this build.

## Read the record {.nostep}


Each grant Straza issues and each refusal of its decision writes one `straza.audit.mcp` record with the event `grant`, the tool `git_fetch` or `git_push`, and the repository in `object`. Its `arguments` hold the repository and, on a push, the branch and each update. An issued grant adds `grant`, with the grant's id, its operation, its audience, its expiry and the approval when one ran. The record enters the audit queue before the grant leaves the server, so a grant whose record cannot be queued is never issued. Read the records with `strazactl audit tail --app ghapp --type straza.audit.mcp`. ghapp logs each git request on a line named `git` whose `grant_id` and `decision` are the grant's id and the record's id, so the two logs join. [Events and the audit record]({{< relref "reference/events.md#git-grants" >}}) lists every field.

## What it does not do {.nostep}


- A force push is not detected. Block it at GitHub with a branch rule that the App cannot bypass, as the note above says.
- An agent runs as the person, so it can use any git credential the person stored, such as a GitHub token in Git Credential Manager, by running git without the Straza settings. Remove stored GitHub credentials for the organizations Straza governs from machines that run agents.
- Policy decides per tool, not per branch. A role or a rule names `git_push`, no rule can name a branch yet, and an approval covers exactly the updates its card showed.
- ghapp refuses tags, deletes, refs outside `refs/heads/`, push options and signed pushes, each with its own reason beside the ref. GitHub refuses a push that changes a workflow file, because ghapp's token lacks the workflows permission.
- Git LFS does not work for a host in the host file, because git-lfs follows the same rewrite and fails.
- Git older than 2.31 ignores the settings and goes to GitHub with no credential, so a public clone works and a push or a private repository fails.
- In agent processes the three entries replace a person's own `GIT_CONFIG_COUNT` and its keys, from their environment or their settings.
- One push names at most a few dozen branch updates, because a git grant stays under 8000 bytes. A larger push stops with `Straza: this push names 60 ref updates, which is more than one git grant can carry, so it did not run. Push fewer branches at a time, for example one git push per branch.`
- The host file is one per machine, so on a machine where people enroll with different Straza servers, it follows the server that answered last.
- A `straza service` that runs as the user, with `STRAZA_HOME`, writes its host file to `$STRAZA_HOME/service/git/hosts.conf`, which the wiring does not name, so git on such a machine goes to GitHub directly.
- Only Claude Code on Linux, macOS and Windows is wired in this version. Codex CLI and Gemini CLI get no git wiring yet.
- On macOS, Claude Code's sandbox for shell commands, when an administrator turns it on, lets a command reach only the Unix sockets that its `allowUnixSockets` setting names. The wiring does not name the service's socket, so git through Straza fails in a sandboxed command there.

## Next {.nostep}

- [Events and the audit record]({{< relref "reference/events.md#git-grants" >}}) lists the fields of a grant's record.
- [The spec]({{< relref "reference/spec.md" >}}) links the git grant specification, for the author of another git resource server.
- [Hold a call for a person]({{< relref "guides/write-policy/hold-for-a-human.md" >}}) sets up the approver role and every approval window.
