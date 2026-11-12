---
title: Overview and Audit
description: You read the deployment's health on Overview, then find any decision on Audit, check it against the hash chain and test the same call again.
summary: >-
  Overview is the first page of the console and lists what needs a person now, such as waiting
  approvals, failed servers or a broken audit chain. Audit is the hash-chained record of every
  decision and change, which your browser re-hashes as it loads. From any decision, Test this
  call asks the policy that is live now again.
pagetype: how-to
weight: 10
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server on Linux, read as the admin in headless Chromium after a person's machine had sent allowed, denied and held calls through the hook, with strazactl audit verify run from a terminal
  date: 2026-10-06
applies_to: both
who: Admin
where: Console
steps: true
keywords: console overview audit chain verified live tail lens search record test this call export verify
---


Overview is the first page the console opens, and it says what needs a person now. Audit is the hash-chained record of every decision and change, read in your browser. This page walks both as an admin uses them. Overview opens to a session that holds the `config` area, and Audit to one that holds the `audit` area, so a delegated admin may see one without the other, as [Delegated administration]({{< relref "guides/operate/delegated-admin.md" >}}) explains.

## Before you start {.nostep}

- A console sign-in as an admin, at `/console/` on your server with **Sign in**. [Try it on one laptop]({{< relref "evaluate/try-it-on-one-laptop.md" >}}) shows the sign-in on a standalone server.
- Some decisions on the chain, such as the deny and the allow of the first session.


{{< fails >}}
`This sign-in answer was not started in this tab, or it is more than 10 minutes old, so Straza did not use it. Press Sign in to start again.`
: The answer that came back from the sign-in was started in another tab, or more than 10 minutes ago. Press **Sign in** again.
{{< /fails >}}

## Read the summary


{{< clicks "Overview" >}}

The page opens on the **Summary** tab. The strip at the top counts the MCP servers running, the active sessions, the active users and the active policies. Under it, **Tool decisions** charts the calls the gateway and the hooks decided, with totals for allowed, denied and required approval. Switch its window between **Last 24 hours** and **Last hour** to see the last hour by the minute.


The console adds no users. People and agents arrive over SCIM from your IGA, or an admin creates them with `strazactl`.


The foot of the menu names the profile and the strazad build, and its tooltip adds the commit and the Go version.

{{< shot name="overview" caption="The **Summary** tab: the counts at the top, and **Tool decisions** with its switch between **Last 24 hours** and **Last hour**." >}}

Beside the chart, **Needs attention** lists only what a person should act on: approvals that wait, drafts that wait for review, MCP servers that failed or degraded, a broken audit chain, a push lane that is down, and sinks that hold parked events. For a full administrator it also names a client assertion key whose certificate expires within 30 days or has expired, when a provider uses Entra, and a key that has been staged for over a day without a promote. A line reads, for example, `1 draft waits for review, the oldest from alice, drafted 16 m ago.`, and each line opens the area where you act. Below it, the configuration note counts the settings that loosen the server, and **Review settings** opens them. A fresh standalone server shows `4 settings to review`, because its defaults suit a first try on one machine.

**Denied and held calls** lists the tools with the most denials or approval requests in the last 24 hours. **View reason** shows the reason recorded most often and a link to Audit filtered to that tool. The line at the foot, `Latest records verified (up to 25)`, is the chain check this page runs.

{{< see >}}The counts match what you expect of your deployment, and the foot line says `Latest records verified (up to 25)`.{{< /see >}}

## Read the activity and the system details


{{< clicks "Overview" "Activity" >}}

**Recent changes** lists the newest control plane changes in words, such as `Published a draft` or `Enrolled an approver phone or browser`, with who made each one. **Recent audit events** shows the newest records. Its **Follow** switch is off by default, and once you turn it on the list refreshes every 30 seconds. This browser remembers your choice.

{{< clicks "Overview" "System details" >}}

{{< shot name="overview-system" caption="**Security configuration** on System details, with each relaxed setting, what it costs and the key that sets it." >}}

