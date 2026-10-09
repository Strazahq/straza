---
title: Run it on Kubernetes
description: Run strazad on a throwaway kind cluster through the Helm chart, sign in through a port-forward, and read one deny with its reason.
pagetype: tutorial
weight: 35
draft: false
who: You, as the admin
where: Two terminals and a browser, on a machine with Docker
steps: true
modes: [console, cli]
mode_default: cli
tested:
  version: v1.1.0-117-g106081a8
  platform: A Linux host with Helm 3.16.4 and no Kubernetes cluster, so the cluster, the image build and load, the install, the pod checks, the port-forward and the teardown were not run, and their pasted outputs come from the kind walk of 2026-09-19 at v1.0.0-1235-ge8f5cdea. The install's values and the chart's notes were rendered with helm template from chart 1.1.0, and the version and readiness checks, the login with the form post the sign-in page sends and the simulated deny ran against a standalone strazad in an Alpine 3.20 container. The console steps were not clicked
  date: 2026-10-06
applies_to: standalone
keywords: kubernetes kind helm chart tutorial port-forward
---


You run strazad on a throwaway Kubernetes cluster on your own machine through the Helm chart, and work as its admin from two terminals and a browser. You sign in through a port-forward and ask the server what it decides for `rm -rf`. At the end you have read one deny with its reason, and you delete the cluster.

The chart runs in its standalone profile here: one pod with an embedded database, an embedded event broker and the built-in sign-in page. No external service stands between you and the first decision.

## Before you start {.nostep}


- Docker Engine, kind, kubectl and Helm.
- A clone of the repository, because the commands read the chart from `deploy/helm/straza`.
- `strazactl` on your PATH, from [Install]({{< relref "get-started/install.md" >}}).

The chart pulls the release image `ghcr.io/strazahq/straza` by default, and building your own image from the repository is an optional section below. One terminal is enough for everything except the port-forward, which holds a second one.

## Check the tools


{{< only form="cli" >}}The cluster and the chart are terminal work. The console opens once the server runs.{{< /only >}}

kind runs a whole Kubernetes node as one Docker container, so Docker is the only service that has to be running before you start. Confirm that the four tools answer.

{{< command terminal="Terminal 1" purpose="your machine" >}}
```sh
docker version --format 'Docker Engine {{.Server.Version}}'
kind version
kubectl version --client
helm version --short
```
{{< /command >}}

```text
Docker Engine 29.4.0
kind v0.33.0 go1.26.7 linux/amd64
Client Version: v1.37.0
Kustomize Version: v5.8.1
v3.16.4+g7877b45
```

These are the versions this page was tested with. kind and kubectl are single static binaries from their release pages, and each page publishes a SHA-256 file to check the download against. The cluster needs about 1.5 GB of disk for the node image and the server image together, and the node and the pod together use about 600 MB of memory.

## Start the cluster


Create the cluster. kind pulls its node image the first time and starts one container that runs the control plane.

{{< command terminal="Terminal 1" purpose="your machine" >}}
```sh
kind create cluster --name straza
kubectl get nodes
```
{{< /command >}}

{{< see >}}The node `straza-control-plane` with the status `Ready`.{{< /see >}}

```text
Creating cluster "straza" ...
 ✓ Ensuring node image (kindest/node:v1.37.0) 🖼
 ✓ Preparing nodes 📦
 ✓ Writing configuration 📜
 ✓ Starting control-plane 🕹️
 ✓ Installing CNI 🔌
 ✓ Installing StorageClass 💾
Set kubectl context to "kind-straza"
NAME                   STATUS   ROLES           AGE   VERSION
straza-control-plane   Ready    control-plane   30s   v1.37.0
```

The in-progress lines and kind's closing hints are trimmed. `kind create cluster` also writes the context `kind-straza` into your kubeconfig and selects it, which is why the `kubectl` commands below name no cluster.

## Optional: build your own image {.nostep}


Skip this section if you use the release image. If you run Straza from source, build the server image from the repository root with the version git describes, so the pod reports the same version the binaries from Install would.

