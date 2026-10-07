---
title: Kubernetes with Helm
description: Install strazad on your cluster with the Helm chart, set the values that decide its shape, and know the way to upgrade and roll back.
pagetype: how-to
weight: 30
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: Helm 3.16.4 rendering and linting chart 1.1.0 from the repository on a Linux host with no cluster, for the render and its object list, the trimmed Deployment, the values tables, the chart's release notes and the render refusals. The Secrets, the install, the port-forward, the upgrade and the rollback were not run for want of a cluster, the /readyz body is the one a standalone strazad at the same build returned, and the rollback's pod was built offline from the rendered Deployment with the page's jq program
  date: 2026-10-06
applies_to: enterprise
who: You, as the operator of the cluster
where: A terminal with helm and kubectl against your cluster
steps: true
keywords: kubernetes helm chart values pvc emptydir
---


You install strazad on Kubernetes with the Helm chart at `deploy/helm/straza`, as the operator of the cluster. You need `helm` and `kubectl` against a cluster, `openssl` and `jq`, and a clone of the repository, because every command reads the chart from `deploy/helm/straza`. At the end two strazad pods answer ready behind one Service, with every secret mounted from a Secret you created.

strazad pods are stateless on the request path, so the chart runs two of them by default. Under the enterprise profile it bundles a single-node Postgres and a NATS server with JetStream as StatefulSets, unless you point it at your own. The chart refuses to render the shapes that boot green and misbehave silently, such as several replicas that each run their own event bus.

## Create the secrets and render


Four values make the smallest real install:

- the URL your clients dial
- your identity provider's issuer
- a Secret holding a 32-byte key-encryption key at the data key `kek`
- a `kubernetes.io/tls` Secret for the listener

The key-encryption key seals stored credentials, and every replica needs the same one, so the chart mounts it at `/etc/straza/secret.key`. Without it each pod mints an ephemeral key in its own empty data directory, and secrets stop surviving a restart. Create the namespace and three Secrets first, the third holding the password of the bundled database at the data key `password`.

{{< command terminal="Terminal" purpose="kubectl against the cluster" >}}
```sh
kubectl create namespace straza
head -c 32 /dev/urandom > kek.bin
kubectl -n straza create secret generic straza-kek --from-file=kek=kek.bin
kubectl -n straza create secret tls straza-tls --cert=tls.crt --key=tls.key
kubectl -n straza create secret generic straza-db-password --from-literal=password="$(openssl rand -hex 24)"
```
{{< /command >}}

`tls.crt` and `tls.key` are your certificate and its key for the name in `publicUrl`. strazad refuses to start on a key-encryption key of any length other than 32 bytes.

{{< now title="Keep a copy of kek.bin, then delete it here" >}}Keep a copy where you keep backups, then delete it from the working directory. No stored credential can be opened without it.{{< /now >}}

The password is hex because the chart writes it into the database address, where some other characters would need escaping. The chart can also generate the password, but a Secret you created survives `helm uninstall`, which deletes a generated one.

Render first and read what you are about to apply. The command names the password Secret you created, so the render is the same on every run.

{{< command terminal="Terminal" purpose="helm, render only" >}}
```sh
helm template straza deploy/helm/straza --namespace straza \
  --set publicUrl=https://straza.example.com \
  --set oidc.issuer=https://idp.example.com/realms/prod \
  --set secrets.kekExistingSecret=straza-kek \
  --set tls.existingSecret=straza-tls \
  --set postgres.passwordSecret=straza-db-password | grep '^kind:'
```
{{< /command >}}

```text
kind: NetworkPolicy
kind: NetworkPolicy
kind: Service
kind: Service
kind: Service
kind: Deployment
kind: StatefulSet
kind: StatefulSet
```


{{< see >}}Eight objects: a NetworkPolicy for each bundled backend that narrows its ingress to the strazad pods, a Service each for NATS, Postgres and strazad, the Deployment, and the two StatefulSets.{{< /see >}}

