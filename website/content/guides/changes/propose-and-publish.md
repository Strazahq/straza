---
title: Propose and publish a change
description: A change to an MCP server, a role or a policy set waits as a checked draft until a person publishes it, and a published change can be taken back with a second draft.
summary: >-
  A change to an MCP server, a role or a policy set waits as a draft that Straza checks against
  live state. Nothing in it is live until a person publishes it, and then all of it goes live
  together or not at all. An undo is a second draft that holds the state from before.
pagetype: how-to
weight: 10
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server on Linux. The admin ran every strazactl drafts verb shown except contact, which refused the admin API token it ran on. The admin saved, published, undid and discarded a draft and pressed Contact it now in headless Chromium, and a person's agent submitted a draft through straza mcp
  date: 2026-10-06
applies_to: both
who: Admin, and an agent that proposes a change
where: Console or a terminal
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

{{< clicks "Policies" "kubectl-apply-hold" "Change" "Secrets masked" "Apply" "Save draft" >}}

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
  warning   PolicySet/dev-access Neither a push lane nor Slack is configured, so the requests of rule hold-terraform-apply of dev-access show only on the console. Configure approval.push or approval.channels.slack, or keep someone watching Approvals.
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

Select a draft. The breadcrumb ends `Drafts / Draft 7`, and the page is titled with what the draft does, such as `Change policy kubectl-apply-hold`, beside its status, such as `Ready`. A row of pills under the title names the draft, `Draft 7`, then who drafted it and how long ago. After an AI agent a pill names its sponsor, such as `you sponsor it`, and after anyone else a pill says how the draft came in, such as `in the console`. **Details** opens the rest as facts: **Came from**, with the file a draft from the apps directory came from, **Revision**, **Last checked**, **May publish**, **Expires** for a draft that expires, and **Undoes** for an undo. When you may not publish, **May publish** reads `not you` and names what each object needs, such as `The policy set kubectl-apply-hold needs the scope policy:write.` A note from whoever drafted it shows in a dashed box with the author's name, `in their own words`, or `in its own words` for an AI agent, and the chip `unverified`, because nobody checked it.

{{< see >}}The band under the head, which stays in view while you scroll. Its chips count what the check found, such as `1 to confirm` or `2 warnings`, or read `3 checks passed` when it found nothing else. One pill says what stands before **Publish…**, such as `nothing to confirm` or `confirmed when you publish`, and **Discard** and **Publish…** end the band.{{< /see >}}

**In short** says what publishing does, one row per fact, such as `Adds` with the tag of a new policy set, `Gives` with a role, an outcome and a server, `Loosens` for a policy rule whose outcome gets looser, and `Affects` with how many hold the roles it touches. A published draft says `Added` or `Changed` and counts the holders under `Holders now`, with a button such as **Assign scout-tools-readers** for a role it added that nobody holds. A draft that In short cannot sum up, such as one with an object you may not read, shows **In this draft** in its place, a table of one row per object.

**What changes** counts the objects, such as `3 objects` beside `all or nothing`, and shows one card per object. Each card's head names the object with its kind, such as `MCP server` or `application role`, says when a server owns a role, such as `owned by ghapp`, and carries **View as YAML** and, for an object that is live, a link such as `Open kubectl-apply-hold`. A role's card lists its description, its holders, such as `nobody yet` or `2 holders`, and the tools it reaches behind Straza under their outcome. When the role changes, its tools show as chips: a green chip with `+` for a tool that joins, a struck red chip for one that leaves, and a long run of unchanged ones folded into a chip such as `9 unchanged`. For a policy set, the card says who the set applies to and whether it records, then lists its policy rules with their calls and outcome. A changed set lists only the policy rules that change, one line per field with its value now and after publishing, and folds the rest under a count such as `4 policy rules unchanged`. Server cards that change list only the fields that change, each as the old value, an arrow and the new value. **View as YAML** opens a sheet with a tab per file, such as `kubectl-apply-hold.yaml`, under the line `As it will be published.` There a server's file says `A value Straza keeps secret reads [REDACTED] here.` A card of an object the draft removes, or of one you may not read, has no **View as YAML**.

