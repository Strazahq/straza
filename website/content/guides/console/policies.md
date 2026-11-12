---
title: Policies
seo_title: Policies in the console
description: You read which rules decide each call, build a policy with the New policy wizard, change a policy on its own page, and test a call before and after you publish.
summary: >-
  Policies holds the rules that decide every call: what is denied, what needs approval and what
  runs at once. You read the rules per policy or per role, build one with the New policy
  wizard, and change one on its own page before you publish it. Test a call shows what a call
  would get, and records nothing.
pagetype: how-to
weight: 30
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server on Linux with the starter policy and a hold policy for the role dev, read and edited as the admin in headless Chromium. The wizard was opened and not saved
  date: 2026-10-06
  except: saving from the New policy wizard, which was opened and not saved
applies_to: both
who: Admin
where: Console
steps: true
keywords: console policies policy page rules yaml decisions new policy wizard test a call save draft publish turn off recording
---


Policies holds the rules that decide every call: what is denied, what needs approval, and what runs at once. This page walks the area as an admin uses it, from reading the rules to changing one and testing the result. [Policies]({{< relref "concepts/policy-model.md" >}}) in Concepts explains how the rules combine, and the [PolicySet grammar]({{< relref "reference/policyset-grammar.md" >}}) lists every key a policy can hold.

## Before you start {.nostep}

- A console sign-in that holds the `policy` area.
- A role to write rules for. The wizard offers application roles, the kind that carries tool access, and **Everyone** for rules that hold for every session.

## Read the policy list


{{< clicks "Policies" >}}

{{< shot name="demo-policies" caption="The **Sets** view on the demo stack, with each policy's status, the roles it applies to, its counts and a `REC` badge." >}}

The list opens on the **Sets** view. Policies that hold for every session sit under **For everyone**, and policies that name a role sit under **For roles**. A third table, **Outside roles**, appears when a policy is scoped by user or identity. Each row shows the **Status**, the roles it **Applies to**, and how many of its rules deny, need approval or allow. `Live` runs now, `Off` is stored and gates nothing, and `a draft edits it` means a saved edit waits under Drafts while the published version keeps running. A `REC` badge marks a policy that records conversations. The filters narrow the list by status, role and call type.

{{< clicks "Policies" "By role" >}}

**By role** shows one row per application role, plus **Everyone**, the floor under every role. Each row counts the role's own live denials, approval gates and allows, and says whether its sessions are recorded. An empty **Denied** or **Needs approval** cell opens **New policy** with that role already picked.


{{< clicks "Policies" "Denied by default" >}}

**Denied by default** lists the tools that were denied in the last 24 hours because no rule covers them: the tools of MCP servers on people's own machines, and the harness tools Straza does not classify. Each row says how often, in which harness and when last, and **Write a rule** opens **New policy** with **Which calls** answered.

## Open a policy


{{< clicks "Policies" "standalone-starter" >}}

The head shows the status, the description and a count such as `1 rule: 1 denied, 0 need approval, 0 allowed`. Three facts sit above the rules, each with its own **Change**: **Applies to**, **Recording** and **Priority**. A denial always wins across every policy that matches, so the priority only picks which rule is reported as the reason.

{{< shot name="starter-rules" caption="The starter policy of a standalone server on its **Rules** tab: the shell patterns it denies, the reason the agent reads, and the rule id." >}}

The **Rules** tab lists each rule with its call type, the calls it matches, what happens, the reason the agent reads and the rule id. **Add rule** offers the same five kinds of call as the wizard. A rule that only an automated check decides, such as a classify rule, reads `An automated check, kept as written. Change it on the YAML tab.` The **YAML** tab holds the whole policy as stored, comments included, with **Validate** and **Copy**. The **Decisions** tab lists the calls this policy decided, newest first, from the audit chain, and a row opens the same record sheet that [Audit]({{< relref "guides/console/overview-and-audit.md#read-one-record" >}}) opens.

## Build a policy with the wizard


{{< clicks "Policies" "New policy" >}}

{{< shot name="policy-new-outcome" caption="The first step, **What should happen**, with **Needs approval** picked and **Write YAML instead** under the cards." >}}

The wizard asks one question per step: **What should happen**, **Applies to**, **Which calls**, **Who decides** and **Review**. Nothing is stored until you save a draft or publish.

1. What should happen: **Needs approval**, **Denied** or **Allowed**. Allowed is for a tool the deployment denies by default, such as a shell command under the enterprise profile or an MCP server on a person's own machine. A registered server's tools run by role access and need no allow.
2. Applies to: **Everyone** or **A role**. Only application roles are offered, because a business role governs one assignment path only. A full administrator also gets **A new role**, made in the same draft as the policy.
3. Which calls: pick **MCP tool**, **Shell command**, **File path**, **Network** or **Harness tool**, then the tools, the **Command patterns** or the **Paths**. A pattern is a shell-style glob over the whole command line, such as `kubectl apply *`.
4. Who decides, for an approval: **Who decides** is **The person behind the agent** or **An approver role**, and the wait is **A hold, up to** a time for a call the agent waits on, or **A ticket a person grants within** a time for planned work. For a denial, the step is **Reason**: write the **Reason the agent reads**. An allowed policy has no such step and stores no reason.
5. Review: the name, the facts in words and the policy text as the server stores it, then **Save draft** or **Save and publish**.

{{< details summary="Tools on people's own machines" >}}

