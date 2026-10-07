---
title: Knowledge packs
description: A role carries a knowledge pack, and every agent session of that role starts with the pack's text in its context.
pagetype: how-to
weight: 90
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine Linux container against a standalone server, with strazactl as the admin and the session start piped into straza hook as alice. The console steps were read in the console source, not clicked
  date: 2026-10-06
applies_to: both
who: You, as the admin
where: A terminal, the console, and the agent's machine
steps: true
modes: [console, cli]
mode_default: cli
keywords: knowledge pack context role session start bind unbind
---


A knowledge pack is a piece of text you write once and bind to a role. Every agent session of a person who holds that role starts with the text in its context, right after Straza's banner. Use it for what an agent should know about your team, such as where it may deploy and which tests to run. A pack tells the agent things and enforces nothing, so a rule that must hold belongs in a policy.


Creating, binding, unbinding or deleting a pack writes no audit record today.

## Before you start {.nostep}


- `strazactl` logged in as an admin. Packs belong to the identity area of the admin API, so a delegated admin needs write access to that area.
- A role of kind application or business to carry the pack. The examples use `dev`, which alice holds.
- A machine enrolled as alice, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows, to see the result.

## Write the pack


A pack is plain text, and Markdown reads well to a model. Save the text each session should start with as `team-conventions.md`. Keep it short, because it rides in the context of every session of the role.

```markdown
Use the staging cluster for every deploy. Production deploys go through the release pipeline, never from a laptop.
Run the unit tests before you propose a commit.
```

## Create the pack

{{< only form="cli" >}}The console binds and unbinds packs and cannot create one, so this step runs in a terminal.{{< /only >}}


{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl packs create team-conventions --file team-conventions.md
```
{{< /command >}}

{{< see >}}`created pack team-conventions (01a112c9-a801-7d18-9f24-b211accab334)` with your own id.{{< /see >}}


A pack's name is unique. `--version` sets the label the agent sees next to the name, `1` by default, and `--content` takes the text inline in place of a file. `strazactl packs list` shows each pack with its version and size:

```table
NAME              VERSION  BYTES  ID
team-conventions  1        163    01a112c9-a801-7d18-9f24-b211accab334
```

{{< fails >}}
`pack already exists`
: A pack of that name exists. Pick another name, or replace the pack as the last step shows.
{{< /fails >}}

## Bind it to a role

{{< console >}}

Open the role's page, go to its `Knowledge packs` tab, pick the pack and bind it.

{{< clicks "Roles" "dev" "Knowledge packs" "Pick a pack" "team-conventions" "Bind pack" >}}

{{< shot name="role-packs" caption="The **Knowledge packs** tab of `dev`, with `team-conventions` picked and **Bind pack**." >}}

{{< see >}}`dev receives team-conventions now.`{{< /see >}}

The tab shows on the page of an application or business role once at least one pack exists. The New role wizard has a `Knowledge packs` step as well. A draft never binds a pack, so the wizard binds the packs you pick when you press `Save and publish`.
{{< /console >}}

{{< cli >}}

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl packs bind team-conventions dev
```
{{< /command >}}

{{< see >}}`bound the knowledge pack team-conventions to role dev`{{< /see >}}
{{< /cli >}}


Each session receives the packs of every role its person holds, a role held through another role that composes it included. So a person who holds a business role `web-team` that composes `dev` receives `team-conventions` as well. Approver roles and Straza roles carry no packs.


{{< fails >}}
`approver role: it decides approval requests and cannot carry knowledge packs`
: Bind the pack to an application or business role instead.

`Straza role: it governs Straza itself and cannot carry knowledge packs`
: Bind the pack to an application or business role instead.

`no pack named "team-conventions"`
: strazactl finds a pack by its name. Check the name with `strazactl packs list`.
{{< /fails >}}

## See it at the next session start


A pack reaches an agent when its session starts. On alice's machine, open a session the way a harness does when it starts.

{{< command terminal="Terminal" purpose="on alice's machine" >}}
```sh
printf %s '{"hook_event_name":"SessionStart"}' | straza hook --harness claude-code
```
{{< /command >}}

{{< see >}}The context ends with `# Straza knowledge packs (delivered by role)` and the pack under its name and version.{{< /see >}}

The context the agent receives, decoded from the hook's JSON answer, reads:

```text
Straza governance is active for this session. You are operating as "alice" (roles: dev) against http://127.0.0.1:8420; policy snapshot 72ef15aad404, attestation advisory. Tool use is checked locally against signed policy and every decision is audited. A denied tool call always carries its reason: relay it to the user and do not retry or work around the denial.

# Straza knowledge packs (delivered by role)

## team-conventions (v1)

Use the staging cluster for every deploy. Production deploys go through the release pipeline, never from a laptop.
Run the unit tests before you propose a commit.
```


A session that is already running keeps the packs it started with, so a pack you bind or unbind reaches the agent at its next session start. Packs arrive through the session-start hook of Claude Code, Codex and Gemini. A client that reaches Straza only through the gateway, with `straza mcp`, receives no pack.

## Unbind the pack

{{< console >}}

{{< clicks "Roles" "dev" "Knowledge packs" "Unbind" "Unbind pack" >}}

The dialog says `Sessions holding dev stop receiving team-conventions at their next check-in. The pack itself is kept.`

{{< see >}}`dev no longer receives team-conventions.`{{< /see >}}
{{< /console >}}

{{< cli >}}

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl packs unbind team-conventions dev
```
{{< /command >}}

{{< see >}}`unbound the knowledge pack team-conventions from role dev`{{< /see >}}
{{< /cli >}}

## Delete or replace the pack

{{< only form="cli" >}}The console cannot delete a pack, so this step runs in a terminal.{{< /only >}}


Delete a pack once no role carries it. Without `--yes`, strazactl asks before it deletes.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl packs delete team-conventions --yes
```
{{< /command >}}

{{< see >}}`deleted knowledge pack team-conventions`{{< /see >}}

{{< fails >}}
`the knowledge pack is still bound to the role dev, and deleting it would change what that role's sessions receive. Unbind it from the role first, then delete it`
: Unbind the pack from each role the sentence names, then delete it.
{{< /fails >}}


A pack has no edit. To change its text, unbind it, delete it, create it again from the new file, and bind it again. Sessions that start after that receive the new text.

## Next {.nostep}

- [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) quotes the banner a pack follows and every answer an agent reads from Straza.
