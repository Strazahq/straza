---
title: Demo stack defaults and the hardened shape
description: Every lab shortcut of the demo stack, the hardened shape that keeps it on the host's loopback, and what keeps it running for a team.
pagetype: reference
weight: 26
draft: false
keywords: demo eval stack compose defaults hardened shape loopback overlay siem tunnels ssh cast seeder volumes keycloak midpoint elasticsearch teardown lab shortcuts
---


The demo stack trades safety for a short first run: its logins are public, it serves plain HTTP and the launcher opens its ports on every interface of the host. This page lists each of those defaults, the hardened shape that keeps the stack on the host's loopback, and what an operator needs to keep the stack running for a team. [Try it on the demo stack]({{< relref "evaluate/try-it-on-the-demo-stack.md" >}}) is the tutorial that walks the stack.

## Demo defaults and the hardened shape


The landing page ends with its lab shortcuts, the choices that keep the demo short and that a deployment of your own would not make. Some change in the hardened shape.

| Lab shortcut | In the hardened shape |
|---|---|
| Every password the landing page prints is committed to the repository. So are the Postgres logins, the midPoint service account, the seed-bootstrap account, the Keycloak client secret, objects-demo's two tokens and the Kibana secrets. | Stays. The hardened shape makes these logins reachable from the host only. |
| Plain HTTP everywhere except the phone approver on 8443, whose certificate strazad makes itself. | Stays. The traffic travels only on the host's loopback and inside your SSH tunnel. [TLS and exposure]({{< relref "guides/operate/tls-and-exposure.md" >}}) covers a deployment that faces a network. |
| The minimum attestation is `none`, so any install of the client gets a session. The enterprise default `managed` refuses a plain enrollment until a managed install has registered its hash. | Stays. Both shapes set `STRAZA_MIN_ATTESTATION=none`. |
| Keycloak lets the seeder and joe's chat face sign in with a password, and joe's chat-face password is in his seed object. midPoint's Keycloak connector signs in as Keycloak's master admin. | Stays. The agents' own sessions ride Ed25519 keys made inside their service containers and never committed. |
| The push relay is on, and ntfy.sh and the browser push services are allowed push targets. A push carries a reference to the request, never its content. | Stays. |
| midPoint masters the Straza roles `straza-admin`, `straza-enroll-browser` and `straza-enroll-mobile`. | Stays, because this is the designed enterprise shape for production too. |
| The ports 8400, 8420, 8443, 8480 and 8087 listen on every interface. | Bound to 127.0.0.1 and reached over SSH. |


In the base file, Keycloak listens on 8480, strazad on 8420 and 8443, the landing page on 8400 and midPoint on 8087, all on every interface. Docker's own iptables rules run before firewalld or ufw, so a host firewall does not cover them. The loopback overlay, `compose.loopback.yaml`, rebinds those ports to 127.0.0.1 and pins the advertised approver URL to `https://127.0.0.1:8443`. Elasticsearch, Kibana and the demo chat face bind loopback on their own. The pin shows in `/version`: on a stack the launcher started, its `approver.public_url` is the address the launcher picked, and in the hardened shape it is `https://127.0.0.1:8443`.


