---
title: Connect identity
description: Bring people, AI agents and their role membership in from your identity manager, which stays the master of who exists, while the roles themselves are created in Straza.
pagetype: index
weight: 20
draft: false
keywords: identity scim idm iga provisioning
---

- [midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) connects midPoint over SCIM, so users, agents and role membership arrive from your IGA and leave when it says so.
- [Okta]({{< relref "guides/connect-identity/okta.md" >}}) provisions people and their role membership from Okta over SCIM.
- [Keycloak login]({{< relref "guides/connect-identity/keycloak-login.md" >}}) makes Keycloak the login provider, so people sign in with their own identity and Straza never holds a password.
- [Generic SCIM]({{< relref "guides/connect-identity/generic-scim.md" >}}) connects any SCIM 2.0 client with the profile the server accepts.
- [AI agents as identities]({{< relref "guides/connect-identity/nhi-provisioning.md" >}}) provisions an AI agent from your identity manager, registers the key it signs in with, and retires it again.
