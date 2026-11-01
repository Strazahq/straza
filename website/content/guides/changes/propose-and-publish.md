---
title: Propose and publish a change
description: A change to an MCP server, a role or a policy set waits as a checked draft until a person publishes it, and a published change can be taken back with a second draft.
pagetype: how-to
weight: 10
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server on Linux. The admin ran every strazactl drafts verb shown except contact, which refused the admin API token it ran on. The admin saved, published, undid and discarded a draft and pressed Contact it now in headless Chromium, and a person's agent submitted a draft through straza mcp
  date: 2026-10-06
applies_to: both
who: You, as the admin who changes Straza, and an agent that proposes a change
where: The console or a terminal
steps: true
modes: [console, cli]
mode_default: console
keywords: drafts propose publish check confirm acknowledge widens access revert undo discard rebase stale contact agent draft_submit straza-draft-config
---


A draft holds the documents of a change to MCP servers, roles and policy sets. Straza checks it against live state, and nothing in it is live until a person publishes it. Its documents then go live together or not at all. A change can come in through the console, through `strazactl drafts`, through a file in the apps directory, or from an agent through the built-in straza MCP server. Even a direct write such as `strazactl roles create` is a draft of one item that is checked and published in the same request.

This page follows one change from the draft to the publish, then takes it back. [Who may draft and publish]({{< relref "guides/changes/who-may-draft.md" >}}) decides who may do each step, and when a second person must publish.

## Before you start {.nostep}

- A console sign-in that may publish every object in your change, because only a person publishes a draft.
- For the CLI, `strazactl` on an admin API token in `STRAZA_API_TOKEN` that holds `drafts:read`, `drafts:write` and the read grant of each kind of object it drafts, minted in the console as [Mint an admin API token]({{< relref "guides/console/settings.md#mint-an-admin-api-token" >}}) shows. The token creates, checks, reverts and discards drafts, and it never publishes one.
- For the CLI, the documents of your change. `strazactl roles export <name>` prints a role, `strazactl apps export <server>` prints a server's manifest, and `strazactl policy show <name>` prints a policy set, so a change starts from what runs. The example below adds a policy set that holds `terraform apply` for the role `dev` until a holder of the approver role `release-approvers` decides.

## Write the change as a draft

{{< console >}}

Every editor in the console ends in **Save draft** and **Save and publish**: the policy page, the **New policy** and **New role** wizards, a role's **Edit access**, and the MCP server editors. **Save draft** keeps the change and changes nothing live. As an example, turn recording on for a policy.

{{< clicks "Policies" "kubectl-apply-hold" "Change" "Secrets masked" "Add to unpublished changes" "Save draft" >}}

{{< shot name="policy-unpublished" caption="The strip of unpublished changes on a policy's page, with **Save draft** beside **Save and publish**." >}}

{{< see >}}`kubectl-apply-hold is saved as a draft. The version that runs stays live until you publish it.`{{< /see >}}

The wizards and the role and server editors add each saved change to your open working draft, and the console header links that draft as `Your draft · 1 change`. The **Save draft** of a policy's own page stores the policy's new text as a draft of its own. **Drafts** in the sidebar counts the drafts that wait.
{{< /console >}}

{{< cli >}}

Save the policy set as `dev-access.yaml`.

```yaml
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: dev-access
  description: Gates for the role dev
spec:
  priority: 10
  match:
    roles: [dev]
  rules:
    - id: hold-terraform-apply
      events: [tool.pre]
      tools: [shell.exec]
      command:
        allowPatterns: ["terraform apply*"]
      effect: allow
      mode: approve
      approve:
        roles: [release-approvers]
        timeoutSeconds: 300
        retryTTLSeconds: 300
      reason: "Straza: terraform apply needs approval before it runs"
```

`strazactl drafts check` sends the documents for the same check a draft gets, and stores nothing.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl drafts check -f dev-access.yaml
```
{{< /command >}}

{{< see >}}`Nothing was stored. The documents can be published:` and the create command.{{< /see >}}

{{< details summary="Recorded output" >}}
```text
  +   PolicySet/dev-access
Checked against live state at 2026-10-06 20:13 UTC.
  warning   PolicySet/dev-access Rule hold-terraform-apply of dev-access holds calls, and neither a push lane nor Slack is configured, so a person learns of a request only on the console. Configure approval.push or approval.channels.slack, or keep someone watching Approvals.
Nothing was stored. The documents can be published: strazactl drafts create -f dev-access.yaml
```
{{< /details >}}

Store it as a draft, with a note for whoever reviews it.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl drafts create -f dev-access.yaml --note "Hold terraform apply for dev until release-approvers decides."
```
{{< /command >}}