On a host that the internet can reach, never run the launcher, because `up.sh` starts the base file alone. Start the base file with the loopback overlay instead, from the stack directory:

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
cd deploy/compose/eval-stack
docker compose -f compose.yaml -f compose.loopback.yaml up -d --build
```
{{< /command >}}

Compose builds what changed and starts the stack, and `docker compose ps` then shows every published port on `127.0.0.1`, as in [What runs](#what-runs). To add the other overlays, name them after these two in the order siem, demo. The walked stack runs all four, `-f compose.yaml -f compose.loopback.yaml -f compose.siem.yaml -f compose.demo.yaml`, so the later commands on this page pass all four. Pass only the files you started with.


The overlays and the profile each add one part:

| File or setting | Adds | Note |
|---|---|---|
| `compose.loopback.yaml` | The loopback binds and the pinned approver URL | Needs Docker Compose 2.24 or later, for `!override` |
| `compose.siem.yaml` | Elasticsearch and Kibana, fed by a strazad sink | About 1.5 to 2 GiB of memory more, by the file's own estimate |
| `compose.demo.yaml` | The chat face `demo`, and `straza-harness`, the Claude Code box that runs as joe | Builds from a checkout of the straza-agents-demo repository beside this one, which is not public |
| `COMPOSE_PROFILES=pg` in the stack directory's `.env` | adaptive-pg-mcp as the MCP server `postgres`, in front of four demo databases on midPoint's Postgres server | Off by default |

Without the straza-agents-demo checkout, leave `-f compose.demo.yaml` out of every command, and joe then has no runtime on the stack, which leaves sam as the one working agent that you can watch.


Reach a stack on another machine over SSH. `tunnels.sh user@host` on Linux or macOS and `tunnels.bat user@host` on Windows forward 8400, 8420, 8443, 8480, 8087, 5601 and 8477 to the same ports on your workstation. They need nothing but an SSH client, and the browser still opens every address as `localhost`. The script prints the ports it forwards and keeps the connection open until you press Ctrl+C.

## Pass the same files every time


Two shapes exist, and you keep the one you started with. The launcher starts the base file alone. In the hardened shape, every later `up`, `ps`, `logs` and `down` passes the files you started with, in the order base, loopback, siem, demo. The file chain decides what strazad reads.

- The base file mounts `straza.rs-mode.yaml` at `/etc/straza/straza.yaml`, and the siem overlay mounts `siem/straza-siem.yaml` at the same container path. Compose merges volumes by container path, so the later file wins, and strazad reads exactly one config file. That second file repeats the `oauth` and `admin` blocks of the first and adds the `sinks` list that posts every audit and revocation event to Elasticsearch.
- Drop the siem file from one command, and the next recreate mounts the base config again. The sink is gone and nothing reports it.
- Drop the demo file, and compose forgets the `demo`, `straza-harness` and `straza-harness-service` services. `up` skips them, `down` orphans them, and every step that needs joe stops working.

Confirm which config file the running server holds:

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
docker inspect straza-eval-strazad-1 --format '{{range .Mounts}}{{if eq .Destination "/etc/straza/straza.yaml"}}{{.Source}}{{end}}{{end}}' | sed 's#.*/eval-stack/##'
```
{{< /command >}}

```text
siem/straza-siem.yaml
```


