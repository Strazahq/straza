---
title: Run Straza
description: Deploy, expose, back up, upgrade and debug strazad in every supported shape, and stop an identity or every agent when you must.
pagetype: index
weight: 10
draft: false
keywords: operate deploy docker kubernetes helm run
---

[Requirements and sizing]({{< relref "reference/requirements-and-sizing.md" >}}) gives the processors, memory and disk for each shape below, and [Install]({{< relref "get-started/install.md#choose-how-to-run-the-server" >}}) compares the shapes in one table.

- [Standalone host]({{< relref "guides/operate/standalone-host.md" >}}) runs strazad as one binary on one host with the standalone profile.
- [Docker]({{< relref "guides/operate/docker.md" >}}) runs a standalone server in a container with persistent storage.
- [The enterprise shape]({{< relref "guides/operate/enterprise-shape.md" >}}) runs strazad as stateless replicas behind a load balancer with a shared database, event bus and key file.
- [Kubernetes with Helm]({{< relref "guides/operate/kubernetes-with-helm.md" >}}) installs strazad on Kubernetes with the Helm chart and the values that matter.
- [TLS and exposure]({{< relref "guides/operate/tls-and-exposure.md" >}}) configures HTTPS and chooses which server routes each network can reach, before clients connect from another machine.
- [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md" >}}) backs up the state that matters, upgrades strazad without logging anyone out, and rolls back.
- [Kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}) stops one identity or every session now, and confirms the stop landed.
- [Doctor and logs]({{< relref "guides/operate/doctor-and-logs.md" >}}) diagnoses a hook or session problem from the agent machine, and a boot or endpoint problem from the server log.
- [Delegated admin]({{< relref "guides/operate/delegated-admin.md" >}}) gives someone admin rights over one area of Straza without handing out the root role.
- [Delegate one MCP server]({{< relref "guides/operate/delegate-one-server.md" >}}) lets the people who own one MCP server administer it, and nothing else.

- [Metrics and alerts]({{< relref "guides/operate/metrics-and-alerts.md" >}}) lists what /metrics counts and what to alert on.
