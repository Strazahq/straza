---
title: Ports and network
description: The listeners strazad opens, what each address on them serves, and every connection a deployment makes in each direction.
pagetype: reference
weight: 50
draft: false
keywords: ports network listeners firewall outbound telemetry usage data push relay
---


Use this page to write the firewall rules and ingress routes for a Straza deployment. strazad opens at most two listeners, and every other connection in a deployment starts at strazad and goes out. An agent's machine opens no port at all.

## Listeners


There is no separate metrics port and no debug port. The embedded event bus has no socket at all.

| | Main listener | Approver listener |
|---|---|---|
| Setting | `server.listen` | `server.approverTLS.listen` |
| Default address | `127.0.0.1:8420` in standalone, this machine only. `:8420` in enterprise, every interface. | `:8443` on every interface in standalone, created at the first start. Off in enterprise until you configure it. |
| What it serves | Every address in the next table, over HTTP | The `/v1/approver/` routes and `/readyz`. Every other address answers 404. |
| Who connects | straza clients, strazactl, browsers, your identity manager, MCP clients, Prometheus, and Slack's button callback | The phone approval app and load balancer probes |
| TLS | Plain HTTP until you set `server.tls.certFile` and `server.tls.keyFile`. Under enterprise, strazad logs a warning at every start while the listener is plain. | Always, TLS 1.2 or newer. The certificate is self-signed by default, and the phone pins it at enrollment. |


The approver listener allows 10 requests a second from each connecting address, set by `server.approverTLS.perIPRPS`. It takes the address from the connection and never from `X-Forwarded-For`. [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md#listener" >}}) shows the listener rows of both profiles side by side.

## What the main listener serves


| Address | What it is | Who uses it |
|---|---|---|
| `/v1/` | The JSON API of the [API reference]({{< relref "reference/api.md" >}}), the revocation stream at `/v1/push` and Slack's button callback at `/v1/approval/callbacks/slack` | strazactl, the straza client, the console, the self-service page, connectors and Slack |
| `/healthz`, `/readyz`, `/version` | Probes that answer without a token | Load balancers and monitoring |
| `/.well-known/straza/` | Discovery documents: the session token keys, the snapshot keys, where a client signs in, and the client assertion keys | straza clients, gateways and your identity provider |
| `/console/` | The console | Admins in a browser |
| `/self-service/` | The self-service page. The root address `/` redirects there, and `/approvals/`, its former address, redirects there permanently. | People in a browser |
| `/mcp` and `/mcp/` followed by a server's name | The MCP gateway. `/mcp` serves every server you may reach, and a server's own address serves its tools under their own names. | MCP clients and `straza mcp` |
| `/scim/v2/` | The SCIM 2.0 endpoint | Your identity manager, with an admin API token that carries `scim:read` and `scim:write` |
| `/metrics` | The Prometheus metrics | Prometheus |
| `/oidc/` and `/.well-known/openid-configuration` | Straza's own sign-in | straza, strazactl and AI agents with a key of their own |


`/metrics` answers anyone who reaches the listener until you set `server.metricsToken`. `/metrics` labels its counts by MCP server, sink, route, status, effect and lane. No metric names a session or a user. [Metrics and alerts]({{< relref "guides/operate/metrics-and-alerts.md" >}}) sets the token and lists every metric.


Straza's own sign-in does a different job in each profile. Under standalone it signs in every person with the device flow, and every AI agent that has a key of its own. Under enterprise it signs in only those AI agents, through the client credentials grant, and the break-glass admin, through a device flow closed to every other account.

## The approver listener


The approver listener lets a phone reach a server whose main listener is plain HTTP inside a trusted network. A phone connects only over TLS, and only to the key it pinned at enrollment.

In standalone, strazad creates the key pair at the first start and guesses a public URL from the host's first LAN address. In enterprise, set `listen`, `certFile`, `keyFile` and `publicUrl` under `server.approverTLS` together, or set `server.approverTLS.autoMint: true`.

When an ingress with a publicly trusted certificate already serves the approver routes, set `server.approverPublicUrl` to its address instead. No second listener is needed then. [TLS and exposure]({{< relref "guides/operate/tls-and-exposure.md" >}}) shows both shapes.


{{< fails >}}
`Set server.approverTLS.listen to a free port, or server.approverTLS.autoMint: false to run without the dedicated approver surface`
: Another process holds the approver port, so strazad does not start. The message starts with `approver listen` and the port. Free the port, move the listener to another one, or turn it off as the sentence says.
{{< /fails >}}

## What the two profiles bind, observed


The [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}) runs the enterprise profile under Docker Compose with the loopback override, which rebinds the two published ports to `127.0.0.1`. The base compose file alone publishes `8420` and `8443` on every interface.