The sink is alive when the index fills, which this count shows from the Docker host:

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
curl -s -u elastic:strazasiem http://127.0.0.1:9200/straza-events/_count
```
{{< /command >}}

Elasticsearch answers a JSON object whose `count` is the number of audit and revocation events delivered so far, and it grows with every decision.


While Elasticsearch is down, strazad parks the events it cannot post, and the console's **Overview** says so under **Needs attention**, with a button that replays them, **Replay elastic** on this stack. Start Elasticsearch again with the same files, then replay.

## What runs


List every container, the one-shot jobs that have exited included, from the stack directory. The command passes the four files of the hardened shape. On a stack that `up.sh` started, pass `compose.yaml` alone.

{{< command terminal="Terminal" purpose="on the Docker host, in the stack directory" >}}
```sh
docker compose -f compose.yaml -f compose.loopback.yaml -f compose.siem.yaml -f compose.demo.yaml ps -a --format 'table {{.Service}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}'
```
{{< /command >}}

The walked stack runs the pg profile too, and its Elasticsearch and Kibana were not running, so their rows read `Exited`.

```table
SERVICE                    IMAGE                                                                                                                STATUS                      PORTS
adaptive-pg-mcp            ghcr.io/strazahq/adaptive-pg-mcp:0.1.1@sha256:b3c7f9a1a9eea68762bb6cc3afa8e8ab804746d86e09a0d2c1263a3bb6881df2       Up 7 hours
agent-keys-init            busybox:1.36                                                                                                         Exited (0) 9 days ago
connector-jar              curlimages/curl:8.7.1                                                                                                Exited (0) 2 weeks ago
demo                       straza-eval-demo                                                                                                     Up 12 hours
demo-tools                 straza-eval-demo-tools                                                                                               Up 12 hours
docs-build                 ghcr.io/gohugoio/hugo:v0.165.0@sha256:608a19e34f86de36773503adbaab174fc28a6e338dc7904e03c70320b003a153               Exited (0) 2 weeks ago
docs-publish               python:3.13-alpine                                                                                                   Exited (0) 2 weeks ago
elasticsearch              docker.elastic.co/elasticsearch/elasticsearch:8.14.3                                                                 Exited (137) 13 hours ago
eval-seed                  alpine:3.20                                                                                                          Exited (0) 35 hours ago
kc-connector-build         maven:3.9-eclipse-temurin-17                                                                                         Exited (0) 2 weeks ago
keycloak                   quay.io/keycloak/keycloak:26.0                                                                                       Up 7 hours (healthy)        8443/tcp, 9000/tcp, 127.0.0.1:8480->8080/tcp
kibana                     docker.elastic.co/kibana/kibana:8.14.3                                                                               Exited (0) 11 days ago
kibana-setup               docker.elastic.co/elasticsearch/elasticsearch:8.14.3                                                                 Exited (0) 2 weeks ago
landing                    nginx:1.27-alpine                                                                                                    Up 7 hours                  127.0.0.1:8400->80/tcp
mcp-midpoint-http          ghcr.io/strazahq/midpoint-mcp-server:0.6.0@sha256:0b24ff42bb60eeb78a756f5db8b24e25a3cfc7887517cf8151e9c4e957194a00   Up 7 hours
midpoint                   evolveum/midpoint:4.10.3-alpine                                                                                      Up 7 hours (healthy)        127.0.0.1:8087->8080/tcp
midpoint-db                postgres:16-alpine                                                                                                   Up 7 hours                  5432/tcp
midpoint-init              evolveum/midpoint:4.10.3-alpine                                                                                      Exited (0) 2 weeks ago
midpoint-partition-guard   postgres:16-alpine                                                                                                   Exited (0) 2 weeks ago
nats                       nats:2                                                                                                               Up 7 hours                  4222/tcp, 6222/tcp, 8222/tcp
objects-demo               straza-eval-objects-demo                                                                                             Up 12 hours                 3002/tcp
pg-showcase-init           postgres:16-alpine                                                                                                   Exited (0) 4 days ago
siem-setup                 docker.elastic.co/elasticsearch/elasticsearch:8.14.3                                                                 Exited (0) 26 hours ago
straza-data-init           busybox:1.36                                                                                                         Exited (0) 26 hours ago
straza-db                  postgres:16                                                                                                          Up 7 hours (healthy)        5432/tcp
strazad                    straza-eval-strazad                                                                                                  Up 5 hours                  127.0.0.1:8420->8420/tcp, 127.0.0.1:8443->8443/tcp
uni-connector-build        curlimages/curl:8.7.1                                                                                                Exited (0) 2 weeks ago
views-demo                 straza-eval-views-demo                                                                                               Up 12 hours
straza-harness             straza-eval-straza-harness                                                                                           Up 12 hours
straza-harness-service     straza-eval-straza-harness-service                                                                                   Up 12 hours (healthy)
agent-sam                  straza-eval-agent-sam                                                                                                Up 12 hours (healthy)
agent-sam-service          straza-eval-agent-sam-service                                                                                        Up 12 hours (healthy)
```


The long-running services form these groups:

| Group | Services |
|---|---|
| Identity | `keycloak`, Keycloak 26 in dev mode, which imports the realm `straza` at boot. `midpoint`, midPoint 4.10.3, with its own `midpoint-db`. |
| Straza | `strazad` in the enterprise profile with the console embedded, `straza-db` and `nats`. NATS publishes no port, because the agents' services get the kill-switch push over strazad's own `/v1/push` route. |
| The governed side | `demo-tools`, `views-demo`, `objects-demo` and `mcp-midpoint-http`, the MCP servers that only the gateway reaches. midPoint's server runs every call as the person signed in, and `adaptive-pg-mcp` joins them with the pg profile. `landing` is the nginx behind the landing page and the docs under `/docs/`. |
| Agents | `agent-sam`, the always-on autonomous agent, with `agent-sam-service` beside it. With the demo overlay, `straza-harness`, a pinned Claude Code that you drive with `docker exec`, with `straza-harness-service` beside it, and `demo`, the chat face on 127.0.0.1:8477. |
| SIEM, from the siem overlay | `elasticsearch` and `kibana`. |


Each of the two service containers runs `straza service`, the Straza background service, from its agent's image under its own account, uid 64000. It keeps the agent's key in a volume that only it mounts, and it shares only the socket directory with its agent, so nothing the agent runs can read the key. The chat face `demo` has no service, because its gateway lane asks Keycloak for a realm and a client that this stack does not have.


Everything listed as `Exited (0)` is a one-shot job that ran to completion while its dependents waited on it. A failed job therefore stops the boot instead of leaving a half-built stack.

- The init jobs fix volume ownership, create the midPoint schema and pre-create the shadow partitions that midPoint 4.10.3 otherwise creates in the wrong order.
- The connector jobs check the vendored Straza connector jar against its pinned checksum and build the Keycloak connector from source. A Straza jar that does not match the pin stops the boot.
- `docs-build` and `docs-publish` render these docs, comment-free, on every `up`.
- `siem-setup` and `kibana-setup` prepare the ingest pipeline and the dashboards.
- `pg-showcase-init` makes the demo databases when the pg profile is on.
- `eval-seed` is the seeder, which [Bring it up, down and back](#bring-it-up-down-and-back) describes.


midPoint loads the Straza connector jar from the repository, pinned by checksum, and the seeder stops with an error when midPoint cannot discover it. The midPoint MCP server runs from its released image, `ghcr.io/strazahq/midpoint-mcp-server`, pinned by digest, so it needs no checkout. Its release workflow attests how the image was built, which this command checks. It exits 0 and prints nothing when the attestation holds, and `--format json` prints the attestation itself.

{{< command terminal="Terminal" purpose="any machine with the GitHub CLI" >}}
```sh
gh attestation verify oci://ghcr.io/strazahq/midpoint-mcp-server:0.6.0 --owner Strazahq; echo "exit $?"
```
{{< /command >}}

```text
exit 0
```

## The cast


Every person and agent except two local accounts is born in midPoint and reaches Straza over SCIM, while the seeder drives the rest of the setup in this order:

1. It signs in the way the console does, as the transient `seed-bootstrap` user on a first boot and as alice on a re-run. It mints a full-scope admin API token named `eval-seed` for its own run, which expires two hours after the mint and is revoked when the seeder exits. Then it mints the admin API token that midPoint holds, named `midpoint`.
2. Next it creates the Straza-born roles: the business roles `developer`, `operator` and `analyst`, the approver role `sec-approvers` and the Straza role `auditor`.
3. It imports the midPoint objects and waits for midPoint to provision the working agents.
4. Through the admin API it installs the MCP servers, so the console and `strazactl` can change them, and creates each application role on the one server it reaches, with its access row.
5. It registers the agents' Ed25519 public keys and activates the seeded policy sets: `agent-guardrails` and the gates of each application role that has any.
6. dave gets the role that administers every MCP server, and carol the role the `demo-tools` server names at its own registration.
7. Last, on a first boot, the seeder removes the admin role from `seed-bootstrap`, locks it in Straza and disables its Keycloak login.


Sponsorship is the accountability edge. alice sponsors joe and sam, so when either raises a gated call, Straza sends the request to her in the console, on the self-service page and on her enrolled phone. joe is supervised and sam is autonomous, and an autonomous agent can never approve its own request.


| Name | Who | Access from midPoint | Login |
|---|---|---|---|
| alice | employee, the accountable person, manager of dev-ops | `straza-admin`, `sec-approvers` and the enroll roles, sponsor of joe and sam | Keycloak, `alice` / `alice` |
| ivan | employee, the second approver | `sec-approvers` and a Keycloak login, no tools | Keycloak, `ivan` / `Ev4l-St4ck!26` |
| joe-java-developer-agent | supervised AI agent, the identity of the harness and the chat face | the `developer` business role, a Keycloak login, member of dev-ops | Ed25519 key for sessions, Keycloak password `Ev4l-St4ck!26` for the chat face |
| sam-sre-agent | autonomous AI agent of the `agent-sam` container | the `operator` business role | Ed25519 key only, no password anywhere |
| nina-data-analyst-agent, pam-personal-agent | an autonomous AI agent and carol's personal agent, both without roles | nothing until you assign a role in midPoint | none |
| jana | employee, the new hire of the scripted demo | no roles by design | none |
| carol | employee in dev-ops, the server admin of the demo | `AR:mcp-admin-demo-tools`, the role the `demo-tools` server names at its registration, and a Keycloak login | Keycloak, `carol` / `Ev4l-St4ck!26` |
| dave | employee, the global MCP admin of the demo | `BR:Straza-global-mcp-admin-access`, which hands out `straza-global-mcp-admin` over every MCP server, and a Keycloak login | Keycloak, `dave` / `Ev4l-St4ck!26` |
| erin, frank, grace, judy | employees in midPoint only, frank a manager | none | none |
| heidi | external contractor | no roles | none |
| mcp-service | technical account of the midPoint MCP server | two scoped midPoint roles, no superuser | midPoint REST only, password in the seed objects |
| seed-bootstrap | transient local admin the seeder drives on a first boot | admin while seeding, then demoted, locked and disabled | password in the realm import and the seeder, unused after seeding |
| break-glass | local emergency admin that strazad creates on every store | standing admin | printed once in the strazad log at first boot, used only on the emergency sign-in |


Every login in the stack is public by design, which is the reason the hardened shape binds every port to loopback. alice signs in to the console through Keycloak as `alice` / `alice`, and `strazactl` runs on an admin API token that she mints there. The landing page's Accounts table lists the logins of alice, ivan, carol, dave and joe beside those of the midPoint, Keycloak and Kibana administrators. Committed files of the stack hold the mcp-service and seed-bootstrap passwords too, and operating the stack never needs them.


The seeder's work shows in four listings. Each is trimmed to the rows a default boot makes: the walked stack also ran the pg profile, which adds the server `postgres`, its three application roles, its admin role and the set `postgres-helpdesk-access`, and it held rows other walks had added. The role listing leaves out the ID column.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl roles list
strazactl apps list
strazactl policy list
strazactl users list
```
{{< /command >}}

