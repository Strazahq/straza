---
title: Credentials
description: Store one shared secret for an MCP server, so the gateway adds it to every call and no agent ever sees it.
pagetype: how-to
weight: 20
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server in an Alpine container on Linux, beside the demo stack's MCP reference server, with strazactl on an admin API token minted in the console and the gateway call made through straza mcp from alice's enrolled machine in a second container. Every CLI step ran. The console steps were read from the console's source and not clicked
  date: 2026-10-06
applies_to: both
keywords: credential secret injection upstream fingerprint
who: You, as the admin of the server
where: The console or a terminal with strazactl, and the agent's machine for the last check
steps: true
modes: [console, cli]
mode_default: console
---


The gateway adds the credential a server declares to each upstream call. A static credential is the server's shared secret, or the override of the caller's role. When no credential can be found for a call that needs one, the call fails before it reaches the upstream. Every view identifies a stored secret by its scope and fingerprint, never by its value.


The server's manifest declares whose credential the upstream sees. This page covers the shared secret, `static`:

| The upstream needs | Credential kind | Who completes the next step |
|---|---|---|
| No credential | `none` | You register the server. |
| One shared secret for its calls | `static` | You declare the injection and store the server's secret or a role's override. |
| A token belonging to each person | `token` | You declare the server, and each person pastes their token on their self-service page or runs `straza connect <server>`. |
| A browser sign-in belonging to each person | `oauth` | You configure the provider and declare the server, and each person signs in from their self-service page. |