To apply the same values, replace `template` with `install` and drop the `grep`. Bring-your-own always wins. Setting `postgres.dsn` or `postgres.existingSecret` suppresses the bundled database, `nats.url` suppresses the bundled broker, and their NetworkPolicies vanish with them.

## Read the pod the chart runs


The Deployment is where the runtime footprint is decided. Trimmed to the lines that matter, the render reads:

```text
      terminationGracePeriodSeconds: 30
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
      containers:
        - name: strazad
          image: ghcr.io/strazahq/straza:1.1.0
          args: ["serve", "--profile", "enterprise", "--listen", "0.0.0.0:8420"]
          env:
            - name: STRAZA_PUBLIC_URL
              value: "https://straza.example.com"
            - name: STRAZA_STORE_DSN
              value: postgres://straza:$(STRAZA_PG_PASSWORD)@straza-straza-postgres:5432/straza?sslmode=disable
            - name: STRAZA_EVENTS_URL
              value: "nats://straza-straza-nats:4222"
            - name: STRAZA_SECRETS_KEK_FILE
              value: /etc/straza/secret.key
            - name: STRAZA_TLS_CERT_FILE
              value: /etc/straza/tls/tls.crt
            - name: STRAZA_APPROVAL_PUSH_RELAY_ENABLED
              value: "true"
            - name: STRAZA_APPROVAL_PUSH_RELAY_TOKEN_FILE
              value: /var/lib/straza/push-relay-token
          volumeMounts:
            - name: data
              mountPath: /var/lib/straza
          readinessProbe:
            httpGet: { path: /readyz, port: http, scheme: HTTPS }
          securityContext:
            readOnlyRootFilesystem: true
      volumes:
        - name: data
          emptyDir: {}
```


The pod runs as user 65532 with a read-only root filesystem and every capability dropped. Its data directory `/var/lib/straza` is an emptyDir on purpose. Under the enterprise profile the store is Postgres and the event stream is NATS, so the directory holds only three things: the apps directory, the push relay token strazad mints at each start and, when you skip the key Secret, an ephemeral key-encryption key.

- The persistent volume claims belong to the bundled backends, 5Gi for NATS and 20Gi for Postgres by default.
- Until you pin `image.tag`, the image tag is the chart's `appVersion`, the release the chart ships with.
- The database password never lands in the manifest. The DSN references it through a variable the kubelet expands from the Secret.
- The pod gets 30 seconds to stop, which strazad needs to finish requests and write its last audit records.

During a database outage in the enterprise profile, each server decision can wait up to 25 seconds for room in the audit queue, and each waiting decision holds memory. The default memory limit of 512 MiB covers about 250 decisions per second, so raise `resources.limits.memory` if your peak rate is higher.

## Set the values that change behavior


| Value | What it does | Default |
|---|---|---|
| `publicUrl` | The issuer inside every session token and the base of every link strazad hands out. Set it to the name clients dial as soon as anything outside the cluster connects. Left empty, the chart derives the ingress host or the in-cluster Service name, and never the loopback default. | empty |
| `configYaml` | The settings that have no environment variable, such as sinks and delegated admin areas, rendered into a ConfigMap and wired through `STRAZA_CONFIG`. A change to it rolls the pods through a checksum annotation. | empty |
| `webpush.existingSecret` | Mounts the WebPush signing key from a Secret, because a key minted into the emptyDir would change on every restart and strand every browser subscription. | empty, so WebPush is off |
| `pushRelay.enabled` | Sends each approval request to the approver app on iOS and Android through the Straza relay, with no Apple or Firebase account on your side. The relay token lives in the emptyDir, so every pod start registers again, and the relay allows five registrations per hour from one address. [Push delivery and connectivity]({{< relref "guides/approve/push-and-connectivity.md#the-hosted-relay" >}}) says what the relay sees. | `true` |
| `apns.existingSecret` | Mounts your own APNs key for iOS. A render that sets it while `pushRelay.enabled` is true is refused, because strazad refuses two senders for one platform at boot. | empty |
| `ingress.enabled` | Renders one rule for the whole surface at one host. That is right only for a host that is already restricted, because the unauthenticated `/metrics` endpoint answers there too. | `false` |