```table
NAME                     KIND         SERVER        DESCRIPTION
analyst                  business     -             The analyst role: composes demo-tools-readers and midpoint-self-service. Nobody holds it at boot; assign it to nina in midPoint and watch her land in Straza.
auditor                  straza       -             Read-only oversight: audit, sessions and transcripts. Opens the console, never agent tools.
demo-tools-readers       application  demo-tools    Read-only reach into the demo-tools server: the tools that only read or compute, named one by one. Defined by that server's own admin and assigned by the identity manager.
demo-tools-sandbox       application  demo-tools    Every tool of the demo-tools server, tools added later included. Composed by the developer role; get-sum is a hold and get-env is a ticket in demo-tools-sandbox-access.
developer                business     -             The developer role: composes demo-tools-sandbox, every tool of the demo-tools server, views-demo-tools, the MCP Apps example server, objects-demo-tools, the repo server, and midpoint-self-service, the midPoint reads plus request_role. joe holds it from midPoint.
mcp-admin-demo-tools     straza       -             Administers the MCP server demo-tools: its connection, credentials, settings and health, never its removal. It opens the console's MCP servers area for that server only.
mcp-admin-midpoint       straza       -             Administers the MCP server midpoint: its connection, credentials, settings and health, never its removal. It opens the console's MCP servers area for that server only.
mcp-admin-objects-demo   straza       -             Administers the MCP server objects-demo: its connection, credentials, settings and health, never its removal. It opens the console's MCP servers area for that server only.
mcp-admin-views-demo     straza       -             Administers the MCP server views-demo: its connection, credentials, settings and health, never its removal. It opens the console's MCP servers area for that server only.
midpoint-operations      application  midpoint      Every tool of the midPoint server. Composed by the operator role; every write waits for the person behind the call in midpoint-operations-access.
midpoint-self-service    application  midpoint      The midPoint reads plus the self-service pair whoami, ping, list_requestable_roles and request_role. Composed by the developer and analyst roles; request_role needs approval in midpoint-self-service-access.
objects-demo-tools       application  objects-demo  Every tool of the objects-demo server, ten repos behind tools that name the repo they reach. Composed by the developer role.
operator                 business     -             The operator role: composes demo-tools-readers, the read-only tools the demo-tools server's admin defined, and midpoint-operations, every midPoint tool with every write needing approval. sam holds it from midPoint.
sec-approvers            approver     -             The deciders of approval requests for governed agents. Reaches no tools. The identity manager assigns and certifies its holders.
straza-admin             straza       -             Straza administration
straza-draft-config      straza       -             Lets an agent propose config drafts through the built-in straza MCP server's tools straza__draft_submit and straza__draft_status. A person publishes them. It opens no console area, and straza-admin does not include it.
straza-enroll-browser    straza       -             May enroll a signed-in browser as an approval device.
straza-enroll-mobile     straza       -             May enroll their own phone as an approval device.
straza-global-mcp-admin  straza       -             Administers every MCP server: registration, changes, credentials and reach. It opens the console's MCP servers area, never agent tools.
views-demo-tools         application  views-demo    Every tool of the views-demo server, the MCP Apps example whose get-time opens a clock view. Composed by the developer role.
NAME          VERSION            RUNTIME  STATUS   REASON  SOURCE  TOOLS  REACHED BY                                            CHECKED
demo-tools    2026.8.31          remote   running  -       api     14     demo-tools-readers,demo-tools-sandbox                 8s ago
midpoint      0.6.0              remote   running  -       api     39     midpoint-operations,midpoint-self-service             8s ago
objects-demo  1.0.0              remote   running  -       api     5      objects-demo-tools                                    8s ago
views-demo    2.0.3              remote   running  -       api     1      views-demo-tools                                      8s ago
NAME                          PRIORITY  STATUS  ID
agent-guardrails              150       Live    01a1547c-7ecd-71f4-9e6d-e60755917566
demo-tools-readers-access     100       Live    01a1547c-7e44-742f-a728-12403ab4ff1e
demo-tools-sandbox-access     100       Live    01a1547c-7c03-7a18-949a-dbfff1ea0e53
midpoint-operations-access    100       Live    01a1547c-7d15-7706-9935-3062c54d5c69
midpoint-self-service-access  100       Live    01a1547c-7c8c-7491-ba50-d9e2e3ad1bc2
USERNAME                  STATUS  ORIGIN  EMAIL                           ID
alice                     active  scim    alice@example.com               01a1547c-188b-79d9-93b1-334e1fb5a148
break-glass               active  local                                   01a1547a-1c8b-7064-9dd5-4c8856981e74
carol                     active  scim    carol.jensen@example.com        01a1547c-a30a-7058-9fbf-5eb0ed3f09ba
dave                      active  scim    dave.okafor@example.com         01a1547c-8e36-73cd-8305-5f90f470cef8
ivan                      active  scim    ivan.petrov@example.com         01a1547c-2abb-71bc-a830-b3b118950435
joe-java-developer-agent  active  scim    joe@agents.example.com          01a1547c-366e-7767-97eb-caa598255d94
sam-sre-agent             active  scim    sam@agents.example.com          01a1547c-3f3d-7238-b13b-b5b6090ba343
seed-bootstrap            active  local   seed-bootstrap@example.invalid  01a1547a-327b-7c5b-87e4-ea07c76cba71
```