{{< see >}}`Created draft 14 with 1 document. Nothing changes until a person publishes it.` with your own draft number.{{< /see >}}

`-f` takes a file, a folder of `.yaml` and `.yml` files, or `-` for standard input, and it repeats, so one draft can hold a server, the roles it owns and their policy set.
{{< /cli >}}

## Read the check

{{< console >}}

{{< clicks "Drafts" "Waiting" >}}

{{< shot name="drafts-waiting" caption="The **Waiting** tab, with the draft, who drafted it, how it came in and its check." >}}

The **Waiting** tab lists the open drafts with **What changes**, **Drafted by**, **How it came in** and the counts of the last check, such as `1 widens access` and `2 warnings`. **Published**, **Discarded** and **Expired** keep the drafts that are closed, and **Published** is the change record. **Mine** narrows the list to the drafts you wrote.

Select a draft. Its page opens with a status, such as `Ready to publish`, and the facts: **Drafted by**, **Came from**, each revision, **Last checked** and **Who may publish**, such as `You may publish all of it.` The help icon beside it names what each object needs, such as `The approval set kubectl-apply-hold needs the scope policy:write.` A note from whoever drafted it shows under a heading such as `What alice says`, or `What the agent says` for an AI agent, marked `unverified`, because nobody checked it.

The sections below follow in order. **What changes** shows each object as it would be published. **Access after publishing** shows, for each role the draft touches, what a holder gets now and after publishing: `allowed`, `needs approval`, `no access` or `denied`. **Checks** groups the server's findings under **Refused**, **Widens access**, **Warnings**, **Not checked** and **Passed**, and leaves out a group with no line. **Files** holds the text as it would be published, with secret values shown as `[REDACTED]`.

{{< see >}}The foot of the page says what to do before you publish, such as `1 line needs your confirmation when you publish.`{{< /see >}}
{{< /console >}}

{{< cli >}}

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl drafts list
```
{{< /command >}}

{{< details summary="Recorded output" >}}
```text
Open drafts, newest first.
ID  STATE  TITLE                        PROPOSER  DOOR        CHECKS       CHECKED               UPDATED
14  open   Add approval set dev-access  admin     strazactl   1 warn       2026-10-06 20:13 UTC  2026-10-06 20:13 UTC
9   open   Change role dev              alice     straza app  no findings  2026-10-06 20:04 UTC  2026-10-06 20:04 UTC
Counts are from each draft's last check. strazactl drafts show checks it against live state now.
```
{{< /details >}}

`--state` lists `published`, `discarded`, `expired` or `all` drafts instead of the open ones, and `--mine` keeps the drafts with a revision you wrote. `strazactl drafts show` checks one draft again now and prints who proposed it, the difference against live state, the verdict and what publishing needs.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl drafts show 14
```
{{< /command >}}

{{< details summary="Recorded output" >}}
```text
Draft 14, revision 1, open: Add approval set dev-access
Revision 1 by admin through strazactl at 2026-10-06 20:13 UTC.
The note, which Straza did not check: Hold terraform apply for dev until release-approvers decides.
  +   PolicySet/dev-access
--- live PolicySet/dev-access
+++ draft PolicySet/dev-access
@@ -0,0 +1,22 @@
+apiVersion: straza.dev/v1beta1
+kind: PolicySet
+metadata:
+  name: dev-access
+  description: Gates for the role dev
+spec:
+  priority: 10
+  match:
+    roles: [dev]
+  rules:
+    - id: hold-terraform-apply
+      events: [tool.pre]
+      tools: [shell.exec]
+      command:
+        allowPatterns: ["terraform apply*"]
+      effect: allow
+      mode: approve
+      approve:
+        roles: [release-approvers]
+        timeoutSeconds: 300
+        retryTTLSeconds: 300
+      reason: "Straza: terraform apply needs approval before it runs"
Checked against live state at 2026-10-06 20:13 UTC.
  warning   PolicySet/dev-access Rule hold-terraform-apply of dev-access holds calls, and neither a push lane nor Slack is configured, so a person learns of a request only on the console. Configure approval.push or approval.channels.slack, or keep someone watching Approvals.
  passed    The policy compiles with this draft.
  passed    Every approver role the draft's rules name exists and may decide.
  passed    No document holds a secret or the shape of one.
  Publishing PolicySet/dev-access needs the scope policy:write.
An admin API token carries no person and cannot publish a draft. It may create drafts. A person publishes them on the console under Drafts.
Publish draft 14 in the console under Drafts: http://localhost:8420/console/drafts/14
```
{{< /details >}}