{{< command terminal="Docker host" purpose="the demo stack" >}}
```sh
docker ps --format '{{.Names}}\t{{.Ports}}' | grep strazad
```
{{< /command >}}

```text
straza-eval-strazad-1	127.0.0.1:8420->8420/tcp, 127.0.0.1:8443->8443/tcp
```

{{< see >}}Both ports are bound to `127.0.0.1`.{{< /see >}}


A standalone server with no configuration file binds the main listener to loopback and the approver listener to every interface. This is what `netstat` inside the container shows after the first start.

{{< command terminal="Inside the container" purpose="standalone, no configuration file" >}}
```sh
netstat -tln
```
{{< /command >}}

```text
Proto Recv-Q Send-Q Local Address           Foreign Address         State
tcp        0      0 127.0.0.1:8420          0.0.0.0:*               LISTEN
tcp        0      0 :::8443                 :::*                    LISTEN
```

{{< see >}}Port 8420 on `127.0.0.1` and port 8443 on every interface.{{< /see >}}

The start log names the same two listeners. The approver's public URL is guessed from the container's own address, which is right on that network and wrong everywhere else.

{{< details summary="Recorded start log" >}}
```text
{"time":"2026-09-28T20:03:44.263721273Z","level":"INFO","msg":"approver surface serving","addr":"[::]:8443","publicUrl":"https://172.17.0.4:8443","pin":"sha256/fhsumsu3W/yi4vUhvo4l5RBud2S3GxLjvjbuH8C1v8g=","cert":"data/approver-tls/cert.pem","certExpires":"2028-12-26T20:03:43Z","autoMinted":true}
{"time":"2026-09-28T20:03:44.26376128Z","level":"INFO","msg":"strazad serving","addr":"127.0.0.1:8420","profile":"standalone","publicUrl":"http://127.0.0.1:8420","tls":false,"version":"v1.1.0"}
```
{{< /details >}}

## Outbound connections


strazad, straza, strazactl and the console send no usage data, and none of them checks for updates. Every destination in the table below is a system you configure, except the push relay at `https://push.straza.ai`, which is the only service Straza operates that strazad dials. A standalone server started with no configuration file opens no outbound connection at all.


The relay is off when strazad runs on its own. The Helm chart, the compose file at `deploy/compose/docker-compose.yaml` and the demo stack turn it on, so that the approver app on iOS and Android gets requests with no Apple or Firebase account on your side. With the relay on, strazad registers with an empty request and gets an anonymous token. Then it sends one message per phone notification. Each message holds the phone's platform and push route, a delivery deadline, the approval's reference and whether the request waits for a decision or changed its status. The command, its arguments and every name stay in your deployment. The relay also sees the address each request comes from, as any server does. To turn the relay off, set `approval.push.relay.enabled: false`, or `pushRelay.enabled: false` in the chart, or `STRAZA_APPROVAL_PUSH_RELAY_ENABLED=false` for the compose file. [The hosted relay]({{< relref "guides/approve/push-and-connectivity.md#the-hosted-relay" >}}) explains the lane and its limits.


Everything else a deployment talks to, strazad dials. Each row names the destination, the setting that names it, and when strazad connects.