### Values that set the shape of the install


| Value | What it does |
|---|---|
| `replicaCount` | How many strazad pods run. The standalone profile allows one. The default is `2`. |
| `postgres.enabled` | Runs the bundled single-node Postgres under the enterprise profile. Setting `postgres.dsn` or `postgres.existingSecret` replaces it, whatever this says. The default is `true`. |
| `postgres.storage` | The size of the bundled database's volume claim. The default is `20Gi`. |
| `nats.enabled` | Runs the bundled NATS server with JetStream under the enterprise profile, and setting `nats.url` replaces it. With neither, each pod runs its own bus, so the chart refuses more than one replica. The default is `true`. |
| `nats.storage` | The size of the bundled broker's volume claim. The default is `5Gi`. |
| `networkPolicy.enabled` | Renders a NetworkPolicy for each bundled backend that admits the strazad pods alone. Your network plugin must enforce NetworkPolicy, and a cluster that runs flannel alone accepts the objects and ignores them. The default is `true`. |
| `approverTLS.enabled` | Runs the dedicated approver listener on `approverTLS.port`, 8443, with its own Service port. The render fails unless `approverTLS.existingSecret` and `approverTLS.publicUrl` are set too. The default is `false`. |
| `pushRelay.url` | The relay strazad registers with. Empty means the Straza relay at `https://push.straza.ai`, so set it only for a relay you run. Empty by default. |
| `service.type` | The type of the strazad Service. The default is `ClusterIP`. |
| `autoscaling.enabled` | Renders a HorizontalPodAutoscaler between `autoscaling.minReplicas` and `autoscaling.maxReplicas`, which scales on CPU at `autoscaling.targetCPUUtilizationPercentage`. It is off by default, with 2, 10 and 70 as the other three values. |
| `ingress.className` | The ingress class of the rule that `ingress.enabled` renders. Empty by default. |
| `ingress.host` | The host of that rule. While `ingress.enabled` is on and `publicUrl` is empty, the chart builds `publicUrl` from it, with https when `ingress.tls` is on. The default is `straza.example.com`. |
| `ingress.tls` | Adds TLS to the rule with the certificate in the Secret `<release>-straza-tls`, which you or cert-manager create. The default is `false`. |
| `demoTools.enabled` | Runs the demo MCP server of the demo stack as its own pod that only strazad reaches, so a fresh install has a server to register. Leave it off in a real deployment. The default is `false`. |
| `demoTools.package` | The npm package and version the demo pod fetches at every start, so the node needs to reach npm. The default is `@modelcontextprotocol/server-everything@2026.7.4`. |


Two of the chart's render guards cover replicas. Scaling the standalone profile past one replica fails, because each pod would own its own SQLite store. An enterprise render with more than one replica and no shared NATS fails too.

The dedicated approver listener is off by default on Kubernetes. Turning it on requires your own certificate Secret and the public URL the phone dials, because the chart never mints the pair, as [TLS and exposure]({{< relref "guides/operate/tls-and-exposure.md" >}}) explains. On a cluster with an ingress that serves `/v1/approver/*` under a publicly trusted certificate, set `server.approverPublicUrl` in `configYaml` instead.

Running a guard yourself shows the wording:

{{< command terminal="Terminal" purpose="helm, render only" >}}
```sh
helm template straza deploy/helm/straza --set profile=standalone --set replicaCount=2
```
{{< /command >}}

```text
Error: execution error at (straza/templates/deployment.yaml:4:4): straza: profile=standalone cannot scale past one replica: each pod would run its own sqlite store and embedded events. Set replicaCount=1 (and autoscaling ceilings), or use profile=enterprise.
```

## Verify


After `helm install`, the release notes print a health check against `publicUrl`. Before DNS points at the cluster, forward the Service port in one terminal and ask `/readyz` from a second one. It reports the store and the event bus. The readiness and liveness probes hit the same two endpoints on the container port, and switch to HTTPS when `tls.existingSecret` is set.