{{< command terminal="Terminal 1" purpose="the repository root" >}}
```sh
STRAZA_VERSION=$(git describe --tags --always)
docker build -f deploy/Dockerfile --build-arg VERSION=$STRAZA_VERSION --build-arg COMMIT=$(git rev-parse --short HEAD) -t straza:$STRAZA_VERSION .
```
{{< /command >}}

Docker prints the build steps, and the Go compile takes the longest. Without the two build arguments the pod would report the version `dev`. The finished image is about 120 MB.

The Dockerfile has two stages. A Go 1.26 image compiles `strazad` and `strazactl` with CGO off. The final image is built from `scratch` and holds the two static binaries, the CA bundle and the license and notice files under `/licenses/`, running as user 65532 with the data directory set to `/var/lib/straza`. The `.dockerignore` file at the repository root keeps local state such as `bin` and the tool caches out of the build context, so the context is the source tree alone.

Hand the image to the cluster, because a kind node cannot see your machine's image store.

{{< command terminal="Terminal 1" purpose="your machine" >}}
```sh
kind load docker-image straza:$STRAZA_VERSION --name straza
```
{{< /command >}}

{{< see >}}kind reports that the image is not yet present on the node `straza-control-plane`, and loads it.{{< /see >}}

The load copies the image into the node's own image store. That is what lets the chart run it with a pull policy of Never, because nothing on the node ever asks a registry for it. Add three values to the install command of the next section so the chart uses it: `--set image.repository=straza --set image.tag=$STRAZA_VERSION --set image.pullPolicy=Never`.

## Install the chart


The chart's defaults describe the enterprise shape: two replicas, a bundled Postgres, a bundled NATS and an external identity provider. Three values turn it into the one-pod standalone shape of this tutorial. The image is `ghcr.io/strazahq/straza` at the chart's appVersion, which is 1.1.0 for this release.

{{< command terminal="Terminal 1" purpose="the repository root" >}}
```sh
helm install straza deploy/helm/straza \
  --set profile=standalone --set replicaCount=1 \
  --set publicUrl=http://127.0.0.1:18420 \
  --wait --timeout 3m
```
{{< /command >}}

{{< see >}}The release status `deployed`, followed by the chart's notes. They name the release `straza-straza`, the image `ghcr.io/strazahq/straza:1.1.1` or your own tag, the profile `standalone`, one replica, the public address `http://127.0.0.1:18420`, embedded per-pod events and a per-pod SQLite store.{{< /see >}}

Each value has a reason:

| Value | Why |
|---|---|
| `profile=standalone` | Selects the embedded store and broker, and drops the bundled backends, which the chart renders only under the enterprise profile. |
| `replicaCount=1` | Has to go with the standalone profile. The chart refuses to render a standalone release with more than one replica, because each pod would own its own SQLite file. |
| `publicUrl` | The address your clients dial. It becomes the issuer inside every session token and the base of every link the server hands out, including the sign-in address the login command prints in a moment, so it has to be the port-forward address you open next. Left empty, the chart derives the in-cluster Service name `http://straza-straza.default.svc:8420`, which is right for clients inside the cluster and unreachable from your browser. |

`--wait` returns once the pod passes its readiness probe. Check the pod:

{{< command terminal="Terminal 1" purpose="your machine" >}}
```sh
kubectl get pods
```
{{< /command >}}

```text
NAME                             READY   STATUS    RESTARTS   AGE
straza-straza-78467cff98-tfqx6   1/1     Running   0          22s
```

{{< see >}}One pod, `1/1` ready, with the status `Running`.{{< /see >}}


{{< fails >}}
`ImagePullBackOff`
: The node could not pull the release image. `kubectl describe pod -l app.kubernetes.io/name=straza` ends with the registry's reply, and the tag and the node's network access are the two things to check.

`ErrImageNeverPull`
: The pod's events say the image "is not present with pull policy of Never", so the load of your own image did not reach this cluster. Run `kind load docker-image` again with the same `--name`, then delete the pod with `kubectl delete pod -l app.kubernetes.io/name=straza`, and the Deployment replaces it.
{{< /fails >}}


