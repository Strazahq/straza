---
title: Reference
description: The exact surfaces, generated from the code where they can be.
pagetype: index
weight: 40
draft: false
keywords: reference cli configuration api events spec
---

- [Command line]({{< relref "reference/cli/_index.md" >}}) lists every command of strazad, strazactl and straza with its flags and operands.
- [Configuration]({{< relref "reference/configuration.md" >}}) lists every strazad configuration key, its environment variable when it has one, and the reason when it has none.
- [Client environment]({{< relref "reference/client-environment.md" >}}) lists every environment variable the straza client binary reads.
- [API]({{< relref "reference/api.md" >}}) gives every operation of the strazad HTTP API by method and path, from the OpenAPI document.
- [Events and the audit record]({{< relref "reference/events.md" >}}) lists every audit event Straza emits and the fields of the audit record.
- [Ports and network]({{< relref "reference/ports-and-network.md" >}}) lists the listeners, ports and connection directions of a deployment.
- [The spec]({{< relref "reference/spec.md" >}}) points to the wire-format specifications Straza publishes and says what each one fixes.
- [Glossary]({{< relref "reference/glossary.md" >}}) defines each term the docs use, one name per concept.

- [Requirements and sizing]({{< relref "reference/requirements-and-sizing.md" >}}) gives the processors, memory and disk for each shape of Straza, what each number rests on, and the platforms Straza was tested with.
- [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md" >}}) gives one row per difference between the two profiles, with the setting that controls it.
- [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md" >}}) lists how long each token, credential, hold and grace period lasts.
- [PolicySet grammar]({{< relref "reference/policyset-grammar.md" >}}) gives every key of a PolicySet with a one-line example.