`none` is the default. [Each caller's own credential]({{< relref "guides/serve-mcp-apps/caller-credentials.md" >}}) covers `token` and `oauth`, and what agents run on there, because an agent cannot finish a browser sign-in.

## Before you start {.nostep}


- An installed server that declares `static`, such as the `scout-tools` server from [Add a server]({{< relref "guides/serve-mcp-apps/add-a-server.md" >}}).
- A login of the server's own admin, of `straza-global-mcp-admin`, or of an admin whose areas include MCP servers.

## Declare the credential


{{< console >}}
In the Add MCP server wizard, pick **One shared secret for this server** on the Credential step and type the secret there. For a server that already runs, change the credential on its page:

{{< clicks "MCP servers" "scout-tools" "Overview" "Change" >}}

Press the **Change** of the Server authentication card. The sheet saves through a draft, like every change to a server.
{{< /console >}}


{{< cli >}}
The `credential` block says what kind of secret the server needs and how the gateway renders it. This is the block of the `scout-tools` manifest from Add a server:

```yaml
straza:
  runtime:
    kind: remote
    remote:
      url: http://demo-tools:3001/mcp
  credential:
    kind: static
    inject:
      as: header
      name: X-Demo-Key
      template: "{{secret}}"
```

Installing the manifest says what is missing and what to run:

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl apps install -f scout-tools.yaml
```
{{< /command >}}

{{< see >}}`installed scout-tools (remote runtime). Health: degraded, requires a credential and none is stored. Set one with strazactl apps secret set scout-tools, which asks for the value at a hidden prompt.`{{< /see >}}
{{< /cli >}}


`inject.as: header` is valid only for the `remote` runtime, and `inject.as: env` only for `command` and `oci`, because a stdio process has no headers and a remote server gets no process environment. `template` must contain `{{secret}}`, and the rendered value exists only on the gateway side. The parser refuses a `credential.inject` block with `kind: none` with `credential.inject is forbidden with kind none`. It refuses a static kind without one with a message that names the field, says why, and says what to set. For example, a header inject on a command runtime is refused with "credential.inject.as header needs a remote runtime, because a command or oci runtime starts a process that reads an environment variable and gets no request headers. Use as: env with the variable name the process reads, for example API_TOKEN". The gateway never dials a server that takes a shared secret until that secret is stored, so a freshly installed one lists no tools until then.


A `command` or `oci` server starts with `PATH`, `HOME` and `LANG` from strazad, the manifest's `env` entries and the injected credential, and nothing else from strazad's environment. Secrets strazad reads from its own environment, the store DSN and the metrics token among them, never reach the child. When the server needs a proxy or a certificate variable, put it in the manifest's `env`. The `oci` runtime also passes every `DOCKER_` variable to the docker command line, so a rootless or remote daemon keeps working, and the container never sees them. Because such a server holds its static secret in its environment, give no role a tool that prints the environment, such as `get-env`.

## Set the secret


{{< console >}}
{{< clicks "MCP servers" "scout-tools" "Overview" "Set one" >}}

**Set one** sits on the row Stored here of the Server authentication card. Type the secret and press **Store it**. The value is stored at once and never rides a draft. Once a secret is stored, the same row offers **Set it again**, which rotates it.

{{< shot name="server-secret-set" caption="The box that opens under **Stored here**, where you type the secret and press **Store it**." >}}

A role's override goes on the row Per-role secrets: press **Add one for a role**, pick the role under `Pick an application role`, type its secret and press **Store it**.
{{< /console >}}


{{< cli >}}
Without `--role`, the command sets the server's shared secret. Run in a terminal, it asks for the value at a hidden prompt, so the secret never lands in a command line that shell history keeps.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl apps secret set scout-tools
```
{{< /command >}}

```text
Secret for the MCP server scout-tools (input hidden):
secret set for the MCP server scout-tools: the server's own secret, fingerprint 19c6
```

A script fills `STRAZA_SECRET_VALUE` from its secret manager instead, and the command then reads it without a prompt. `--value` also works, but it puts the secret in the process arguments and the shell history.

The server turns `running` within seconds:

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl apps list
```
{{< /command >}}

```table
NAME         VERSION  RUNTIME  STATUS   REASON  SOURCE  TOOLS  REACHED BY  CHECKED
scout-tools  0        remote   running  -       api     3      -           4s ago
```

The listing is trimmed to the one server. Setting the secret again with the same command rotates it. A role's override is the same command with `--role`, and the role must exist first, as [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) creates it:

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl apps secret set scout-tools --role scout-tools-readers
```
{{< /command >}}

```text
Secret for the MCP server scout-tools, role scout-tools-readers (input hidden):
secret set for the MCP server scout-tools, role scout-tools-readers, fingerprint f0b5
```
{{< /cli >}}


The server seals the value with its key-encryption key, stored under the data directory unless `secrets.kekFile` names another path, and keeps only the ciphertext. It answers with a fingerprint, the first four hex characters of the value's SHA-256, so you can tell which value is stored without ever reading it. It probes the server again under the new credential, and a rotation closes the pooled upstream sessions opened under the old value. A role's override is used for that role's calls in place of the server's own secret.


{{< fails >}}
`the MCP server demo-tools declares no credential (credential.kind none), so a secret would never be used. Change the manifest's credential block first.`
: Declare a credential in the manifest, then store the secret.
{{< /fails >}}

An override for a business role is refused with a sentence that ends in `Set the secret for an application role instead, or for the server itself.`, because a business role reaches tools only through the application roles it composes. An override for an approver role or a Straza role is refused with a sentence that ends in `cannot hold a secret`.

## Read what is stored


{{< console >}}
{{< clicks "MCP servers" "scout-tools" "Overview" >}}

{{< shot name="server-overview" caption="The **Server authentication** card, with the type, how the secret is sent and its fingerprint under **Stored here**." >}}

The Server authentication card shows the type and how the secret is sent. Its row Stored here gives the time the secret was set, its fingerprint and `Never shown again.`, and its row Per-role secrets gives one line per role with the fingerprint. No row shows a value.
{{< /console >}}


{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl apps show scout-tools
```
{{< /command >}}

```text
SECRETS
SCOPE  ROLE                 KIND    FINGERPRINT  SET
app    -                    static  19c6         2026-10-06T21:08:38Z
role   scout-tools-readers  static  f0b5         2026-10-06T21:09:02Z
```

The output is trimmed to the secrets table.
{{< /cli >}}

No endpoint reads a secret back. The value goes in once and is only ever decrypted in memory for an upstream call or a health probe.

## Prove the agent never sees it


{{< only form="cli" >}}This check runs on the agent's enrolled machine, where the console has no part.{{< /only >}}

Drive a governed call through the gateway from a machine enrolled as a holder of the role. The proxy carries the session token, and the gateway resolves the secret in memory, renders the header, calls the server and returns the result:

{{< command terminal="Agent's machine" purpose="client" >}}
```sh
{ printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"docs-example","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"scout-tools__echo","arguments":{"message":"hello through the gateway"}}}'; sleep 5; } \
  | straza mcp --harness claude-code
```
{{< /command >}}

```text
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"Echo: hello through the gateway"}]}}
```

A `sleep` holds the pipe open while the replies come back, because the proxy stops when its input ends. The initialize reply is trimmed from the output. Nothing beyond the echo reached the agent, and the audit record of the call names the credential row that was used, by id, never by value. On your side, the server record the API returns carries health, tool names and the roles that reach it, and no field that holds a stored secret.

## When a session has no secret {.nostep}


Resolution runs role first, then server. When one of the session's roles holds an override for the server, that row is used, and when two roles hold one, the role that carries the access row wins. Otherwise the server's own secret is used. When neither exists the call fails closed, the upstream is never contacted, and the agent reads "upstream call failed: app scout-tools requires a credential and none is stored for the server or bound to your roles. An administrator sets one with strazactl apps secret set scout-tools, which asks for the value at a hidden prompt". The health probe resolves the same way, so a server with only per-role rows is probed under the row of the role whose name sorts first.

## Remove a secret {.nostep}


A secret is removed on its own, without touching the server. Removing the server's own secret leaves every role's override stored. Each removal is an audit record with the actor. With no secret left, the server reads `degraded` again, with the same reason as before the first set.

{{< console >}}
On the Server authentication card, press **Remove** on the row of the secret, then **Remove secret**. The dialog says what changes first, such as `Calls by scout-tools-readers fall back to the shared secret, or run without one when none is stored.`
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl apps secret remove scout-tools --role scout-tools-readers
strazactl apps secret remove scout-tools
```
{{< /command >}}