The tiles count sessions, users, MCP servers, policies, waiting approvals and the newest record of the audit chain. **Security configuration** names each relaxed setting with what it costs, such as `Local tool default` with `A local tool no rule names runs.`, and links the row in Settings that says where to set it. **Active sessions** lists up to ten sessions with their harness, the client version and the hook configuration each one reported. A session whose client was built from a different commit than the server reads `differs from server` under its version, and its tooltip names the commit the session started on and the server's. The panel's header counts the marked sessions among the governed ones only, such as `1 client build differs from the server`, so a marked console or strazactl row is left out of it. The check compares commits, not version strings, because a release build and a build from a checkout spell the version differently. A session keeps the build it started on, so after a box upgrades, its open session stays marked until it ends. That is at most `governance.sessionMaxLifetime` after its start, 12 hours by default, and the box's next session shows the new build. Sessions that started before the server recorded commits carry none and are never marked.

## Open the audit chain


{{< clicks "Audit" >}}

Above the table, the status line reads `Chain verified`, with the count of records loaded and the head of the chain, such as `43 records loaded, head at seq 43`. The Overview card re-hashes the 25 newest records in your browser, and the Audit screen re-hashes every record it has loaded, starting with the newest 200. The table is a live tail that polls every second, and each new record is checked against the one before it as it arrives.

{{< shot name="audit-list" caption="The newest records on Audit. Each denied `rm -rf` has a red edge and a tinted row." >}}

Each row shows **Seq**, **Time**, **Who**, **Type**, **Effect**, **Action** and **Reason**. A denied call has a red edge and a tinted row. A shell command fits on one line in the **Action** cell: its commands joined by their operators, a here-document as its opener, such as `python - <<'EOF'`, and `+7 lines` beside it when the command has more lines. On a server that pins its objects, the **Action** cell names the object after the tool, as in `ghapp.pr_create on Strazahq/pay-api` followed by the arguments, and no column of its own holds it. A run of identical records folds into its newest row with `×2 identical · show all`, and the export and the chain check still read every record of the run. **Load older** reads the 200 records before the oldest one loaded and checks that they join it.

{{< see >}}`Chain verified` and a count of loaded records.{{< /see >}}

{{< fails >}}
`Audit chain broken at seq <seq>: a record fails re-hashing. Records may have been altered or removed. Verify with strazactl audit verify.`
: The console shows this alert, with the number of the first bad record, when a record it loaded fails the check. Run `strazactl audit verify` from a terminal, as the last step shows, and keep the database as it is for the investigation.

`Chain not verified` with `re-hashing needs a secure context (TLS or localhost)`
: The browser checks the chain only on a page served over TLS or from localhost. Open the console at an `https://` address, or at `localhost` on the server's own machine.
{{< /fails >}}

## Narrow the records


The **Lens** picker narrows the loaded tail to one kind of record: `all records`, `decisions`, `approvals`, `recording`, `sentinel`, `audit`, `revocations`, `identity`, `MCP servers` or `policy`. A note under the controls says what the lens covers, such as `decisions = type tool and mcp: the policy engine's verdicts on tool calls`.

The effect buttons, the search box and the **User** picker search the whole chain on the server instead. Pick **deny**, type a command, a path, a user or a rule into `Search the whole chain: command, path, user, rule`, or pick a person. A name in the **Who** column filters to that person as well. While a search runs, the status line reads `Whole-chain search` and the count of matches. Search results are not contiguous, so the browser does not re-hash them, and the tail underneath stays verified.

{{< clicks "Audit" "deny" >}}

{{< see >}}`Whole-chain search` and the denied calls, newest first.{{< /see >}}

## Read one record


Select a row. The sheet is titled with the record's number and type, such as `Record 24 tool`, and names who, when and the session. For a decision it opens on the outcome, `Denied.`, in its color, then the sentence `Decided by rule block-recursive-delete in policy standalone-starter:` followed by the reason the agent read. `Recorded at decision time.` reminds you that the record holds what the engine knew then, whatever changed since.