The drafts verbs exit 0 when the documents can be published as they stand, 1 when the server looked and said no, and 2 when the command could not do its job. `list`, `show` and `discard` never exit 1.
{{< /cli >}}

A warning never blocks a publish. A refused line does, and it says what to fix. Correct the document and send it again: `strazactl drafts update <id> -f <file>` replaces the draft's documents with a new revision, and in the console you save the change again from its editor.

{{< fails >}}
`Role dev does not read as a Role document: line 5: field description not found in type metadata. Compare it with strazactl roles export dev and send the draft again.`
: The document does not match the shape the server stores. Start from `strazactl roles export dev`, change the field where the export has it, here `spec.description`, and send the draft again.
{{< /fails >}}

### When a draft adds a remote server {.nostep}


Straza does not dial an address that only a draft names, so the check cannot list that server's tools at first. It says `Straza has not contacted mcp.example.com, because an address in a draft is contacted only when a person asks, so the tool names of gitops-tools are unknown.`, with your own address and server. In the console, press **Contact it now** beside that line. It opens one MCP connection with no credential, follows no redirect, starts nothing, and keeps the tool names on the draft for its check. The audit chain records who asked. Only a person contacts a server, so strazactl, which runs on an admin API token, has no way to.

### When a draft limits a role to some objects {#covers .nostep}


Some servers pin an object type in their manifest, such as ghapp, which pins `repo`. A role on such a server says in its binding which repos it covers, with `covers: every` or `covers: {objects: [...]}`, as [Add a server]({{< relref "guides/serve-mcp-apps/add-a-server.md#covers" >}}) shows. The check resolves each named repo to its id in the list Straza last fetched from the server, so the publish binds the role to ids and a later rename keeps it covered. It refuses a covers line that cannot hold, in sentences that carry your own names:

{{< fails >}}
`ghapp-write-payments binds tools that work on repos. Say which repos it covers: every repo, or these repos by name.`
: The binding has no covers line. Its fix reads `In the role file, write covers: every, or covers: {objects: [<repo>, ...]}.`

`Strazahq/pay-old is not in ghapp's repo list. Compare the name with ghapp's Repos tab or strazactl apps objects ghapp. If the repo is new, refresh the list with strazactl apps objects refresh ghapp, then try again.`
: Spell the repo exactly as the server's list does. A repo made since the last listing appears after the refresh the sentence names, which runs at most once a minute per server.

`repo_list cannot be in a role on some repos: nothing keeps it to them. Make a role on every repo for it.`
: The tool can reach every repo at once, so only a role with `covers: every` may hold it. A tool marked `credential` is refused too, because Straza does not check a role's own login yet.
{{< /fails >}}

The check also refuses a covers line that names no repo, one that names a repo twice, and one on a server that pins no object type.