The first boot happened inside the pod, so its log is where the one-time passwords went. Read them now.

{{< command terminal="Terminal 1" purpose="your machine" >}}
```sh
kubectl logs deploy/straza-straza
```
{{< /command >}}

{{< see >}}One line with the password of `admin` and one with the password of `break-glass`, and a `strazad serving` line for the profile standalone at `http://127.0.0.1:18420`.{{< /see >}}

{{< now title="Copy the admin password from your own log" >}}Each password is printed once. You sign in with the admin one in a moment.{{< /now >}}

The pod's data directory `/var/lib/straza` is an emptyDir. It holds the SQLite file, the embedded broker's store and the approver certificate the standalone profile mints for itself. The store and the admin you are about to use therefore exist exactly as long as this pod does. That is the right shape for a throwaway cluster and the wrong one for anything you intend to keep, which is why the production shape on [Kubernetes with Helm]({{< relref "guides/operate/kubernetes-with-helm.md" >}}) puts the store in Postgres and the events in NATS.

## Reach the server through a port-forward


{{< only form="cli" >}}The port-forward runs in a terminal in both forms. The console needs it too.{{< /only >}}

The Service is a ClusterIP, so nothing outside the cluster reaches it until you forward a port. In a second terminal, forward local port 18420 to the Service and leave it running.

{{< command terminal="Terminal 2" purpose="port-forward, keep it running" >}}
```sh
kubectl port-forward svc/straza-straza 18420:8420
```
{{< /command >}}

```text
Forwarding from 127.0.0.1:18420 -> 8420
Forwarding from [::1]:18420 -> 8420
```

The local port is 18420 rather than the server's own 8420, so that a standalone server already listening on 8420 on your machine, from the first tutorial for example, is never mistaken for this one. The address has to match the `publicUrl` you set at install. Back in the first terminal, ask the server what it is and whether it is ready.

{{< command terminal="Terminal 1" purpose="your machine" >}}
```sh
curl -s http://127.0.0.1:18420/version
curl -s http://127.0.0.1:18420/readyz
```
{{< /command >}}


The first answer is one line of JSON. Its `version` is the one in the image, and its `profile` is `standalone`. Its `approver` block describes a second listener that the standalone profile mints for phone approval at the pod's own IP. The chart publishes no port for it, so nothing outside the cluster reaches it, and this tutorial does not use it. The second answer says the pod is ready:

```text
{"status":"ok","components":{"bus":"ok","store":"ok"}}
```

## Sign in


{{< console >}}
Open `http://127.0.0.1:18420/console/` in your browser and press **Sign in with a code**. The console shows a one-time code.

{{< shot name="signin-code" caption="Press Open the sign-in page. Your code is different." >}}

1. Press **Open the sign-in page**. A new tab opens on the page titled Sign in to Straza, with the code filled in.
2. Check that the code matches, enter `admin` and the password from the pod log, and press **Sign in**.
3. The tab says *Signed in. You can close this tab.* Go back to the console.

{{< see >}}The console opens on **Overview**. The foot of the sidebar says `standalone` and the server's build.{{< /see >}}
{{< /console >}}

{{< cli >}}
Log in. `login` is the one command that takes the server address, and it remembers it for every later command.

{{< command terminal="Terminal 1" purpose="administration" >}}
```sh
strazactl login --server http://127.0.0.1:18420
```
{{< /command >}}

```text
Open http://127.0.0.1:18420/oidc/device?user_code=5FN6-5GP8
and confirm code 5FN6-5GP8
Logged in as admin.
note: this login can change Straza's configuration. Keep it away from coding agents. Automation uses an admin API token in STRAZA_API_TOKEN, and an agent proposes config changes through the built-in straza MCP server.
```

Between the second and the third line the command waits for you.

1. Open the printed address in a browser. The page titled Sign in to Straza shows the code already filled in.
2. Enter `admin` and the password from the pod log, and press **Sign in**. The page answers "Signed in" and says you can close the tab.
3. Go back to the terminal.

