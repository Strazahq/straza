---
title: Decide whether Straza fits
description: The pages an IAM or security architect reads, in order, to decide whether to run a pilot.
pagetype: journey
weight: 1
draft: false
keywords: evaluate decide pilot architect journey fit
---


[How it works]({{< relref "evaluate/how-it-works.md" >}}) describes the model that the later pages build on.

{{< stops >}}
{{< stop page="evaluate/what-straza-is.md" >}}What Straza does, who it is for, and what it does not try to do.{{< /stop >}}
{{< stop page="evaluate/how-it-works.md" >}}A diagram of every core noun, and a tool call traced on the agent's machine and at the gateway.{{< /stop >}}
{{< stop page="evaluate/security-model.md" kind="Security" >}}What Straza enforces and where. An action that bypasses the hook, the gateway and `straza exec` is outside Straza's view.{{< /stop >}}

{{< stop page="security/known-limits.md" >}}A table of the known limits that matter for a fleet. If you manage Claude Code through a device management profile, server-managed settings or a Windows registry policy, read the row on its managed sources first. Claude Code uses only its highest managed source, so if any of those lacks Straza's hooks, Claude Code skips the hooks and gives no warning.{{< /stop >}}
{{< stop page="evaluate/identity-and-your-iga.md" >}}Your identity provider checks sign-in, and your IGA, or an admin on standalone, owns people, agents and who holds which role. Straza owns what each role may do.{{< /stop >}}
{{< stop page="concepts/objects.md" >}}How a role covers some of a server's objects, and what a certifier reads in your IGA.{{< /stop >}}
{{< stop page="evaluate/try-it-on-the-demo-stack.md" >}}Run midPoint, Keycloak and working agents on one machine, and check your pilot's questions against them.{{< /stop >}}
{{< stop page="reference/standalone-and-enterprise.md" label="Plan your deployment" >}}The standalone and enterprise profiles compared, with the setting behind each difference. Read it once you decide that Straza fits.{{< /stop >}}
{{< /stops >}}

{{< next "guides/set-up-straza-for-a-team.md" >}}