Every person and agent with the origin `scim` was created in midPoint and provisioned from there. `break-glass` and `seed-bootstrap` are the two local accounts. On the walked stack `nina-data-analyst-agent` was also present, because a walk had assigned her the analyst role in midPoint.


Four kinds of role appear. `developer`, `operator` and `analyst` are business roles that people and agents hold. Each composes application roles, and those application roles are what the active PolicySets match. An application role reaches one server and belongs to it, so the SERVER column names that server and midPoint shows the role as that server's role. `sec-approvers` decides approvals and holds no tools. A Straza role such as `auditor` carries rights inside Straza itself and no agent tools.


Four MCP servers come from the seeder, and a fifth with the pg profile:

| Server | What it is |
|---|---|
| `demo-tools` | The MCP reference server, published on no port |
| `midpoint` | midPoint's own MCP server. Each call runs in midPoint as the person signed in, and an agent runs on its sponsor's sign-in once the sponsor allows it on the self-service page. Its views are turned on |
| `views-demo` | The MCP Apps example server, whose clock view a chat app renders through `/mcp/views-demo`, as [Show MCP Apps views]({{< relref "guides/serve-mcp-apps/show-views.md" >}}) shows |
| `objects-demo` | A test server with ten repos behind tools that mark which argument names the repo. Straza lists them with the server's listing credential, and its console page gains a Repos tab |
| `postgres` | adaptive-pg-mcp in front of the four demo databases, with the pg profile only |