**Access after publishing** shows what a holder gets now and after publishing, `allowed`, `needs approval`, `no access` or `denied`, wherever a role somebody holds gains or loses a tool, a tool's outcome changes, or the objects it covers change. **Checks** counts what you confirm and the other findings beside its heading, such as `1 to confirm` and `2 warnings`, says how long ago it checked, and lists every line under **Refused**, **Widens access**, **Warnings** and **Not checked**. **Widens access** shows one row per tool and change: the tool on its server, the roles it changes for, the outcome now with who decides and the time, such as `needs approval`, `the agent's sponsor` and `2 minutes`, and the outcome after publishing. A changed policy shows one row per policy rule. Under the rows it says once how you confirm them, such as `Type kubectl-apply-hold when you publish.`, and for a draft that changes or removes an object that exists, whether `Publishing the old version again does not undo it.` **Details** under it lists the versions of the policy rules and the check codes. **Show** opens the **Passed** lines and the **Notes**, and a group with no line is left out.
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
ID  STATE  TITLE                      PROPOSER  DOOR        CHECKS       CHECKED               UPDATED
14  open   Add policy dev-access      admin     strazactl   1 warn       2026-10-06 20:13 UTC  2026-10-06 20:13 UTC
9   open   Change role dev            alice     straza app  no findings  2026-10-06 20:04 UTC  2026-10-06 20:04 UTC
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
Draft 14, revision 1, open: Add policy dev-access
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
  warning   PolicySet/dev-access Neither a push lane nor Slack is configured, so the requests of rule hold-terraform-apply of dev-access show only on the console. Configure approval.push or approval.channels.slack, or keep someone watching Approvals.
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


Straza does not dial an address that only a draft names, so the check cannot list that server's tools at first. It says `Straza has not contacted mcp.example.com yet, so the tool names of gitops-tools are unknown. It contacts an address in a draft only when a person asks.`, with your own address and server. In the console, press **Contact it now** beside that line. It opens one MCP connection with no credential, follows no redirect, starts nothing, and keeps the tool names on the draft for its check. The audit chain records who asked. Only a person contacts a server, so strazactl, which runs on an admin API token, has no way to.

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


A draft that pins a server's objects reads differently. The first pin gives every role already on the server `covers: every`, so nothing changes for any agent, and the check says so under **Notes**: `Pinning repos on ghapp keeps today's reach: ghapp-read and ghapp-write now cover every repo. Narrow them in this draft or later.` A warning names each tool the pinned block does not decide, which reads as reaching every repo until you name it, and another warns when the server has no listing credential yet. A type change while a role names repos of the old type is refused, and an unpin gets a note that names the roles that lose their lists. The draft's page shows the repos each role covers on ghapp as chips, now and after publishing, and a changed role's card marks a repo that joins with `+` and strikes one that leaves. `strazactl drafts show` prints the same as a table under `Repos on ghapp, now and after publishing:`. A covers line that makes a role someone holds reach more repos widens access even when its tools stay the same, so the check lists it under **Widens access** as a row of the repos the role covers now and after publishing, which you tick when you publish. `strazactl drafts show` prints its line: `ghapp-write-payments will reach more repos on ghapp: Strazahq/pay-web.` A role nobody holds raises no such line.

## Publish it

{{< console >}}

{{< clicks "Drafts" "Waiting" "Change policy kubectl-apply-hold" "Publish…" >}}

{{< shot name="draft-publish" caption="The publish question of a draft with no line to confirm." >}}

