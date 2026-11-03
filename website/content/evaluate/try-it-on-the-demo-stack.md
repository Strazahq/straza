---
title: Try it on the demo stack
description: Run Straza with midPoint, Keycloak, AI agents and MCP servers on one machine, and see what the seeded stack does.
pagetype: tutorial
weight: 50
who: You, as alice, the admin of the stack
where: A terminal and a browser, on the machine that runs Docker or through SSH tunnels to it
steps: true
modes: [console, cli]
mode_default: console
draft: false
summary: >-
  A Docker Compose project runs strazad with midPoint as the IGA, Keycloak for sign-in, AI agents and MCP servers. You sign in as alice, watch Straza allow, deny and hold commands from the agent sam, and approve the held deploy. Then you disable sam in midPoint and Straza stops it.
aliases:
  - /guides/operate/enterprise-compose-stack/
  - /get-started/enterprise-demo-stack/
tested:
  version: v1.0.0-1695-gdec5ce12
  platform: the running demo stack on Rocky Linux 10.2, x86_64, with Docker Engine 29.9.2 and the four compose files of the hardened shape, with the browser steps in headless Chromium
  date: 2026-11-02
  except: the first boot, which ran before this walk and was not repeated
applies_to: enterprise
keywords: demo eval stack compose midpoint keycloak enterprise sam agent allow deny hold ticket approve kill switch disable scim seeder try
---


The demo stack is a Docker Compose project that runs strazad on the enterprise profile together with the systems an enterprise connects to it. midPoint is the [IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}). It creates the people and AI [agents]({{< relref "concepts/identities.md" >}}), sends them to Straza over SCIM, and creates a Keycloak account for each person. Keycloak is the identity provider that signs people in. The agent sam was created in midPoint and runs in its own container.

You bring the stack up and sign in as alice. Then you run three of sam's commands: a [policy]({{< relref "concepts/policy-model.md" >}}) allows `ls`, denies `rm -rf`, and holds a deploy until alice [approves]({{< relref "concepts/approvals-model.md" >}}) it. Last, you disable sam in midPoint and see Straza refuse its next command.


{{< diagram name="eval-stack" caption="What runs on the demo stack and what the seeder sets up. Each linked part opens its page." >}}

## Before you start {.nostep}


- Docker Engine with the compose plugin, on a machine with 8 GB of memory and four processors, the size this page was walked on. [Requirements and sizing]({{< relref "reference/requirements-and-sizing.md" >}}) covers a real deployment.
- A clone of the Straza repository, and `curl`.
- On Docker Desktop, host networking turned on, in version 4.34 or later, because sam's container uses it.
- For the CLI steps, `strazactl` on your PATH, as [Put the binaries on your path]({{< relref "guides/operate/install.md#put-the-binaries-on-your-path" >}}) shows.


{{< now title="Run it on a network you trust" >}}Every login in the stack is public and it serves plain HTTP. The launcher also opens its ports on every interface of the machine. Run it only on a network you trust, or in the hardened shape that [Demo stack defaults and the hardened shape]({{< relref "guides/operate/demo-stack-defaults.md" >}}) describes.{{< /now >}}

## Bring the stack up


From the repository root, start the stack and follow the seeder, the one-shot job that finishes the setup.

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
deploy/compose/eval-stack/up.sh
docker compose -p straza-eval logs -f eval-seed
```
{{< /command >}}

`up.sh`, or `up.ps1` on Windows, starts the base compose file alone. It also hands strazad your machine's address, so a phone you pair later can reach the approver. A first boot builds the Straza images and pulls the third-party ones, so it takes longer than later boots. Its time was not measured for this page.

{{< see >}}Near its end the seeder prints `EVAL STACK READY`, then the addresses to open.{{< /see >}}

{{< details summary="Recorded output" >}}
The end of the seeder's log on the walked stack, trimmed to the lines about sam and the addresses:

```text
[seed] sam-sre-agent is in Straza (SCIM) ✔
[seed] sam-sre-agent has no Keycloak account ✔ (the human gate on the access packages holds, so agents ride the key lane)
[seed] alice is in Straza with straza-admin assigned by midPoint ✔ (origin: the identity manager)
[seed] sam-sre-agent holds operator, assigned by midPoint ✔ (the operator role)
[seed] ============================================================
[seed] EVAL STACK READY - the agent workforce was born in midPoint and
[seed] provisioned into Straza (SCIM), zero manual steps: joe (supervised
[seed] Java developer agent, the harness identity and chat face, with an
[seed] explicit Keycloak login) holds the developer role and sam (autonomous
[seed] SRE agent, key lane only) the operator role. Both are sponsored by
[seed] alice: Straza sends their gated calls to her phone.
[seed] Landing page: http://localhost:8400  <- start here
[seed] Console:  http://localhost:8420/console/   (alice / alice)
[seed] midPoint: http://localhost:8087/midpoint   (administrator / $MP_ADMIN_PASSWORD)
[seed] Kill-switch demo: disable sam-sre-agent in midPoint -> auth AND access die.
```
{{< /details >}}

## Sign in as alice


The landing page at `http://localhost:8400` lists the stack's accounts and addresses, with walkthroughs.