## Bring it up, down and back


Of all the jobs, the seeder finishes last, and its log, where every line carries the `[seed]` prefix, is the boot's verdict. `WARNING` marks population that did not land and never fails the seed. `FATAL` ends the run with a non-zero exit, so `ps -a` shows `eval-seed` as `Exited (1)`. The log keeps every run of the container, so read it from the end and expect the block that opens with `EVAL STACK READY`:

{{< command terminal="Terminal" purpose="on the Docker host, in the stack directory" >}}
```sh
docker compose -f compose.yaml -f compose.loopback.yaml -f compose.siem.yaml -f compose.demo.yaml logs --no-log-prefix eval-seed | grep -n -E 'READY|FATAL|WARNING'
```
{{< /command >}}

```text
166:[seed] EVAL STACK READY - the agent workforce was born in midPoint and
```

{{< see >}}The READY line, and no `WARNING` or `FATAL` line.{{< /see >}}


A seeder that dies is run again with `up eval-seed` on the same files, and it converges, because every step is idempotent. Roles answer that they are already present, the seeder's earlier tokens are revoked and minted again, and a PolicySet that exists is never touched. On a re-run the seeder signs in as alice, since `seed-bootstrap` is no longer an admin.


State lives in fifteen named volumes with the demo overlay, and eleven without it. `down` without `-v` removes every container and keeps everything that matters:

- both Postgres stores and midPoint's home
- strazad's data volume, with the key-encryption key, the approver TLS pair it made itself, the WebPush key and the relay token
- the state volumes of the `straza service` containers, with the agents' private keys and enrollments, and the socket volumes they share with their agents
- the Maven cache and the docs


The next `up -d` brings the same stack back with the same certificate pin, so enrolled phones keep working. Keycloak is the exception. It runs in dev mode with no volume, so a recreate of the `keycloak` container loses every change made in its admin console and every login midPoint made there. The realm import brings back `alice` and `seed-bootstrap`, enabled. The other logins return once midPoint provisions them again, which a run of the seeder starts. `down -v --remove-orphans` wipes the volumes, and the next boot is a fresh boot with new keys and a new pin, so phones scan again.

## Verify


After an `up`, confirm the version and the profile `enterprise` with `curl -s http://localhost:8420/version`, as the tutorial does. Then confirm that the docs are served:

{{< command terminal="Terminal" purpose="any machine that reaches the stack" >}}
```sh
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8400/docs/
```
{{< /command >}}

```text
200
```

The last check is `agent-sam` reporting `(healthy)` in the listing of [What runs](#what-runs). Its healthcheck passes only while sam's governed heartbeat passes. The heartbeat runs every ten minutes, so a sam disabled in midPoint turns the container unhealthy within ten minutes, and enabling sam again heals it with no restart.

## When something refuses


| What you see | Why | What to do |
|---|---|---|
| `Straza: approval ticket … is pending.` from `straza exec` | A ticket rule holds the command until a person decides | Decide it under **Approvals**, then run the same command again within the grant window |
| The same sentence again, with a new reference, after an approval | The grant was used once already, or its hour passed | Approve the new ticket, then run the same command again |
| `Straza: user is disabled. Contact your administrator Refusing to run.` from `straza exec` | The agent is disabled in midPoint, and Straza refuses its session | Enable the agent in midPoint |
| `eval-seed` reads `Exited (1)` | The seeder hit a `FATAL` step | Read its log from the end, fix the cause it names, then run `up eval-seed` on the same files |
| **Needs attention** on Overview says a sink holds parked events | The sink's target, Elasticsearch here, did not answer | Start Elasticsearch with the same files, then press **Replay elastic** |

## An agent with a login


sam has no login anywhere, so stopping sam needs only Straza. joe also has a Keycloak login for the chat face, given by an explicit assignment in midPoint. When midPoint disables joe, it sends the same status to both of its resources: Straza deactivates him over SCIM, and Keycloak turns his login off. Disabling a login in Keycloak alone stops new sign-ins and leaves running Straza sessions alive. Deactivating over SCIM alone leaves the login usable. A leaver flow has to do both, and here it does because midPoint provisions both resources from the one status.

Ask the Keycloak admin API whether joe's login is on, with the lab admin login `admin` / `admin`:

{{< command terminal="Terminal" purpose="Keycloak admin API" >}}
```sh
KCT=$(curl -s -d client_id=admin-cli -d username=admin -d password=admin -d grant_type=password http://localhost:8480/realms/master/protocol/openid-connect/token | jq -r .access_token)
curl -s -H "Authorization: Bearer $KCT" 'http://localhost:8480/admin/realms/straza/users?username=joe-java-developer-agent&exact=true' | jq '.[0].enabled'
```
{{< /command >}}

It answers `true` while joe is enabled and `false` after midPoint disables him. It answers `null` when Keycloak holds no login for joe, as after a recreate of the `keycloak` container before the seeder has run again.

## Tear it down


Stop the stack and delete its volumes. Name every overlay you ever started, because compose stops only the services it can see in the files you pass.

{{< command terminal="Terminal" purpose="on the Docker host, from the repository root" >}}
```sh
docker compose -f deploy/compose/eval-stack/compose.yaml \
  -f deploy/compose/eval-stack/compose.loopback.yaml \
  -f deploy/compose/eval-stack/compose.siem.yaml \
  -f deploy/compose/eval-stack/compose.demo.yaml down -v --remove-orphans
```
{{< /command >}}

Compose stops and removes every container, network and named volume of the project. The next `up` is a fresh boot.

{{< details summary="How this page was checked" >}}

On 2026-11-02 these ran against the demo stack on this host, built at v1.0.0-1695-gdec5ce12 and started with the four files: the container listing, the config mount, the seeder verdict, the docs check, the volume count and the Keycloak query. The stack's Elasticsearch was stopped, so the index count did not run. Nothing ran that would restart or remove the shared stack: not the launcher, the four-file `up`, the tunnels or the teardown. Its Keycloak had been recreated without a seeder run, so it held no login for joe, and joe's disable was not repeated for this page.
{{< /details >}}
