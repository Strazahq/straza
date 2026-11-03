---
title: Your first approval on one laptop
description: Hold your agent's command on your laptop until you approve it.
pagetype: journey
weight: 60
draft: false
keywords: laptop standalone first approval confirm self-service browser journey newcomer
---


You use the standalone profile, so Straza runs on your laptop and signs you in itself, with no external identity provider. [How it works]({{< relref "evaluate/how-it-works.md" >}}) explains the model, and [Try it on one laptop]({{< relref "evaluate/try-it-on-one-laptop.md" >}}) walks through all of these steps.

{{< stops >}}
{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="before-you-start" label="Get the binaries" >}}Put strazad, strazactl and straza on your PATH, on a machine with no Straza package or managed install.{{< /stop >}}

{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="start-the-server" label="Start the server" >}}Run strazad on the standalone profile. Its first start prints the admin password and adds a starter rule that denies a recursive forced delete.{{< /stop >}}
{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="sign-in-to-the-console" label="Sign in to the console" >}}Sign in as the admin from the first start, and mint a token for strazactl.{{< /stop >}}
{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="add-alice" label="Add alice, the person your agent works for" >}}Add the person your machine enrolls as, with the role that lets their browser approve.{{< /stop >}}
{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="enroll-this-machine-as-alice" label="Enroll your machine" >}}Start the Straza service and run `straza enroll`, which signs you in.{{< /stop >}}
{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="connect-claude-code" label="Connect Claude Code" >}}`straza install claude-code` installs the hook for your user, and the starter rule then denies a command.{{< /stop >}}
{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="hold-git-push-for-yourself" label="Hold a command for yourself" >}}A rule with the mode confirm holds a command until you approve it.{{< /stop >}}
{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="turn-on-approvals-in-this-browser" label="Turn on approvals in this browser" >}}On the self-service page, under This browser.{{< /stop >}}
{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="approve-your-agents-call" label="Approve your agent's call" >}}Straza denies the agent's call at once with a reference. Approve it on the self-service page under Requests. An identical retry in the same session within 60 seconds of your approval runs once.{{< /stop >}}
{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="remove-straza-from-this-machine" label="Remove Straza from this machine" >}}Remove the hooks while the service still runs, then stop the service and the server and delete their state.{{< /stop >}}
{{< stop page="evaluate/try-it-on-one-laptop.md" anchor="what-this-protects-on-one-laptop" label="What this protects on one laptop" >}}It catches a mistake by the agent or a prompt injection against it. It does not stop you, because you approve the calls yourself. A hook installed for your user is also advisory.{{< /stop >}}
{{< /stops >}}

{{< next "guides/set-up-straza-for-a-team.md" >}}
