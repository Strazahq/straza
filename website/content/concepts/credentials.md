---
title: Credentials
description: Whose credential a call to an MCP server carries, and why the agent never holds it.
pagetype: explanation
weight: 90
draft: false
summary: >-
  A server that needs a credential checks it on every call, and the agent never holds it. The gateway
  adds the credential the server declares, either the server's own secret, which Straza stores, or
  the caller's own token or sign-in. Straza uses a separate listing credential only on its own call
  that lists a server's objects.
noun: [server, credential]
keywords: credential secret token sign-in oauth listing gateway injection upstream sponsor
---


A credential is the secret that an [MCP server]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) checks before it answers a call. Straza stores every such credential, and the gateway adds it to a call only after the call is decided.

## Why the agent never holds it


Straza does not hand an [agent]({{< relref "concepts/identities.md" >}}) its own credentials and trust the agent's machine to use them well. The model can read, print and send any credential on the agent's machine, and no hook on an open machine can stop a process that already holds one. Because the secret is kept on the gateway, calls on the gateway path stay governed even when the controls on the agent's machine fail.


This protection covers only calls that pass through the gateway. An agent that has its own route or its own credential to a server can skip the gateway, so remove those routes when you deploy.

## Whose credential a call carries


The server's manifest declares which credential it needs, and the gateway resolves it for each call from the session that makes it.

| The server needs | The call carries | Who puts it in Straza |
|---|---|---|
| Nothing | No credential | Nobody |
| One shared secret | The server's own secret, or the override of the [role]({{< relref "concepts/roles-and-access.md" >}}) that allowed the call | An admin of the server |
| Each person's own token | The caller's own token | Each person, once, on their self-service page or from a terminal |
| Each person's own sign-in | The caller's own sign-in at a provider an admin registered | Each person, once, in a browser |


On the demo stack, the server postgres takes one shared secret. The gateway sends it to adaptive-pg-mcp as a bearer header on every call. All its callers reach the database as the same upstream identity, and only Straza's records tell them apart. [Credentials]({{< relref "guides/serve-mcp-apps/credentials.md" >}}) stores a shared secret, and [Each caller's own credential]({{< relref "guides/serve-mcp-apps/caller-credentials.md" >}}) sets up tokens and sign-ins.


A shared secret goes to a remote server as a header, and to a server that runs as a process in its environment. A token or a sign-in of each caller works only with a remote server, because a process is one identity and cannot carry a credential per caller.


Straza denies a person who has no token or sign-in of their own, and a person never falls back to another credential. An agent with none of its own runs on what the manifest names for agents: nothing, its [sponsor's]({{< relref "concepts/identities.md" >}}) own credential once the sponsor has allowed it, or the server's shared secret. On a server that takes each caller's sign-in, an agent can also get a token of its own client at the provider. The [audit record]({{< relref "concepts/evidence.md" >}}) of each call names which credential it used and whose it was, so a review can tell an agent on its sponsor's account from the sponsor's own calls.

## Where it is kept and who can see it


Straza seals each stored credential with its key-encryption key and keeps only the ciphertext. It holds the sealed rows in memory, so a call never reads the database, and opens a row only when a call or a health check needs it. No API response ever carries the value. People recognize a stored secret by its fingerprint, the first four hex characters of its SHA-256.


The agent's own Straza session token never reaches the server, because the gateway never forwards a client's authorization header. When a call needs a credential and none can be found, the gateway denies it with a sentence that says who stores one, and it never calls the server without it. The server does receive the credential and can answer with content of its own, so you must still trust the server.

## The listing credential


A server that declares its [objects]({{< relref "concepts/objects.md" >}}) needs another stored secret, the listing credential. Straza calls the server's list tool itself, with no person's session behind the call, and that call carries only the listing credential. No tool call and no health check carries it, and the listing never falls back to another secret. On the demo stack, every tool call to postgres carries the server's own secret, and only Straza's listing of `list_databases` carries its listing credential.


No person stands behind the listing credential, so a server should accept it only on its list tool. A server that also accepted it on its write tools would let that credential bypass every role.

## Straza's own credentials


The keys and session tokens that prove who is calling Straza itself are separate from the credentials of the servers behind the gateway. [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) follows them from enrollment to revocation.

{{< details summary="What an expert asks about credentials" >}}

For a server with a shared secret, the gateway uses the first credential it finds in this order: an override on the role that allowed the call, the override of the held role whose name sorts first, and the server's own secret. On a server that declares objects, the role that allowed the call is the first role by name that covers it. The health check uses the server's own secret, or the first role override by name when there is none.


Straza does not reach a server that takes a shared secret until the secret is stored, so a newly added server of that kind lists no tools until then. A server that runs as a process does not start until its secret arrives.


When a person's own token or sign-in has expired or cannot be opened, the gateway denies the call and never falls through to another credential. Each person's token is sealed under the same key as every other credential. There is no key per person, because the gateway must open the token for an agent's calls while its person is away.


Nobody can finish a sign-in for an agent, because a sign-in finishes in a browser that is signed in to Straza as a person. An agent on a sign-in server therefore runs on its sponsor's sign-in, a shared secret, or its own client.


When [your IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}) deactivates a person, Straza deletes that person's own tokens and sign-ins. A person made active again connects those servers again.


Straza stores a manifest as it was written and masks secret-shaped values only when it shows one. A secret typed into the manifest itself therefore sits in clear in the database and its backups. Put it in a stored credential instead.
{{< /details >}}

{{< next "concepts/evidence.md" >}}