{{< command terminal="Terminal 1" purpose="port-forward, keep it running" >}}
```sh
kubectl -n straza port-forward svc/straza-straza 8420:8420
```
{{< /command >}}

{{< command terminal="Terminal 2" purpose="check" >}}
```sh
curl -sk https://localhost:8420/readyz
```
{{< /command >}}

{{< see >}}`{"status":"ok","components":{"bus":"ok","store":"ok"}}`, the same body the standalone server returns.{{< /see >}}


Two replicas, the default, can start together on a new database. The store keeps one active signing key for sessions and one for policy snapshots between them.

## Upgrade and roll back {.nostep}


`helm upgrade` with a newer chart or `image.tag` rolls the Deployment, and each new pod migrates the store at start, as [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md" >}}) describes. The chart keeps the default rolling update, so for a moment an old pod and a new pod serve side by side.


A rollback moves the schema down with the newer image before the older one starts, because an older strazad refuses a database that a newer one migrated. Set the shell variable `TARGET` to the newest migration of the release you return to. This release has one migration, 37, so it has no step to move down, and the way back from it is a restore of the database.

For a later release, take three steps:

1. Scale the Deployment to zero.
2. Run `strazad migrate --to "$TARGET"` once in a pod built from the release's own pod spec, so that it reads the same database settings and passes the same NetworkPolicy.
3. Roll the release back.

{{< command terminal="Terminal" purpose="kubectl and helm against the cluster" >}}
```sh
kubectl -n straza scale deployment/straza-straza --replicas=0
kubectl -n straza wait --for=delete pod -l app.kubernetes.io/name=straza,app.kubernetes.io/instance=straza --timeout=2m
kubectl -n straza get deployment straza-straza -o json \
  | jq --arg to "$TARGET" '{apiVersion: "v1", kind: "Pod",
         metadata: {name: "straza-migrate", labels: .spec.template.metadata.labels},
         spec: (.spec.template.spec | .restartPolicy = "Never"
           | .containers[0].args = ["migrate", "--to", $to, "--profile", "enterprise"]
           | del(.containers[0].readinessProbe, .containers[0].livenessProbe))}' \
  | kubectl -n straza apply -f -
kubectl -n straza wait --for=jsonpath='{.status.phase}'=Succeeded pod/straza-migrate --timeout=2m
kubectl -n straza logs pod/straza-migrate
kubectl -n straza delete pod straza-migrate
helm -n straza rollback straza
```
{{< /command >}}

{{< see >}}The migrate pod's log names the migration the database is at now and the release to start.{{< /see >}}

The scale returns before the old pods exit, so the first `wait` holds the migrate pod back until no strazad pod serves. Its label selector matches the strazad pods only, since the bundled backends carry the names `straza-nats` and `straza-postgres`. The pod carries the template's labels, which the NetworkPolicy admits, and no ReplicaSet adopts it, because it lacks the hash label a ReplicaSet selects on.

The command moves one migration per run. When the newer release added more than one, the first run refuses before it changes anything, and its log names the step to run first, so run the pod once for each step. When the command refuses, the pod fails and `kubectl wait` runs out of time. The log then names what to fix before you delete the pod and run it again.

`helm rollback` with no revision returns to the previous one, which brings back the older image and the replica count. With `autoscaling.enabled` the Deployment carries no replica count, and the autoscaler does not scale a Deployment at zero. Scale it back to `autoscaling.minReplicas` yourself, with `kubectl -n straza scale deployment/straza-straza --replicas=2` for the default of two. [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md" >}}) compares this way back with a restore.

## Undo {.nostep}


`helm -n straza uninstall straza` removes the Deployment, the Services and the StatefulSets and keeps their persistent volume claims. The namespace and the three Secrets you created stay as well, so a reinstall over the old data finds the same database password and the same key-encryption key. `kubectl delete namespace straza` removes all of it, the claims included. The shape these pods run is [The enterprise shape]({{< relref "guides/operate/enterprise-shape.md" >}}).