| Destination | Named by | When strazad dials it |
|---|---|---|
| Your identity provider | `oidc.issuer`, or `oidc.discoveryUrl` when strazad reaches the provider by another address | In the enterprise profile, to fetch the provider's discovery document and keys and to verify ID tokens. Standalone is its own issuer. |
| Postgres | `store.dsn` | On every store operation in the enterprise profile. Standalone keeps SQLite under `dataDir`. |
| NATS JetStream | `events.url` | In either profile, once `events.embedded` is `false` and `events.url` names the server. `STRAZA_EVENTS_URL` does both in one step. Otherwise the bus runs inside strazad with no socket. |
| Slack | `approval.channels.slack.*` | `chat.postMessage` to `slack.com/api` when an approval request notifies Slack. The button press comes back inbound on `/v1/approval/callbacks/slack`. |
| Firebase Cloud Messaging | `approval.push.fcm.*` | `oauth2.googleapis.com` for an access token, then `fcm.googleapis.com` for each notification |
| Apple Push Notification service | `approval.push.apns.*` | `api.push.apple.com`, or the sandbox host when `environment` selects it |
| WebPush and UnifiedPush endpoints | `approval.push.allowedPushHosts` | The endpoint each phone or browser registered, only when its host is on the allowlist. An empty allowlist refuses every registration. |
| A push relay | `approval.push.relay.url`, by default `https://push.straza.ai` | For each phone notification while `approval.push.relay.enabled` is true |
| OAuth providers | `oauth.providers` and the `tokenUrl` of each, which the `github` provider fills in itself | To redeem a person's sign-in for a server, to refresh that token, and to get an AI agent's own token for a server whose manifest sets `credential.agents: client_credentials` |
| Webhook sinks | `sinks[].url` | One HMAC-signed POST per event, or per batch, from a durable consumer |
| Upstream MCP servers | The manifest's `runtime` block | A `remote` server is dialed at the manifest's URL. An `oci` server pulls an image from its registry. Both `oci` and `command` servers run beside strazad. A remote server that only a draft names is dialed once, with no credential, when a person asks the draft to contact it. |
| An object store for transcripts | `capture.bodyStore.endpoint` | When transcript bodies are kept outside the database, in the enterprise profile |


strazad makes no connection to your identity manager to sync identities. The identity manager pushes users and role membership over SCIM to `/scim/v2/`, and a pull connector reads `/v1/admin/`, both with an admin API token whose scope names its areas. An identity manager behind its own firewall therefore needs one route to strazad, and strazad needs none to it.


An agent's machine accepts no connection, and it makes two kinds of outbound connection.

- The straza client dials strazad's main listener for enrollment, check-in, decisions, the audit upload and the revocation stream at `/v1/push`. That stream is server-sent events on the same address, so no client ever reaches the event bus.
- In the enterprise profile the client also dials your identity provider. A person's machine does so once, at enrollment, for the device flow. An AI agent that signs in with `STRAZA_CLIENT_ID` and `STRAZA_CLIENT_SECRET` does so at enrollment and at every session start.

## Compose and Helm


The Helm chart's Service exposes `service.port` 8420. It leaves the approver listener off by default, and when you turn it on the chart publishes `approverTLS.port` 8443 on the Service.

The chart's Ingress renders one rule for the whole main listener, so it belongs on an internal ingress class, a VPN or an authenticating edge. A deployment that must split the surface leaves `ingress.enabled` off and routes with its provider's own objects.

Unlike strazad on its own, the chart turns the push relay on by default. A pod then sends phone notifications through `https://push.straza.ai` until you set `pushRelay.enabled: false`.


Beside strazad, the demo stack publishes ports of its own. None of them belongs to Straza.

| Service | Port |
|---|---|
| Keycloak | 8480 |
| midPoint | 8087 |
| The landing page | 8400 |
| Elasticsearch, with the SIEM file | 9200 |
| Kibana, with the SIEM file | 5601 |

The loopback override binds Keycloak, midPoint and the landing page to `127.0.0.1`. The SIEM file binds Elasticsearch and Kibana to `127.0.0.1` on its own.
