---
title: Overview and Audit
description: You read the deployment's health on Overview, then find any decision on Audit, check it against the hash chain and test the same call again.
pagetype: how-to
weight: 10
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server on Linux, read as the admin in headless Chromium after a person's machine had sent allowed, denied and held calls through the hook, with strazactl audit verify run from a terminal
  date: 2026-10-06
applies_to: both
who: You, as an admin
where: The console in a browser
steps: true
keywords: console overview audit chain verified live tail lens search record test this call export verify
---


Overview is the first page the console opens, and it says what needs a person now. Audit is the hash-chained record of every decision and change, read in your browser. This page walks both as an admin uses them. Overview opens to a session that holds the `config` area, and Audit to one that holds the `audit` area, so a delegated admin may see one without the other, as [Delegated administration]({{< relref "guides/operate/delegated-admin.md" >}}) explains.

## Before you start {.nostep}

- A console sign-in as an admin, at `/console/` on your server with **Sign in with a code**. [Your first governed session]({{< relref "get-started/first-governed-session.md" >}}) shows the sign-in on a standalone server.
- Some decisions on the chain, such as the deny and the allow of the first session.

## Read the summary


{{< clicks "Overview" >}}

The page opens on the **Summary** tab. The strip at the top counts the MCP servers running, the active sessions, the active users and the active policies. Under it, **Tool decisions** charts the calls the gateway and the hooks decided, with totals for allowed, denied and required approval. Switch its window between **Last 24 hours** and **Last hour** to see the last hour by the minute.

{{< shot name="overview" caption="The **Summary** tab: the counts at the top, and **Tool decisions** with its switch between **Last 24 hours** and **Last hour**." >}}

Beside the chart, **Needs attention** lists only what a person should act on: approvals that wait, drafts that wait for review, MCP servers that failed or degraded, a broken audit chain, a push lane that is down, and sinks that hold parked events. A line reads, for example, `1 draft waits for review, the oldest from alice, drafted 16 m ago.`, and each line opens the area where you act. Below it, the configuration note counts the settings that loosen the server, and **Review settings** opens them. A fresh standalone server shows `4 settings to review`, because its defaults suit a first try on one machine.

**Denied and held calls** lists the tools with the most denials or approval requests in the last 24 hours. **View reason** shows the reason recorded most often and a link to Audit filtered to that tool. The line at the foot, `Latest records verified (up to 25)`, is the chain check this page runs.

{{< see >}}The counts match what you expect of your deployment, and the foot line says `Latest records verified (up to 25)`.{{< /see >}}

## Read the activity and the system details


{{< clicks "Overview" "Activity" >}}

**Recent changes** lists the newest control plane changes in words, such as `Published a draft` or `Enrolled an approver phone or browser`, with who made each one. **Recent audit events** shows the newest records. Its **Follow** switch is off by default, and once you turn it on the list refreshes every 30 seconds. This browser remembers your choice.

{{< clicks "Overview" "System details" >}}

{{< shot name="overview-system" caption="**Security configuration** on System details, with each relaxed setting, what it costs and the key that sets it." >}}

The tiles count sessions, users, MCP servers, policies, waiting approvals and the newest record of the audit chain. **Security configuration** names each relaxed setting with what it costs, such as `Local tool default` with `A local tool no rule names runs.`, and links the row in Settings that says where to set it. **Active sessions** lists up to ten sessions with their harness, the client version and the hook configuration each one reported.

## Open the audit chain


{{< clicks "Audit" >}}

Above the table, the status line reads `Chain verified`, with the count of records loaded and the head of the chain, such as `43 records loaded, head at seq 43`. The Overview card re-hashes the 25 newest records in your browser, and the Audit screen re-hashes every record it has loaded, starting with the newest 200. The table is a live tail that polls every second, and each new record is checked against the one before it as it arrives.

{{< shot name="audit-list" caption="The newest records on Audit. Each denied `rm -rf` has a red edge and a tinted row." >}}

Each row shows **Seq**, **Time**, **Who**, **Type**, **Effect**, **What** and **Reason**. A denied call has a red edge and a tinted row. A run of identical records folds into its newest row with `×2 identical · show all`, and the export and the chain check still read every record of the run. **Load older** reads the 200 records before the oldest one loaded and checks that they join it.

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


Select a row. The sheet is titled with the record's number and type, such as `Record 24 tool`, and names who, when and the session. For a decision it opens on the outcome, `Denied.`, then the call itself, then the sentence `Decided by rule block-recursive-delete in policy standalone-starter:` followed by the reason the agent read. `Recorded at decision time.` reminds you that the record holds what the engine knew then, whatever changed since.

{{< shot name="audit-deny" caption="The record of alice's denied `rm -rf`: the outcome, the rule and policy that decided it, and the chain check." >}}

The **Chain** box holds the record's hash and the verdict of your browser, `Hash matches the loaded chain.` A record opened from a search reads `Not verified in this browser. See the audit chain status for details.` instead, because a search result is not part of the checked tail. **The record as stored** shows the whole record, and **Copy as JSON** copies it.

## Test the call again


Open the record of a decision, then press **Test this call** at the foot of the sheet. It opens **Test a call** with the person and the call of the record filled in. It answers against the policy version that is live now, and it writes nothing and records nothing. Use it after a policy change to see whether the same call would now get a different answer.

{{< shot name="audit-test-this-call" caption="**Test this call** at the foot of a record, under **The record as stored**." >}}

{{< see >}}An **Answer** such as `Denied`, with `Decided under the policy version live right now.`{{< /see >}}

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
