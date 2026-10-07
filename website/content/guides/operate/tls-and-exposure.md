---
title: TLS and exposure
description: Serve strazad over HTTPS, give phones their own TLS listener, and keep every route that should stay private off the internet.
pagetype: how-to
weight: 50
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: Alpine 3.20 container in the standalone profile, laid out as the standalone host guide describes, for the self-signed pair, the native TLS restart, both curl checks, the SSL_CERT_FILE hint with strazactl status, the approver port refusal and the version check. The enterprise plaintext warning and the approver listener line were read from the log of the running demo stack at v1.1.0-104-g07d2df0e, and the console row was not clicked
  date: 2026-10-06
applies_to: both
who: You, as the operator of the server
where: The strazad config file and a terminal on the host
steps: true
keywords: tls listener exposure certificate approver pin plaintext
---


You decide where TLS terminates for each of strazad's two listeners, and which routes each network may reach, in the server's config file. Do it before a client connects from another machine. At the end clients and phones reach strazad over TLS, and only the routes built for a hostile network face one.

| Listener | What it serves |
|---|---|
| Main, on port 8420 by default | Everything an operator or an agent uses: the admin API, the console, the MCP gateway, the SCIM endpoint, the metrics endpoint and the approver routes. |
| Approver, on port 8443 by default | Only the approver routes, over TLS, so the Straza approver app can enroll against a server whose main listener is plaintext inside a trusted network. |

The phone trusts the approver listener by a pinned key rather than a certificate chain. That makes a self-signed certificate enough, and it makes replacing the certificate expensive.

## Choose the listener shape