The question is titled with the draft, such as `Publish draft 7?`, and says what publishing does, such as `Publishing changes 1 policy.` A draft that adds one object and changes nothing else is titled with that object instead, such as `Publish scout-tools?`, and says nothing under the title. Roles that gain access and that nobody holds show as tags with the pill `nobody yet`. The lines that widen access come in blocks, one per kind, and you confirm each block. Each block names its objects as tags and says what its lines do with the rows of **Widens access**, such as a tool on its server with its outcome now and after publishing. Where no row says a line, the block shows the check's own sentence, such as `Straza will send github's secret to api.example.com, a host github has not used before.` A block carries a title, such as `Loosens 1 policy`, unless it is one line in the check's sentence or another block would carry the same title. Lines that publishing the old version again undoes have a box to tick, labelled with what the block says, and the lines of one kind share one tick. A line it cannot undo asks you to type its name under what the block says, such as `Type kubectl-apply-hold to confirm`. Several such lines of one kind ask for their count once instead, such as `Type 20 to confirm`. A server's removal always asks for the server's name. When the draft changes or removes an object that exists, one line under the blocks says whether publishing the old version again undoes the change. Each field shows what to type in its placeholder and a check mark once it matches.

Confirm each block and press **Publish**, which stays off until every block is confirmed.


A draft that adds a server whose credential is one shared secret with none stored asks for that secret in the same question, under the heading `Secret`, in a field such as `Secret of gitops-tools` with **Add it later** under it. A server that pins its objects and has no listing credential gets a second field, such as `Listing credential of ghapp`. **Publish** then stays off until each field holds a value or is ticked for later. After the publish the question stays open while Straza stores each value and checks the server, and it ends with the check's sentence, such as `Running.` A value that was not stored comes back as an empty field with **Store and check** or **Store the listing credential**, which stores it with no second publish. A value never goes into the draft, and a refused publish sends none. A value ticked for later is set on the server's page, under Server authentication, as [Credentials]({{< relref "guides/serve-mcp-apps/credentials.md" >}}) shows.

{{< see >}}`Draft 7 is live.`, with your own number, then a strip on the draft's page that reads `by`, your name, the time, `from the console`, the policy version and `signed`, with **Undo this publish** at its end.{{< /see >}}
{{< /console >}}

{{< cli >}}

strazactl does not publish, because it runs on an admin API token and only a person publishes a draft. Publish it in the console, as the console form of this step shows.
{{< /cli >}}

The server checks the draft again as it publishes. An object that changed since the last check refuses the publish, and nothing is written. [Who may draft and publish]({{< relref "guides/changes/who-may-draft.md#require-a-second-person" >}}) covers the refusal you meet when a second person must publish.

## Bring a stale draft up to date


A draft goes stale when live state changes under it, for example when someone changes the same role directly. Its check then refuses it with a sentence that names the change.

{{< fails >}}
`The role dev changed after this draft was checked, when admin published draft 6 at 2026-10-06 19:55 UTC. Check the draft again with Check again on the console under Drafts.`
: Check the draft again, as below. The numbers and the time are your own.
{{< /fails >}}

{{< console >}}
The draft's page shows `Out of date` with **Check again**. When the draft and live state both changed the same field, **Pick the values to keep** asks for each one, with `Draft:` and `Live:` before each value. Pick and press **Check again**.
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
{{< clicks "Drafts" "Published" "Change policy kubectl-apply-hold" "Undo this publish" >}}

{{< see >}}A new draft titled `Undo draft 7`, whose **Details** show the draft it undoes under **Undoes**.{{< /see >}}

A draft that a later publish replaced offers no **Undo this publish**. Its strip reads `Replaced` and names the later draft, with a button such as **Open draft 9**.
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

Type an optional **Reason** before you press **Discard draft**. On a draft from the apps directory that would remove a server, the question says that the server keeps running and that its link to the file ends.

{{< see >}}A row on the draft's page that reads `by`, your name and the time, with your reason under **Reason**.{{< /see >}}
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

`straza__draft_status` answers the check's refused lines, risks, warnings and unchecked lines, so the agent reads a refusal such as `Strazahq/pay-old is not in ghapp's repo list.` itself. Its `next` field then reads `Fix the refused lines and submit again with draft 9.`, and the agent sends the corrected document with `draft` set to that number. It never reads the **Notes**, and it cannot publish. The check does not refuse an agent's draft that widens what the agent's own roles cover. It raises a line that widens access, which a person accepts when publishing. An agent that holds ghapp-read-payments and sends it again with `covers: every` reads the risk `This draft lets the agent that wrote it reach file_get, pr_list, repo_get and search_code on every repo of ghapp, which widens the agent's own reach.` Its `next` field then reads `Draft 9 is stored and waits for a person to publish it on the console under Drafts. It changes your own access, so that person has to accept the change when publishing. You cannot publish it.` A third repo in the list reads `on repos of ghapp that its roles do not cover today` in place of `on every repo of ghapp`. A re-pin or an unpin that widens the agent's reach raises the same line. The check reads every combination of roles the agent holds, now and at each later moment one of its assignments starts or ends, and raises the line when any of those combinations comes to reach more. Naming a covered repo by its new name, or narrowing the list, raises nothing.


A draft that changes a policy set that binds the agent is not refused either, and a person may accept it instead. Such a draft turns the set off, removes it, changes its match or priority, takes away a role through which the set applies to the agent, loosens one of its gates, or adds or widens one of its rules. Each such set becomes a line that widens access, such as `agent-guardrails binds the agent that wrote this draft, and this draft turns it off.`, and the agent reads that line too. On the console, the draft's page carries a red warning that names the agent, such as `joe wrote this draft to loosen its own rules.`, or `joe wrote this draft to widen its own reach.` for a wider reach, and ends it with `A person may accept it when publishing.` Publishing in the console asks for the set's name, `Type agent-guardrails to accept it`, or for the server's name for a wider reach, and the `draft.publish` record names the person who accepted it as its `actor`. It lists an accepted set in `agentLoosened`, and an accepted wider reach shows as its key under `typed`. While `admin.secondPerson` is on, the agent's sponsor cannot publish such a draft, so another administrator must.

## Next {.nostep}

- [Who may draft and publish]({{< relref "guides/changes/who-may-draft.md" >}}) sets who may publish, and when a second person must.
- [The apps directory]({{< relref "guides/serve-mcp-apps/gitops-apps-directory.md" >}}) turns a file in a watched folder into a draft.
- [Policies]({{< relref "guides/console/policies.md" >}}) walks the policy editor that saved the console example.