```text
removed the secret for the MCP server scout-tools, role scout-tools-readers
removed the server's own secret for the MCP server scout-tools
```

Removing a row that is not there answers "no secret is stored for the MCP server scout-tools (role scout-tools-readers)".
{{< /cli >}}

## Keep secrets out of the manifest {.nostep}


Straza stores each server's manifest in plain text, exactly as it was written. The database, every backup of it and a file in the apps directory therefore hold every environment value, argument and address of a manifest in clear, and the host's process list shows a command server's arguments. Straza shows these values masked, as `[REDACTED]`, `?…` and `#…`, on the admin API, in `strazactl apps export`, on the console, in a health reason and in the server log. That covers every environment value, every value and default in the server block, each argument or other value the secret scan reads as a secret, and the user part, the query, the fragment and a generated path part of every address. A value the scan does not flag still prints in the server log when the server itself writes it there. Keep a secret in a stored credential with `credential.inject`, which Straza keeps encrypted. Straza injects one secret per server, as a header or an environment entry.


The gateway never forwards the agent's own `Authorization` header upstream. That header carries the Straza session token, which has no business at the server, and the injected header wins even when a client sends one. A server that expects the client's own bearer therefore needs the `oauth` or the `token` kind, where the credential is the person's, as [Each caller's own credential]({{< relref "guides/serve-mcp-apps/caller-credentials.md" >}}) shows.

## Next {.nostep}

[Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) gives a role the server's tools.