Open the console at `http://localhost:8420/console/` and press **Sign in**. Keycloak opens in the same tab. Sign in as `alice` with the password `alice`, and Keycloak sends the tab back. alice is an admin because midPoint assigned her the admin role, and she is the [sponsor]({{< relref "concepts/identities.md" >}}) of sam, the person who answers for it.

{{< see >}}The console opens on **Overview**, and the foot of the menu reads `enterprise` and `strazad v1.0.0-1695-gdec5ce12`, the build you started.{{< /see >}}

{{< cli >}}

`strazactl` has no sign-in of its own. It runs on an admin API token that alice mints in the console.

{{< clicks "Settings" "API tokens" "New API token" >}}

Name it `alice-terminal`, pick `expires in 30 days` and the job **Full root**, and press **Mint token**. Copy the token, because the sheet shows it once. Paste it at the `read` line below and press Enter, which keeps it off the screen and out of the shell history.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
export STRAZA_SERVER=http://localhost:8420
read -rs STRAZA_API_TOKEN && export STRAZA_API_TOKEN
curl -s "$STRAZA_SERVER/version"
```
{{< /command >}}

```text
{"version":"v1.0.0-1695-gdec5ce12","commit":"dec5ce12","go":"go1.26.10","os":"linux","arch":"amd64","profile":"enterprise","runtimes":["remote"],"approver":{"public_url":"https://127.0.0.1:8443","tls_spki_pin":"sha256/u9od/8QtLsBKV5mBRNSqLA2xMXq4YwaA16Sqzt5Zv2A=","cert_not_after":"2029-01-16T14:04:07Z","auto_minted":true,"cert_file":"/var/lib/straza/approver-tls/cert.pem"}}
```

`version` names the build you started and `profile` is `enterprise`. Whoever holds the token acts as an admin until it expires, so keep it in this terminal and never where an agent runs.
{{< /cli >}}

## Watch sam's commands


sam runs in the container `straza-sam`, and a Straza service in the container beside it holds sam's key. `straza exec` asks Straza about one command before it runs it. On the enterprise profile a command is denied until a policy allows it. The seeded policy `agent-guardrails` allows sam's shell, denies destructive commands, and turns a deploy into a ticket that a person approves.

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
docker exec straza-sam straza exec -- ls
docker exec straza-sam straza exec -- rm -rf build/
docker exec straza-sam straza exec -- ./deploy prod
```
{{< /command >}}

{{< see >}}`ls` runs and prints `deploy`. `rm -rf` is refused with `Straza: destructive command denied by the agent guardrails`. The deploy is refused with `Straza: approval ticket … is pending` and the places a person decides.{{< /see >}}

{{< details summary="Recorded output" >}}
```text
deploy
Straza: destructive command denied by the agent guardrails
Straza: approval ticket 01a19df2-a1b3-7bcf-9326-5aaae4171f10 is pending. A person decides within 24 hours, on the self-service page (http://localhost:8420/self-service/), the console, or an enrolled phone. Wait for the decision with the straza__approval_await tool (ref 01a19df2-a1b3-7bcf-9326-5aaae4171f10), or retry this exact call after approval; it fails if denied or expired.
```
{{< /details >}}


`straza exec` decides the command it is given and not the processes that command starts. An agent's calls to MCP servers go through the gateway instead, which is a security boundary. [The security model]({{< relref "evaluate/security-model.md" >}}) explains what each path enforces.

## Approve the deploy as alice


The ticket goes to sam's sponsor alice and to every holder of the approver role `sec-approvers`, and the first answer counts. In the console:

{{< clicks "Approvals" "Requests" "Approve" "Approve request" >}}

The row reads `./deploy prod` for `sam-sre-agent`, decided by `alice or sec-approvers`, with about 24 hours left. The dialog says the next such call by sam within the grant window runs once.