| If you need | Choose | Then read |
|---|---|---|
| Straza itself to serve HTTPS on its main listener | Native TLS | [Put TLS on the main listener](#put-tls-on-the-main-listener) |
| A load balancer or ingress to serve HTTPS | Edge TLS on a trusted hop to Straza | [Or terminate at the edge](#or-terminate-at-the-edge-on-a-trusted-network) |
| Phones to reach a separate TLS listener | Dedicated approver listener | [Give the phone its own listener](#give-the-phone-its-own-listener) |
| An ingress that already serves approver routes with a trusted certificate | `server.approverPublicUrl` | [Give the phone its own listener](#give-the-phone-its-own-listener) |

Only approver routes and `/readyz` are designed to face a hostile network. Keep the console, admin API, gateway, SCIM endpoint, and metrics on a restricted network. [Decide what faces the internet](#decide-what-faces-the-internet) explains the boundary.

## Put TLS on the main listener


Native TLS uses `server.tls.certFile` and `server.tls.keyFile`, or the environment variables `STRAZA_TLS_CERT_FILE` and `STRAZA_TLS_KEY_FILE`. Straza loads the pair at boot and serves TLS 1.2 or newer with Go's current cipher defaults. A renewed certificate takes effect at the next restart.

No knob skips verification anywhere in the product, on the server or in the clients. Clients must trust the certificate, so distribute your private CA's root where needed.

For a deployment, obtain a certificate for the hostname clients use. For a loopback test, this POSIX-shell example creates a self-signed pair in the paths used below. It needs OpenSSL and write access to `/srv/straza/tls`, and you run it once, not on each restart.

{{< command terminal="Terminal" purpose="on the host, once" >}}
```sh
mkdir -p /srv/straza/tls
(umask 077; openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
  -nodes -days 365 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 \
  -keyout /srv/straza/tls/key.pem -out /srv/straza/tls/cert.pem)
```
{{< /command >}}

Add the following settings to `/srv/straza/straza.yaml`. If the file already has a `server` key, merge them into it and do not append a second one. This example uses the loopback certificate described above:

```yaml
server:
  publicUrl: https://127.0.0.1:8420
  tls:
    certFile: /srv/straza/tls/cert.pem
    keyFile: /srv/straza/tls/key.pem
```

Restart the server with that configuration. For a shared deployment, use the HTTPS hostname clients actually reach and a certificate that names it. The self-signed test certificate is in no trust store, so on Linux run `strazactl` and `straza` with `SSL_CERT_FILE=/srv/straza/tls/cert.pem` in their environment while you test on this host.


{{< see >}}The serving line shows `tls=true` and `publicUrl=https://127.0.0.1:8420`.{{< /see >}}

Straza does not derive the advertised URL from the TLS settings. A serving line with `tls=true` next to an `http` public URL means the file is missing `publicUrl`. Check the listener from a shell, once over HTTPS and once over plain HTTP:

{{< command terminal="Terminal" purpose="on the host" >}}
```sh
curl -s --cacert /srv/straza/tls/cert.pem https://127.0.0.1:8420/healthz
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8420/healthz
```
{{< /command >}}

```text
{"status":"ok"}
400
```

{{< see >}}The HTTPS request answers `{"status":"ok"}`, and the plain HTTP request gets a 400 with the body `Client sent an HTTP request to an HTTPS server.` Nothing answers unencrypted on that port any more.{{< /see >}}

### Or terminate at the edge, on a trusted network


Instead of native TLS, you can terminate TLS at a load balancer or ingress in front of a plaintext pod. The connection from that proxy to the pod must use a trusted network, because it carries session tokens and admin API tokens. The enterprise profile logs a warning when its own listener is plaintext. On the demo stack it reads:

```text
{"time":"2026-10-06T10:26:39.17675033Z","level":"WARN","msg":"serving PLAINTEXT HTTP in the enterprise profile. Set server.tls.{certFile,keyFile} or terminate TLS at your ingress/LB; session tokens and admin API tokens transit this listener. The TLS and exposure guide walks both shapes","docs":"https://docs.straza.ai/guides/operate/tls-and-exposure/"}
```


The standalone profile logs no such warning, because its listener defaults to the loopback address and the warning keys on the profile rather than on the address. A standalone server that you move to a routable address is plaintext without a word from the server, so treat that step as the moment to add TLS.

## Give the phone its own listener


The Straza approver app connects only over TLS, to a key it pinned at enrollment, while a main listener may be plaintext by necessity. The approver listener closes that gap, and each profile sets it up differently:

- The standalone profile mints the pair for you on the first boot. It writes a self-signed certificate under `approver-tls` in the data directory, valid for 820 days, opens the listener on port 8443, and guesses a public URL from the host's first LAN address.
- The enterprise profile leaves the listener off. Bring the four settings together, `server.approverTLS.listen`, `certFile`, `keyFile` and `publicUrl`, or set `server.approverTLS.autoMint: true` as the demo stack does.

Every boot logs the three facts an enrollment stands on, here from the demo stack:

```text
{"time":"2026-10-06T10:26:39.177374693Z","level":"INFO","msg":"approver surface serving","addr":"[::]:8443","publicUrl":"https://127.0.0.1:8443","pin":"sha256/FWLhewF1uqb8mj2+J0OTVM2TuIE8vNQdt5lYjwWmpy4=","cert":"/var/lib/straza/approver-tls/cert.pem","certExpires":"2028-12-29T16:18:42Z","autoMinted":true}
```


That listener answers only `/v1/approver/*` and `/readyz`, and throttles each client address to ten requests per second by default. When its port is taken, strazad refuses to boot, naming the port and the `autoMint: false` way out.

The pin is the price. The enroll QR carries the key of the certificate this listener serves, so a new pair is a new pin, and every enrolled phone must enroll again. For that reason the server never replaces the pair on its own, and the Helm chart never mints one, because an emptyDir would mint a different pin per pod and per restart.

{{< now title="Treat the approver pair as backup material" >}}Rotate this certificate on a cadence of years, and warn your approvers first.{{< /now >}}


When an ingress already serves the approver routes on a public name under a publicly trusted certificate, you need no second listener. Set `server.approverPublicUrl` to that name instead, and enrollments hand the phone a URL it trusts through the chain, with no pin to mint and nothing to strand on renewal.

## Decide what faces the internet


Only the approver routes and `/readyz` are designed to face a hostile network. Even those want a rate limit at the edge in front of an ingress, because the server's own throttle guards the dedicated listener and never the main one.

| Route | Where it belongs |
|---|---|
| `/v1/approver/*` and `/readyz` | May face the internet, behind a rate limit at the edge. |
| The console, the admin API, the gateway and the SCIM endpoint | A host that a VPN, an internal ingress class or an authenticating edge restricts. |
| `/metrics` | The private network only. It is unauthenticated unless `server.metricsToken` is set. |

/metrics labels its counts by MCP server, sink, route, status, effect and lane. No metric names a session or a user. Its count of each route's answers by status still shows whether the policy snapshot route answers 200 or 503, so never route it through a public ingress. [Metrics and alerts]({{< relref "guides/operate/metrics-and-alerts.md" >}}) sets the token and says what each metric counts.

A plain Kubernetes Ingress cannot express a host and path allowlist with a rate limit. Deployments that split the surface therefore route with their provider's own objects and leave the chart's ingress off.

## Read it back


`/version` on the main listener reports the pin, the certificate path and the expiry the phones depend on. With the TLS listener from above and `jq` installed, ask:

{{< command terminal="Terminal" purpose="on the host" >}}
```sh
curl -s --cacert /srv/straza/tls/cert.pem https://127.0.0.1:8420/version | jq .approver
```
{{< /command >}}

```text
{
  "public_url": "https://172.17.0.3:8443",
  "tls_spki_pin": "sha256/mXuATf8ReGkPmPatTu4tcAWng12/D0IrmovLCErARvQ=",
  "cert_not_after": "2029-01-03T21:38:33Z",
  "auto_minted": true,
  "cert_file": "/var/lib/straza/approver-tls/cert.pem"
}
```

| Field | What it names |
|---|---|
| `public_url` | The address the enroll QR carries. |
| `tls_spki_pin` | The pin every enrolled phone holds. |
| `cert_not_after` | The certificate's expiry. |
| `auto_minted` | Whether the server minted the pair. |
| `cert_file` | The certificate's path. |

On an enrolled machine, `straza doctor` confirms that the approver surface presents the pinned key from where that machine stands.


In the console, **Settings**, then **Configuration**, shows the row **TLS terminated by strazad** as `on` or `off`, with **Public URL** just above it.

## Undo {.nostep}


Removing the two `server.tls` keys and restarting returns the main listener to plaintext. Deleting the `approver-tls` directory makes the next standalone boot mint a new pair, and every phone then enrolls again.
