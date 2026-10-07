---
title: Support
description: Tell you where to ask for help and what to include when you do.
pagetype: reference
weight: 40
draft: false
keywords: support help issues contact
---


Support for Straza runs through three channels, and the right one depends on what you have. Bugs and feature requests go to GitHub issues on strazahq/straza. Questions about running Straza, an engagement or a commercial license go to hello@straza.ai. A vulnerability goes to security@straza.ai or through GitHub private vulnerability reporting, never to a public issue, and [Reporting a vulnerability]({{< relref "security/reporting.md" >}}) carries the response times.

## What to include


A server problem needs the output of `strazad version`, the profile you run and the exact error text or log line, with any password or token masked. For a client problem, send the output of `straza doctor` on the affected machine, which reports the enrollment, the identity, the server and the approver surface and follows each warning with the command that fixes it, plus the exact error text. When the console misbehaves, name the browser and the page. The repository file [SUPPORT.md](https://github.com/strazahq/straza/blob/main/SUPPORT.md) carries the same facts, and the [Reference]({{< relref "reference/_index.md" >}}) section lists every command and configuration key.
