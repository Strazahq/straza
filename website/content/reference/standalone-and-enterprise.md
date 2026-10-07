---
title: Standalone and enterprise compared
description: Every difference between the two profiles in one table, with the setting that changes each one.
pagetype: reference
weight: 5
draft: false
keywords: profile standalone enterprise compare differences defaults settings
---


Straza runs in one of two profiles. Standalone is the default and suits one machine or a small team: it signs people in itself, keeps its data in SQLite and needs nothing else installed. Enterprise expects your identity provider and a Postgres database, and a shared NATS server once you run more than one replica, as [the enterprise shape]({{< relref "guides/operate/enterprise-shape.md" >}}) shows.

You choose the profile with `profile` in the configuration file, the `STRAZA_PROFILE` environment variable, or `strazad serve --profile`. The profile only picks defaults. A row that names a setting changes when you set that key, and the [configuration reference]({{< relref "reference/configuration.md" >}}) gives each key's environment variable. A row with no setting is fixed by the profile.


{{< compare >}}

Every row has its own address, such as `#offline` for the client that cannot reach strazad, so a guide can link to the one difference it depends on.

## The same in both profiles


Some defaults are often taken for differences and are not. Both profiles run the event bus inside strazad, with no network socket, until you set `events.embedded: false` and `events.url`. A session lasts at most 12 hours and a device credential 30 days in both, as [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md" >}}) lists. The audit sentinel is off, a person may publish their own change, and every approval setting starts from the same value.

The same decision rules hold in both profiles. An MCP tool call that no rule matches runs when one of the caller's roles has access to the tool, and is denied when none has. An event kind Straza does not know is denied before any rule is read. The break-glass admin exists on every database, whatever the profile.