A draft that pins a server's objects reads differently. The first pin gives every role already on the server `covers: every`, so nothing changes for any agent, and the check says so under **Notes**: `Pinning repos on ghapp keeps today's reach: ghapp-read and ghapp-write now cover every repo. Narrow them in this draft or later.` A warning names each tool the pinned block does not decide, which reads as reaching every repo until you name it, and another warns when the server has no listing credential yet. A type change while a role names repos of the old type is refused, and an unpin gets a note that names the roles that lose their lists. The draft's page shows the repos each role covers on ghapp, now and after publishing, and its first sentence says what publishing changes, such as `Changes the role ghapp-write-payments: it will reach 1 more repo on ghapp, Strazahq/pay-web.` `strazactl drafts show` prints the same as a table under `Repos on ghapp, now and after publishing:`. A covers line that makes a role someone holds reach more repos widens access even when its tools stay the same, so the check lists it as a line you tick when you publish: `ghapp-write-payments will reach more repos on ghapp: Strazahq/pay-web.` A role nobody holds raises no such line.

## Publish it

{{< console >}}

{{< clicks "Drafts" "Waiting" "Change approval set kubectl-apply-hold" "Publish…" >}}

{{< shot name="draft-publish" caption="The publish question of a draft with no line to confirm, ending with the sentence that says who the publish record names." >}}

The question is titled with the draft, such as `Publish draft 7?`, and says what publishing does, such as `Publishing changes 1 approval set in one step, or nothing changes.` A draft with lines that widen access is said by what they do, such as `Publishing widens access through 1 approval set, or nothing changes.` The lines that widen access come in blocks, one per kind, and you confirm each block. A line that republishing the old state can undo has a box to tick, and the lines of one kind share one tick. A line it cannot undo asks you to type its name, such as `Type kubectl-apply-hold to confirm`. Several such lines of one kind ask for their count once instead, such as `Type 20 to confirm`, and the block folds open to list them. A server's removal always asks for the server's name. Each field shows what to type in its placeholder and a check mark once it matches. The question ends with `The publish record names you.`, and its help icon says that the record also names the console and your login.

Confirm each block and press **Publish**, which stays off until every block is confirmed.

{{< see >}}`Published by admin at 2026-10-06 20:03:49 UTC from the console. Draft 7 is live.`, with your own name, time and number, and **Undo this publish** under it.{{< /see >}}
{{< /console >}}

{{< cli >}}

strazactl does not publish, because it runs on an admin API token and only a person publishes a draft. Publish it in the console, as the console form of this step shows.
{{< /cli >}}

The server checks the draft again as it publishes. An object that changed since the last check refuses the publish, and nothing is written. [Who may draft and publish]({{< relref "guides/changes/who-may-draft.md#require-a-second-person" >}}) covers the refusal you meet when a second person must publish.

## Bring a stale draft up to date


A draft goes stale when live state changes under it, for example when someone changes the same role directly. Its check then refuses it with a sentence that names the change.

{{< fails >}}
`Role/dev changed after this draft was checked, when admin published draft 6 at 2026-10-06 19:55 UTC. Check the draft again with Check again on the console under Drafts. Straza keeps what this draft changed, takes every other field from live state, and asks you to pick where both changed.`
: Check the draft again, as below. The numbers and the time are your own.
{{< /fails >}}

{{< console >}}
The draft's page shows `Out of date.` with **Check again**. When the draft and live state both changed the same field, **Pick the values to keep** asks for each one, with `Keep the draft's value` and `Keep live's value`. Pick and press **Check again with these values**.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl drafts rebase 5
```
{{< /command >}}

When both sides changed a field, rebase stops and names the values.

```text
  Role/dev spec.description
    at the check: (not set)
    in the draft: Developers who run local tools
    on live: Developers
Pick each value and check again, as in strazactl drafts rebase 5 --pick "Role/dev spec.description=draft", or =live to take live's.
strazactl: Draft 5 and live state both changed Role/dev spec.description since the draft was checked. Pick which value to keep, then check again.
```

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl drafts rebase 5 --pick "Role/dev spec.description=draft"
```
{{< /command >}}

{{< see >}}`Checked draft 5 again against live state: it is at revision 2. Nothing changes until a person publishes it.`{{< /see >}}
{{< /cli >}}

## Take a published change back


An undo is a new draft that holds the state from before the publish. It is checked like any other draft, and it lists what cannot come back, such as each person's connection to a server.

{{< console >}}
{{< clicks "Drafts" "Published" "Change approval set kubectl-apply-hold" "Undo this publish" >}}

{{< see >}}A new draft titled `Undo draft 7`, with `Undoes draft 7` among its facts.{{< /see >}}
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl drafts revert 14 --note "Back out the terraform hold."
```
{{< /command >}}

{{< details summary="Output" >}}
```text
Created draft 15, which undoes draft 14. Nothing changes until a person publishes it.
  widens    PolicySet/dev-access dev-access will stop gating shell.exec as rule hold-terraform-apply does today, because this draft removes the set.
            before: hold-terraform-apply@66f57507ec06
            after:  removed
            Type dev-access to confirm it when you publish the draft in the console under Drafts.
Publish draft 15 in the console under Drafts: http://localhost:8420/console/drafts/15
```
{{< /details >}}
{{< /cli >}}

Removing a gate widens access, so publishing the undo asks you to confirm it, here by typing `dev-access`. Publish the undo as any other draft, or discard it to keep the change.

## Discard a draft


Discarding changes nothing live. The draft moves to **Discarded** with your reason, where whoever drafted it reads it.

{{< console >}}
{{< clicks "Drafts" "Undo draft 7" "Discard" "Discard draft" >}}

Type an optional **Reason** before you press **Discard draft**. On a draft from the apps directory the button reads `Discard: keep live as it is`, or names the server the draft would remove.

{{< see >}}`Discarded by admin at 2026-10-06 20:03:57 UTC. Nothing live changed.` and your reason.{{< /see >}}
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl drafts discard 15 --reason "Keep the hold." --yes
```
{{< /command >}}

{{< see >}}`Discarded draft 15.`{{< /see >}}
{{< /cli >}}

## Let an agent propose a change


An agent proposes a change through two tools of the built-in straza MCP server, `straza__draft_submit` and `straza__draft_status`. The gateway lists them only to a session that holds the role `straza-draft-config`, which the product creates. Give that role to the person or the AI agent whose sessions may propose.

{{< console >}}
{{< clicks "Roles" "straza-draft-config" "Assign to a user" "Assign role" >}}

Pick the person or agent under **User** before you press **Assign role**.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl assign straza-draft-config --user alice
```
{{< /command >}}
{{< /cli >}}