{{< see >}}`Logged in as admin.` and a note that asks you to keep this login away from coding agents.{{< /see >}}
{{< /cli >}}

The address of the sign-in page is the `publicUrl` from the install, and that is the reason the value had to be the port-forward address.

{{< fails >}}
An address at `straza-straza.default.svc`
: The release was installed without `publicUrl`, and a browser on your machine cannot resolve that name. Run `helm uninstall straza` and run the install block again with the value. Then read the new admin password from the new pod's log, because the data directory went away with the old pod.

`login timed out (device code expired)`
: More than ten minutes passed before you approved the code. Run the command again and approve the code it prints this time.
{{< /fails >}}

## Read the deny


Now ask the server what it would decide for a command, before any agent is enrolled. Neither form runs the command or records anything.

{{< console >}}
{{< clicks "Policies" "standalone-starter" "Test a call" >}}

1. Under **Who**, pick `admin`. The starter policy applies to everyone, so the admin can stand in for the person who calls.
2. Pick **Shell command**, type `rm -rf /tmp/x` in **The command line**, and press **Test**.

{{< see >}}**Denied**, decided by rule `block-recursive-delete` in policy `standalone-starter`, with the rule's reason.{{< /see >}}
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal 1" purpose="administration" >}}
```sh
strazactl policy simulate --roles dev --tool shell.exec --command 'rm -rf /tmp/x'
```
{{< /command >}}

```text
This call would be denied.
Decided by rule block-recursive-delete in policy standalone-starter: Straza starter policy: recursive force-delete is denied. Delete files individually, or an admin can edit the standalone-starter PolicySet.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.

subject   roles dev · attestation none
snapshot  705a13b9b35902f8295ee4126287d2c84de8afb01674fd6592ac14544adfe539 (live)
wire      effect=deny · ruleId=block-recursive-delete · setName=standalone-starter · snapshot=705a13b9
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
```

{{< see >}}`This call would be denied.` and the rule `block-recursive-delete` in policy `standalone-starter`.{{< /see >}}

The rule applies to everyone, so the role `dev` needs no definition for the answer to come back. The same command with `git status` answers "This call would be allowed." and names the profile default as the reason, since no rule mentions it. The snapshot id names the exact policy state that decided. A governed client downloads that same compiled snapshot and decides against it locally, which is what the note at the end of the answer means.
{{< /cli >}}

The rule belongs to the starter PolicySet that a fresh standalone store seeds at its first boot, the one [Your first governed session]({{< relref "get-started/first-governed-session.md" >}}) showed in full.

## Delete the cluster


{{< only form="cli" >}}The cluster goes away from a terminal.{{< /only >}}

Stop the port-forward in the second terminal with Ctrl-C, then remove the release and the cluster.

{{< command terminal="Terminal 1" purpose="your machine" >}}
```sh
helm uninstall straza
kind delete cluster --name straza
```
{{< /command >}}

```text
release "straza" uninstalled
Deleting cluster "straza" ...
Deleted nodes: ["straza-control-plane"]
```

If you built your own image, `docker rmi straza:$STRAZA_VERSION` removes it from your machine as well.

The uninstall removes the Deployment and the Service and, with the pod, the emptyDir that held the store, so nothing of this server survives it. The cluster deletion stops and removes the node container, and drops the `kind-straza` context from your kubeconfig.

Three things stay in Docker afterwards:

| What stays | Size | How to remove it |
|---|---|---|
| The node image `kindest/node:v1.37.0`, which the next `kind create cluster` reuses | 1.34 GB | `docker rmi` with its image id, since kind pulls it by digest and leaves it untagged |
| The build cache of the optional image build | about 3.2 GB | `docker builder prune` |
| kind's Docker network named `kind` | | `docker network rm kind` |

## Next {.nostep}

- [Kubernetes with Helm]({{< relref "guides/operate/kubernetes-with-helm.md" >}}) is the production shape on a cluster you keep, with Postgres and NATS behind the pods and your identity provider in front.
- [What just happened]({{< relref "get-started/how-that-worked.md" >}}) names each piece that acted here.