{{< see >}}`Approved. ./deploy prod may run once within the grant window for sam-sre-agent.`{{< /see >}}

{{< cli >}}

`strazactl approvals list` shows the waiting ticket, but `strazactl` does not decide, because a decision must name the person who made it. alice decides in the console as above, or on the self-service page.
{{< /cli >}}

Now run the deploy again, within the hour.

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
docker exec straza-sam straza exec -- ./deploy prod
```
{{< /command >}}

```text
deploy: pretending to ship prod (eval demo artifact, nothing real happens)
```

{{< fails >}}
`Straza: approval ticket … is pending.` again, with a new reference
: The grant was already used, or more than an hour passed since the approval. Approve the new ticket, then run the deploy once more.
{{< /fails >}}


The grant is used once, and a third run raises a new ticket. Each of these decisions, from the first deny to the use of the grant, is a record on the [audit chain]({{< relref "concepts/evidence.md" >}}).

## Disable sam in midPoint


midPoint owns every identity in the stack, so you disable sam in midPoint. Disable sam over midPoint's REST interface with the object id its seed object carries. The midPoint administrator password is the lab default the landing page lists.

{{< command terminal="Terminal" purpose="midPoint REST" >}}
```sh
curl -u 'administrator:StrazaEval5ecr3t!' -H 'Content-Type: application/json' -X PATCH \
  -d '{"objectModification":{"itemDelta":[{"modificationType":"replace","path":"activation/administrativeStatus","value":"disabled"}]}}' \
  http://localhost:8087/midpoint/ws/rest/users/b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c48
docker exec straza-sam straza exec -- ls
```
{{< /command >}}

```text
Straza: user is disabled. Contact your administrator Refusing to run.
```

midPoint answers with no body and sends the change to Straza over SCIM, where Straza revokes every session sam holds and refuses to open a new one until midPoint enables sam again. So the `ls` that ran a minute ago is refused. In the walk, Straza listed sam as disabled as soon as midPoint answered, under two seconds after the request.


{{< diagram name="kill-switch" caption="What the disable starts in Straza." >}}

{{< console >}}

On **Users**, set the **Status** filter to `disabled`, open `sam-sre-agent`, and press **open** after **Audit trail**.

{{< see >}}sam's row reads `disabled` and `locked`, and the sheet says `Locked by Straza. deactivated via SCIM`. The audit trail holds `user.killed` with the reason `deactivated via SCIM`, and the refused `login` with `user is disabled`.{{< /see >}}
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl users list
```
{{< /command >}}

```table
USERNAME                  STATUS    ORIGIN  EMAIL                           ID
sam-sre-agent             disabled  scim    sam@agents.example.com          01a1547c-3f3d-7238-b13b-b5b6090ba343
```

The listing is trimmed to sam's row.
{{< /cli >}}


To put sam back, send the same request with the value `enabled`, then let sam run a command.

{{< command terminal="Terminal" purpose="midPoint REST" >}}
```sh
curl -u 'administrator:StrazaEval5ecr3t!' -H 'Content-Type: application/json' -X PATCH \
  -d '{"objectModification":{"itemDelta":[{"modificationType":"replace","path":"activation/administrativeStatus","value":"enabled"}]}}' \
  http://localhost:8087/midpoint/ws/rest/users/b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c48
docker exec straza-sam straza exec -- ls
```
{{< /command >}}

```text
deploy
```