An MCP server that a person adds to their own harness is not registered in Straza. Every profile denies its tools until a rule allows them. Under **MCP tool**, the **Server** list has two groups, **Registered in Straza** and **On people's own machines**. The second group lists the servers whose calls were denied in the last 24 hours, and **A server not seen yet** asks for the server's name. For such a server, the list holds the tools that were asked for and denied, and **Add tool** adds any other by its name. That name may be a pattern, such as `read_*`. Under **Harness tool**, the list holds the denied harness tools, and a name you add must be exact, such as `LSP`.

**A new role** makes an application role that belongs to no server. Its policy is named after the role with `-access`. A business role can compose it, and your identity manager assigns it like any other role.

**Add rule** on a policy's page offers the same two server groups and suggests the denied tools as chips under **Asked for and denied**. The rules table and the rule's sheet show the names in a rule on harness tools, and the sheet changes them. A rule for a server on people's machines carries the chip `on people's machines`.

In the **Denied by default** view, **Open in Audit** opens a row's newest record. The view shows no caller and no arguments, and a row leaves once a newer call to the tool was decided by a rule. In Audit, the record of such a denied call has **Write a rule for this call**, which opens the wizard the same way.

A rule that allows the tools of a server Straza has not registered is a risk the publisher ticks, and so is a gate that names such a server. With `admin.secondPerson` on, another administrator publishes such a draft. A rule that names a server that is not registered also gets the warning `Rule <id> of <set> names <server>, which is not a registered server, so it matches only an MCP server that a person's own harness runs under that name.`, with the fix `If you meant a registered server, fix the name.`
{{< /details >}}

**Write YAML instead** opens the whole policy as text, for a rule the wizard does not write, such as a classify rule. What you write there is saved as a draft, which you then publish under Drafts. [Hold a call for a person]({{< relref "guides/write-policy/hold-for-a-human.md" >}}) and [Tickets]({{< relref "guides/write-policy/tickets.md" >}}) build a full approval rule with the wizard, and [Your first deny]({{< relref "guides/write-policy/first-deny.md" >}}) writes a deny.

## Change a policy and publish it


A change on a policy's page stays on the page until you save it. As an example, record the conversations of every session the policy matches.

{{< clicks "Policies" "kubectl-apply-hold" "Change" "Secrets masked" "Add to unpublished changes" >}}

The **Change** to press is the one beside **Recording**. A strip at the top then holds the change, such as `1 unpublished change: recording`, with the rule counts on the page and live side by side, **Show the change**, **Discard**, **Save draft** and **Save and publish**. Leaving the page asks first, because the edits are not stored anywhere yet.

{{< shot name="policy-unpublished" caption="The strip of unpublished changes, with **Show the change**, **Discard**, **Save draft** and **Save and publish**." >}}

**Save draft** stores the new text as a draft and keeps the published version running. The page then says `kubectl-apply-hold is saved as a draft. The version that runs stays live until you publish it.`, the status reads `a draft edits it`, and the draft waits under Drafts, where [Propose and publish a change]({{< relref "guides/changes/propose-and-publish.md" >}}) takes it on.

**Save and publish** asks `Publish kubectl-apply-hold?` first. The question says what changes for which roles, what a call that matches did before and does after, and how many active sessions pick up the new version within about 30 seconds. **Test a call first** opens the test from the same question.

{{< see >}}After the publish, the page reads `Live`, and the line under the head names the draft that carried the change.{{< /see >}}

## Test a call


{{< clicks "Policies" "Test a call" >}}

Under **Who**, type a person or an agent and pick them, because their roles decide which policies apply. Under **What**, pick **MCP tool**, **Shell command**, **File path** or **Network**, and give the tool, the command line or the path. Press **Test**.

{{< shot name="test-filled" caption="Test a call with alice under Who, Shell command picked and `rm -rf /tmp/x` as the command line." >}}

The **Answer** says what the call would get and which rule decides it. It answers for now and writes nothing and records nothing. On a policy's page with unpublished changes, it answers twice, under **Live now** and under **With your unpublished changes**, so you see the effect before you publish.

{{< shot name="test-answer" caption="The answer: Denied, decided by rule `block-recursive-delete` in policy `standalone-starter`." >}}

The test takes a person, never a bare role. [Simulate a call]({{< relref "guides/write-policy/simulate-and-coverage.md" >}}) shows the CLI form, which also tests roles nobody holds.

## Turn a policy off or delete it


{{< clicks "Policies" "kubectl-apply-hold" "More" "Turn off" "Turn off" >}}

{{< shot name="policy-turn-off" caption="The question that turns kubectl-apply-hold off, which says what stops governing." >}}

The question says what stops governing the moment you confirm, and that MCP calls the policy gated then run at once where a role has access. The policy stays stored and reads `Off`, and **Turn on** in the same menu brings it back. Sessions that hold the roles pick up the change within about 30 seconds, and a call held for approval right now keeps its hold. Turning a policy off or on acts at once, without a draft.

**Delete** in the **More** menu works only on a policy that is off. On a live one it says `Only a policy that is off can be deleted. Turn a live policy off first.` **Export YAML** in the same menu downloads the policy as a file.

## Next {.nostep}

- [Propose and publish a change]({{< relref "guides/changes/propose-and-publish.md" >}}) reviews and publishes a saved draft.
- [Capture]({{< relref "guides/write-policy/capture.md" >}}) turns recording on for a role and reads the transcripts.
- [Roles]({{< relref "guides/console/roles.md" >}}) shows the roles that the rules match.