**What ran** follows, with two views. **Readable** opens first and lays the call out. A shell command reads as its commands, numbered, with the words for how each leads to the next between them, such as `then, only if it succeeded` for `&&` or `its output goes into` for `|`. Each redirection gets its words beside it, such as `errors into output` for `2>&1`. A here-document, such as a python script an agent feeds on its input, shows as its own numbered code block. **Paths named in the command** lists the files and folders the text names, which is what the command names, not proof of what it touched. Every character of the command stays on screen. **Raw** shows the command exactly as the record holds it, line by line, with **Wrap lines** and **Copy command**, and the browser remembers the view you picked last.

Readable never guesses. When it cannot split a command, such as one with a quote that never closes, it reads `Straza cannot split this command into steps:` and why, and shows the command as it ran. Characters that do not show on screen, such as a zero-width space or a right-to-left override, get a warning that names each one and its line, and both views mark them. A command the record cut says how much Straza kept and gives the SHA-256 of the whole command. For a gateway call the view is a table of its arguments, one row each. A harness tool whose record holds its input reads the same way, under the harness's own name of the tool, so a write shows the text it put in the file, shortened when it is long. An older client's record of a file tool holds only its paths, shown from the workspace with `The record holds the path. It does not hold what changed in the file.`

{{< shot name="audit-deny" caption="The record of alice's denied `rm -rf`: the outcome, the rule and policy that decided it, and what ran." >}}


A call that names a repository adds a row named by the server's type, such as **Repo**, with the value the call named and its id in the list, as in `id 812345678`, or `not in the repo list` when the list did not hold it. When the object check refused the call, no policy ran, so the sheet reads `No policy rule was recorded for this call.` and then the sentence the agent read, which starts with `Straza:` and names the tool, and the repository when the call named one. Under **The record as stored**, `rolesChecked` names the held roles that bind the tool.

The **Chain** line holds the start of the record's hash, **Copy hash** copies it whole, and the verdict of your browser follows, `Hash matches the loaded chain.` A record opened from a search reads `Not verified in this browser. See the audit chain status for details.` instead, because a search result is not part of the checked tail. **The record as stored** is folded, and opens on the whole record. **Copy as JSON** copies it.

## Test the call again


Open the record of a decision, then press **Test this call** at the foot of the sheet. It opens **Test a call** with the person and the call of the record filled in. It answers against the policy version that is live now, and it writes nothing and records nothing. Use it after a policy change to see whether the same call would now get a different answer. On a server that pins its objects, the test carries the record's repository too, unless the record cut a value longer than 512 bytes, and then Test a call asks you to pick the repository.

{{< shot name="audit-test-this-call" caption="**Test this call** at the foot of a record, under **The record as stored**." >}}

{{< see >}}An **Answer** such as `Denied`, with `Decided under the policy version live right now.`{{< /see >}}


A denied call that no rule decided, to an MCP server on a person's own machine or of a harness tool Straza does not classify, has **Write a rule for this call** at the foot of its sheet. The button shows when your session may write policy. It opens **New policy** with **Which calls** answered, as [Policies]({{< relref "guides/console/policies.md#build-a-policy-with-the-wizard" >}}) shows.

[Simulate a call]({{< relref "guides/write-policy/simulate-and-coverage.md" >}}) covers the same test from the policy side and from the CLI.

## Export or verify the whole chain


**Export loaded rows** downloads the rows the current lens shows, as `CSV` or as `JSONL`. The JSONL file holds each record as stored. The export covers what the browser has loaded, never the whole chain.

{{< only form="cli" >}}Only `strazactl audit verify` re-hashes every record of the chain, from the first to the head.{{< /only >}}

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl audit verify
```
{{< /command >}}

{{< see >}}`audit chain intact: 71 records verified`, with the count of your own chain.{{< /see >}}

## Next {.nostep}

- [The sentinel]({{< relref "guides/audit/sentinel.md" >}}) adds verdicts on whole sessions to the chain, under the `sentinel` lens.
- [Sinks and SIEM]({{< relref "guides/audit/sinks-and-siem.md" >}}) sends the same records to your SIEM.
- [Evidence and audit]({{< relref "concepts/evidence.md" >}}) explains how the chain is built and what its check proves.
