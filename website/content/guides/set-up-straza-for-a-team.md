---
title: Set up Straza for a team
description: Every step from an empty host to a governed agent on the enterprise profile, in the order the steps depend on each other.
pagetype: journey
weight: 1
draft: false
keywords: set up team enterprise order journey first admin bootstrap
---


Read [How it works]({{< relref "evaluate/how-it-works.md" >}}) first if the nouns are new. Each step links the page that does it.

{{< stops >}}
{{< stop group="Before anything runs" />}}
{{< stop page="reference/standalone-and-enterprise.md" label="Plan your deployment" >}}Pick the enterprise profile. It needs Postgres, a public address, one key file for secrets on every replica, a shared NATS server once you run more than one replica, and your identity provider's issuer and client id.{{< /stop >}}
{{< stop page="guides/connect-identity/_index.md" label="Connect sign-in" >}}Register Straza at [Entra ID]({{< relref "guides/connect-identity/entra-login.md" >}}), [Okta]({{< relref "guides/connect-identity/okta-login.md" >}}) or [Keycloak]({{< relref "guides/connect-identity/keycloak-login.md" >}}), which gives you the issuer and client id the install needs.{{< /stop >}}
{{< stop page="guides/operate/enterprise-shape.md" label="Install the server" >}}Run strazad on one host, on [Docker]({{< relref "guides/operate/docker.md" >}}), as replicas or with [Helm]({{< relref "guides/operate/kubernetes-with-helm.md" >}}).{{< /stop >}}
{{< stop group="The first admin" />}}
{{< stop gap="true" label="Sign in as the first admin" >}}Nobody can sign in before your IGA provisions them, and your IGA needs a token that only an admin can mint. Break that circle once: sign in with the break-glass admin that the first start printed, under Emergency sign-in, or name your own account in `oidc.bootstrapAdmin` before strazad starts, so your first sign-in at your identity provider makes you an admin. [Identity and your IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}) explains the order.{{< /stop >}}
{{< stop page="guides/connect-identity/generic-scim.md" anchor="mint-a-token" label="Mint the admin API tokens" >}}One for your IGA with SCIM read and write, and one for strazactl.{{< /stop >}}
{{< stop group="What agents may reach" />}}
{{< stop page="guides/serve-mcp-apps/add-a-server.md" label="Add the first MCP server" >}}A role that reaches a server's tools is made on that server, so the server comes first.{{< /stop >}}
{{< stop page="guides/serve-mcp-apps/catalogs-per-role.md" label="Make the roles" >}}Roles are made in Straza and never by your IGA, so make each role before your IGA can assign it.{{< /stop >}}

{{< stop page="guides/write-policy/first-deny.md" label="Write the first policy" >}}On enterprise, local tools are denied until a policy allows them. Allow what agents need, and [hold the risky commands for a person]({{< relref "guides/write-policy/hold-for-a-human.md" >}}).{{< /stop >}}
{{< stop group="Who your people and agents are" />}}

{{< stop page="guides/connect-identity/midpoint.md" label="Connect your IGA" >}}midPoint, [Okta]({{< relref "guides/connect-identity/okta.md" >}}) or [any SCIM system]({{< relref "guides/connect-identity/generic-scim.md" >}}) then sends people, agents, sponsors and role membership. Until it has sent a person, sign-in refuses them, because enterprise makes no account at a first sign-in by default.{{< /stop >}}
{{< stop group="Machines" />}}
{{< stop page="guides/govern-an-agent/enroll-a-machine.md" label="Enroll the first machine" >}}An admin installs Straza on the machine first, managed and owned by root. The person then runs `straza enroll` and signs in.{{< /stop >}}
{{< stop page="guides/operate/doctor-and-logs.md" label="Check it end to end" >}}Run the doctor, make one allowed and one denied call, and practice the [kill switch]({{< relref "guides/operate/kill-switch-runbook.md" >}}).{{< /stop >}}
{{< /stops >}}

A step marked No page yet links the page that explains it.

{{< next "guides/operate/_index.md" >}}