The sessions the disable revoked stay revoked, and sam's next command opens a new one with the same key.

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl audit tail --type straza.audit.identity --user sam-sre-agent --limit 50
```
{{< /command >}}

```text
#33773 [sam-sre-agent] {"data":{"action":"user.killed","origin":"scim","reason":"deactivated via SCIM","sessionsRevoked":1,"user":"01a1547c-3f3d-7238-b13b-b5b6090ba343"},"id":"34fa53d7-29ab-4d98-9ce7-a1378b8484c5","source":"strazad/0ada1a6b-898a-4ecc-ba24-2df16e573ccd","specversion":"1.0","time":"2026-11-02T20:29:05.235850987Z","type":"straza.audit.identity"}
#33779 [sam-sre-agent] {"data":{"action":"user.reactivated","origin":"scim","user":"01a1547c-3f3d-7238-b13b-b5b6090ba343"},"id":"cf5a4df9-f315-4aa1-8e55-bbc9953c95a2","source":"strazad/0ada1a6b-898a-4ecc-ba24-2df16e573ccd","specversion":"1.0","time":"2026-11-02T20:29:38.389515053Z","type":"straza.audit.identity"}
```

`user.killed` counts the one session it revoked, and `user.reactivated` names the same origin.

When you are done, find the token's id with `strazactl api-token list` and revoke it with `strazactl api-token revoke` and that id.
{{< /cli >}}


## More on this stack {.nostep}


The stack is set up for more than this walk. Each item below runs on it as the seeder leaves it, unless the item names an overlay or a profile.


midPoint and Straza stay in step in both directions. midPoint sends people, agents, sponsors and role membership to strazad over SCIM. Three live sync tasks in midPoint read Straza's users, MCP servers and roles back every 10 seconds, after a first reconciliation of each, so a change made in Straza shows up in midPoint. When strazad restarts, midPoint suspends those tasks, and they stay suspended until someone resumes them in midPoint.


{{< details summary="What the seeder configures in midPoint and Keycloak" >}}
- midPoint has a resource for Straza on the universal REST connector and a resource for Keycloak. The Keycloak resource creates an account for each person. Agents get no Keycloak account, because they sign in with keys.
- Archetypes for employees, externals, AI agents and personal agents set the type of each identity in Straza, and an agent's sponsor reference in midPoint becomes its sponsor in Straza.
- Access packages are business roles in midPoint that grant Straza roles. alice holds the admin package, sam the operator package and joe the developer package. ivan holds the approver package, which gives him the approver role sec-approvers, and dave holds the global MCP admin package.
- nina is an AI agent with no access. Assign it the package `BR:Straza-analyst-access` in midPoint to watch an agent gain access through the IGA.
- Finance Team is a role a person can request in midPoint. The request goes to the manager of the person it is for, and without a manager it is rejected. jana is a new employee with no roles, the person to request it for.
- In Straza, the business role developer bundles the application roles demo-tools-sandbox, views-demo-tools, objects-demo-tools and midpoint-self-service. operator bundles demo-tools-readers and midpoint-operations, and analyst bundles demo-tools-readers and midpoint-self-service. The approval gates of an application role sit in a policy set named after it with the suffix `-access`, such as `midpoint-operations-access`, and `agent-guardrails` holds the rules for local commands.
- The Keycloak realm is `straza`. midPoint's API token in Straza carries the six scopes of the IGA connector job: `scim:read`, `scim:write`, `identity:read`, `apps:read`, `changes:read` and `config:read`.
{{< /details >}}

- objects-demo serves ten repositories and marks its tools by repository, so a role can reach some of them and not the rest. [Limit a role to some objects]({{< relref "guides/serve-mcp-apps/limit-a-role-to-some-objects.md" >}}) sets that up.
- views-demo has a tool with a view, which a client that supports MCP Apps can show. [Show MCP Apps views]({{< relref "guides/serve-mcp-apps/show-views.md" >}}) covers it.
- The midpoint server is an MCP server in front of midPoint. It acts as the calling person, allows reads, and holds writes for an approval.
- postgres puts adaptive-pg-mcp in front of four demo databases on midPoint's Postgres server. It starts when the stack directory's `.env` sets `COMPOSE_PROFILES=pg`.
- With the siem overlay, strazad posts every audit and revocation event to Elasticsearch, and Kibana on port 5601 searches them. [Sinks and SIEM]({{< relref "guides/audit/sinks-and-siem.md" >}}) explains the sink.
- With the demo overlay, joe works as Claude Code in the `straza-harness` container and behind the chat face on port 8477. The overlay builds from the straza-agents-demo repository, which is not public.
- A phone that alice pairs with the approver on port 8443 decides held calls too, as [Approve on a phone]({{< relref "guides/approve/phone.md" >}}) shows.

The landing page has more walkthroughs. [Demo stack defaults and the hardened shape]({{< relref "guides/operate/demo-stack-defaults.md" >}}) lists each account, the settings that are lab shortcuts, how to reach the stack from another machine over SSH, and how to tear it down.

{{< details summary="How this page was walked" >}}

Every step after the first boot ran on 2026-11-02 against a demo stack that was already up, built at v1.0.0-1695-gdec5ce12. It runs the four compose files of the hardened shape, so its ports listen on 127.0.0.1 only and `up.sh` did not start it. The seeder's lines come from its last run.

The console steps ran in headless Chromium as alice, who signed in through Keycloak each time. `strazactl` was built at v1.0.0-1700-g89d0cf1d and ran on the token minted in the console. Afterwards the ticket of the third deploy was denied in the console, and the token was revoked.
{{< /details >}}

{{< next "evaluate/security-model.md" >}}