The agent sends the same documents a person would, as YAML texts, with a note for the reviewer. The tool answers at once with the draft's number. Here is the call an agent made in alice's session, through `straza mcp`, and its answer.

```json
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"straza__draft_submit","arguments":{"documents":["apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: dev\nspec:\n    kind: application\n    description: Developers who run local tools and kubectl\n"],"note":"The dev role now covers kubectl work, so its description says so."}}}
```

```json
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{\"checked\":false,\"draft\":\"9\",\"next\":\"Straza checks the draft now. Call straza__draft_status with draft 9 to read the check. A person publishes it on the console under Drafts, and you cannot.\",\"publishable\":false,\"revision\":1,\"state\":\"open\"}"}],"structuredContent":{"checked":false,"draft":"9","next":"Straza checks the draft now. Call straza__draft_status with draft 9 to read the check. A person publishes it on the console under Drafts, and you cannot.","publishable":false,"revision":1,"state":"open"}}}
```

`straza__draft_status` with the number reads the check, and its `next` field tells the agent what happens now, such as `Draft 9 waits for a person to publish it on the console under Drafts. You cannot publish it.` The draft waits under **Drafts** with `the straza app` as the way it came in, and you review and publish it as this page shows. An agent's open draft expires 14 days after its latest revision, as [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#changes-and-drafts" >}}) lists. A draft from the console, strazactl or the apps directory does not expire.


An agent limits a role to some repos the same way, with a covers line in the Role document it sends. This one gives the read tools of ghapp on two repos:

```yaml
apiVersion: straza.dev/v1beta1
kind: Role
metadata:
  name: ghapp-read-payments
spec:
  kind: application
  description: Read tools of ghapp on the payments repos
  server: ghapp
  bindings:
    - app: ghapp
      tools: [repo_get, file_get, pr_list, search_code]
      covers:
        objects: [Strazahq/pay-api, Strazahq/pay-web]
```

`straza__draft_status` answers the check's refused lines, risks, warnings and unchecked lines, so the agent reads a refusal such as `Strazahq/pay-old is not in ghapp's repo list.` itself. Its `next` field then reads `Fix the refused lines and submit again with draft 9.`, and the agent sends the corrected document with `draft` set to that number. It never reads the **Notes**, and it cannot publish. The check refuses an agent's draft that widens what the agent's own roles cover. An agent that holds ghapp-read-payments and sends it again with `covers: every` reads `This draft would let you reach file_get, pr_list, repo_get and search_code on every repo of ghapp, which widens your own reach. An agent's draft may not.`, with the fix `Leave that change out. A person can make it.` A third repo in the list reads `on repos of ghapp that your roles do not cover today` in place of `on every repo of ghapp`. A re-pin or an unpin that widens the agent's reach is refused the same way. The check reads every combination of roles the agent holds, now and at each later moment one of its assignments starts or ends. A draft that widens what any of those combinations reaches is refused the same way, and so is a draft that adds or widens a rule of a policy set that binds the agent at any of those moments. Naming a covered repo by its new name, or narrowing the list, passes.


A draft that loosens a policy set that binds the agent is not refused, and a person may accept it instead. Such a draft turns the set off, removes it, changes its match or priority, takes away a role through which the set applies to the agent, or loosens one of its gates. Each such set becomes a line that widens access, such as `agent-guardrails binds the agent that wrote this draft, and this draft turns it off.`, and the agent reads that line too. On the console, the draft's page opens with a red warning that names the agent, such as `joe wrote this draft to loosen its own rules.`, and ends it with `A person may accept it when publishing.` Publishing in the console asks for the set's name, `Type agent-guardrails to accept it`, and the `draft.publish` record names the person who accepted it as its `actor` and lists the set in `agentLoosened`. While `admin.secondPerson` is on, the agent's sponsor cannot publish such a draft, so another administrator must.

## Next {.nostep}

- [Who may draft and publish]({{< relref "guides/changes/who-may-draft.md" >}}) sets who may publish, and when a second person must.
- [The apps directory]({{< relref "guides/serve-mcp-apps/gitops-apps-directory.md" >}}) turns a file in a watched folder into a draft.
- [Policies]({{< relref "guides/console/policies.md" >}}) walks the policy editor that saved the console example.
