---
title: Deployment best practices
description: Run each agent in its own pod and let only Straza reach your MCP servers, so no agent can call a server past the gateway.
summary: >-
  The gateway protects an MCP server only while it is the one road to that server. On
  Kubernetes you close the other roads with two network policies and one agent per pod.
  This page gives the manifests, the one registration a server that requires `managed`
  asks for, and a check you run from inside the agent.
pagetype: how-to
weight: 35
draft: false
tested:
  version: v1.0.0-2059-g261dc8eb
  platform: kind 0.33.0 with Kubernetes 1.37.0 and its network plugin kindnet on linux/arm64, the chart in the standalone profile with the minimum attestation set to managed, the client v1.0.0-2061-gc2d27dcb with this page's entrypoint and state directory change in the sandbox image, and volumes that kind makes writable for everyone, so storage that hands a new volume to root was not tested
  except: the enterprise profile, a network plugin other than kindnet, an egress proxy, a server outside the cluster, and the image name, which is your own
  date: 2026-11-23
applies_to: both
who: Operator of the cluster, and an administrator for the registration
where: Terminal with kubectl against your cluster, and strazactl on an admin API token
steps: true
keywords: kubernetes pod network policy egress allowlist agent sandbox best practices managed attestation
---


Straza's gateway decides every call that reaches it and keeps each MCP server's credential. That protects a server only while the gateway is the one road to it. On Kubernetes you close the other roads with three rules, and at the end you watch a direct call from an agent time out.

| Rule | What it stops |
|---|---|
| Only strazad reaches an MCP server | Any pod in the cluster that talks to the server past the gateway |
| An agent pod reaches only strazad | A server that someone adds to the agent's own configuration, and data sent to any other address |
| One agent per pod, with its key in a second container | An agent that reads its own key or another agent's files |

An agent on a laptop reaches whatever the laptop's network reaches, so nothing there enforces these rules. In a pod the cluster decides each connection.

## Before you start {.nostep}

- strazad on the cluster, as [Kubernetes with Helm]({{< relref "guides/operate/kubernetes-with-helm.md" >}}) installs it.
- Each MCP server in a pod of its own, added to Straza as a `remote` server.
- The agent image of [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}), built from `deploy/sandbox/Dockerfile`.
- `strazactl` on an admin API token, for the registration step.

The names below come from the cluster this page was tested on. The chart's release is `straza` in the namespace `straza`. One MCP server, `demo-tools`, listens on port 3001 in the namespace `mcp`. The agent runs in the namespace `agents`.

## Check that the cluster enforces network policies


A cluster stores a network policy even when its network plugin ignores it, and flannel alone is such a plugin. Every rule on this page then does nothing and reports no error. Prove enforcement once: start a web server, call it, forbid the call, and call it again.

{{< command terminal="Terminal" purpose="your cluster" >}}
```sh
kubectl create namespace np-check
kubectl run web -n np-check --image=nginx:alpine --port=80 --expose
kubectl wait -n np-check --for=condition=Ready pod/web

probe() {
  kubectl run probe --image=curlimages/curl --rm -i \
    --restart=Never --command -- sh -c 'sleep 1
      curl -s -o /dev/null -w "%{http_code}\n" -m 5 \
        http://web.np-check.svc'
}
probe

kubectl apply -f - <<'EOF'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-all
  namespace: np-check
spec:
  podSelector: {}
  policyTypes: [Ingress]
EOF
probe
kubectl delete namespace np-check
```
{{< /command >}}

{{< see >}}`200` from the first probe and `000` from the second, five seconds later. kubectl also reports that the second probe pod ended with an error, which is the timeout.{{< /see >}}

If the second probe also prints `200`, the plugin does not enforce policies. Stop here and change the plugin, because the rest of this page depends on it.

## Let only Straza reach each MCP server


Give each MCP server a namespace other than strazad's, and admit only the strazad pods to its port.

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: demo-tools-ingress-from-strazad
  namespace: mcp
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: mcp-demo-tools
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: straza
          podSelector:
            matchLabels:
              app.kubernetes.io/name: straza
              app.kubernetes.io/instance: straza
      ports:
        - protocol: TCP
          port: 3001
```

The two selectors sit in one `from` entry, so a caller must match both: it runs in the namespace `straza` and carries the labels of the strazad pods. Written as two entries, the rule would admit every pod of that namespace.

The separate namespace matters. In the test, a pod started in strazad's own namespace with strazad's two labels passed a rule that selected by label alone. With the server in `mcp`, the same pod got through only from inside the namespace `straza`. Whoever may create pods there can reach your servers, so keep that right narrow.

For a server outside the cluster, the same rule is a firewall entry that admits strazad's outbound address. The server's credential stays in Straza either way.

## Run each agent in its own pod


The pod has two containers that share one volume and nothing else.

- `straza service` runs under its own user ID and is the only container that mounts the agent's key.
- The agent's container mounts the service's socket, read-only. It has no other mount, no token and no key.
- The key lives on a persistent volume. A restarted pod then has its session back with no step from anyone. On an `emptyDir` every restart needs a new key, which an administrator has to register again.
- One identity has one key, so the Deployment keeps one replica.
- No container runs as root. A namespace that enforces the Pod Security level `restricted` admitted the pod in the test.

Give the agent's container no writable mount. An `emptyDir` cannot be mounted `noexec`, and in the test the agent wrote a script to such a mount and ran it.


The service reads its server and the server's snapshot keys from a managed configuration, which in a pod is a ConfigMap. Read the keys from your server over a connection you trust, because the agent accepts any policy that one of them signs.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
curl -s https://straza.example.com/.well-known/straza/snapshot-keys.json
```
{{< /command >}}

The answer lists each key with its `kid`. The ConfigMap takes one line for each, the `kid` and then the key.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: straza-managed-config
  namespace: agents
data:
  config.yaml: |
    serverUrl: http://straza-straza.straza.svc:8420
    snapshotKeys:
      <kid>: <key>
```

After you rotate the server's snapshot key, put the new key here and restart the pod, because the service verifies policy with these keys alone.

{{< details summary="The Deployment this page was tested with" >}}
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: straza-agent-state
  namespace: agents
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 64Mi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: straza-agent
  namespace: agents
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: straza-agent
  template:
    metadata:
      labels:
        app: straza-agent
    spec:
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        seccompProfile: { type: RuntimeDefault }
        # Lets the service's group write to the state volume on storage
        # that hands a new volume to root.
        fsGroup: 64000
      initContainers:
        # The service. restartPolicy Always makes it a sidecar that starts
        # before the agent and stops after it.
        - name: straza-service
          image: registry.example/straza-agent:1
          restartPolicy: Always
          command: ["/opt/straza/bin/straza", "service"]
          startupProbe:
            exec:
              command: ["python3", "-c", "import socket; socket.socket(socket.AF_UNIX).connect('/run/straza-service/service.sock')"]
            periodSeconds: 1
            failureThreshold: 60
          securityContext:
            runAsUser: 64000
            runAsGroup: 64000
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
          volumeMounts:
            - { name: sock, mountPath: /run/straza-service }
            # One level above the state directory, so the service creates
            # /var/lib/straza-service itself and owns it.
            - { name: state, mountPath: /var/lib }
            - { name: managed, mountPath: /etc/straza, readOnly: true }
            - { name: service-tmp, mountPath: /tmp }
      containers:
        - name: agent
          image: registry.example/straza-agent:1
          # The image's entrypoint runs first and starts these arguments once
          # the agent is enrolled. Put your agent's own command here. The
          # test ran a process that prints one line and then waits.
          args: ["python3", "-c", "import time\nprint('AGENT STARTED', flush=True)\nwhile True: time.sleep(3600)"]
          env:
            - { name: STRAZA_SANDBOX_WAIT_FOR_ENROLLMENT, value: "1" }
          securityContext:
            runAsUser: 65532
            runAsGroup: 65532
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
          volumeMounts:
            - { name: sock, mountPath: /run/straza-service, readOnly: true }
      volumes:
        - { name: sock, emptyDir: {} }
        - name: state
          persistentVolumeClaim:
            claimName: straza-agent-state
        - name: managed
          configMap:
            name: straza-managed-config
        - { name: service-tmp, emptyDir: { medium: Memory } }
```
{{< /details >}}

Create the agent's identity and enroll it as [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) describes. Run its two `straza` commands in the agent's container, for example `kubectl exec -n agents deploy/straza-agent -c agent -- straza keygen --user pod-agent`. The agent's command starts within about two seconds of the enrollment, with no restart.

{{< details summary="Four things that differ from a plain machine" >}}
The image's entrypoint stops a container that is not enrolled, and a pod has to run before you can enroll it. `STRAZA_SANDBOX_WAIT_FOR_ENROLLMENT=1` makes the entrypoint wait instead. Your agent's command has not started while it waits, and the container's log says how to enroll.

`straza keygen` prints an enroll command with `--server`. In this pod, leave the flag out. The service reads the server from the managed configuration in its own container.

Mount the state volume at `/var/lib`, one level above the service's directory. The service closes its directory to every other account, which only the directory's owner may do, and the mount point of a volume belongs to root. Mounted at `/var/lib/straza-service` itself, the volume stops the service with a sentence that says so.

A process that ignores the stop signal keeps a deleted pod alive for its grace period, 30 seconds by default. The next pod's service cannot take the state volume until the old pod is gone, so it restarts a few times and then runs. An agent that ends on the signal avoids the wait.
{{< /details >}}


{{< details summary="If the pod should fetch the snapshot keys itself" >}}
An init container can run the managed install in place of the ConfigMap. It reads the keys from the server at every pod start, so a restart of the pod picks up a rotated key. The install has to run as root, which a namespace at the level `restricted` refuses. It runs for `claude-code`, because the install has no form for a hookless agent yet, and the files it writes for Claude Code go unused.

Put this container in front of the service, make the volume `managed` an `emptyDir` and add an `emptyDir` named `install-scratch`.

```yaml
        - name: managed-install
          image: registry.example/straza-agent:1
          command:
            - /opt/straza/bin/straza
            - install
            - --managed
            - --server
            - http://straza-straza.straza.svc:8420
            - claude-code
          env:
            - { name: PATH, value: /usr/bin:/bin }
            - { name: STRAZA_MANAGED_BIN_DIR, value: /opt/straza/bin }
          securityContext:
            runAsUser: 0
            runAsGroup: 0
            runAsNonRoot: false
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
          volumeMounts:
            - { name: managed, mountPath: /etc/straza }
            - { name: install-scratch, mountPath: /etc/claude-code }
```
{{< /details >}}

## Let the agent pod reach only Straza


This rule allows the pod two destinations, strazad's port and the cluster's DNS, and lets nothing connect to the pod.

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: straza-agent-egress
  namespace: agents
spec:
  podSelector:
    matchLabels:
      app: straza-agent
  policyTypes: [Ingress, Egress]
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: straza
          podSelector:
            matchLabels:
              app.kubernetes.io/name: straza
              app.kubernetes.io/instance: straza
      ports:
        - protocol: TCP
          port: 8420
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
```

Your agent also needs its model's API, and a network policy cannot name a host. It allows pods, namespaces and address ranges. An address range for an API behind a content delivery network admits every site at that address. Send the agent's model traffic through an egress proxy that allows host names, or use a network plugin with host name rules, and allow that one destination here.

## Register the agent image where the server requires managed


By default the enterprise profile gives no session to a client below the attestation level `managed`. A container agent reads `advisory` there until two things are true.

The first is a managed configuration in the service's container, which the ConfigMap above provides.

The second is one row in the server's registry that names the client binary of your image. Read the hash from the image and register it for the harness `exec-wrapper`, with the platform of your nodes.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
docker run --rm -u 0 --entrypoint /usr/bin/sha256sum \
  registry.example/straza-agent:1 /opt/straza/bin/straza

strazactl attestation add --artifact self --harness exec-wrapper \
  --platform linux/arm64 --hash sha256:5dd2d0ff0e6b3ca0ee5c96e12576b12fb866a36dc6c3e0b7ca35dccf971b6607
```
{{< /command >}}

The row belongs to that one binary. Add a row for each new client release before you roll the image out, or its sessions are refused.

{{< fails >}}
`attestation level "advisory" is below the required level "managed"`
: The registry holds no row for this binary and harness. The sentence goes on to name `straza install --managed`, which is not the step this pod is missing. Add the row above.
{{< /fails >}}

## Check it from inside the agent


Run a governed command in the agent's container, then list the sessions.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
kubectl exec -n agents deploy/straza-agent -c agent -- \
  straza exec -- python3 -c 'print("child ran")'
strazactl sessions list
```
{{< /command >}}

```text
child ran
ID                                    USER       HARNESS       CLIENT                 ATTESTATION  WIRING      STATUS
01a2075c-c25d-7427-8afb-1cf753c1a59b  pod-agent  exec-wrapper  v1.0.0-2060-g572be18d  managed      unmeasured  active
```

Now call the MCP server directly from the same container, past the gateway.

{{< command terminal="Terminal" purpose="your cluster" >}}
```sh
kubectl exec -n agents deploy/straza-agent -c agent -- python3 -c \
  'import urllib.request; urllib.request.urlopen("http://demo-tools.mcp.svc:3001/mcp", timeout=5)'
```
{{< /command >}}

{{< see >}}`urllib.error.URLError: <urlopen error timed out>` after five seconds. The same call to any address outside the cluster ends the same way.{{< /see >}}

The agent's road to that server is the gateway. Its MCP client starts `straza mcp --harness exec-wrapper`, and a tool the agent's role covers answers there. In the test the call to the tool `echo` returned its text, and `strazactl audit tail --type straza.audit.mcp --user pod-agent` showed the record with the role that allowed it.

## What these rules leave open {.nostep}

| Still open | What to do about it |
|---|---|
| Anyone who may create pods in strazad's namespace passes the server rule. | Keep that namespace to strazad and its operators. |
| The agent's own code runs inside the pod without a decision. | Put every tool whose arguments matter behind the gateway. |
| A session does not prove which image it ran in. | The registry row pins the client binary. |

## Next {.nostep}

- [Hardening]({{< relref "security/hardening.md" >}}) is the checklist for the server and for agents on people's machines.
- [Ports and network]({{< relref "reference/ports-and-network.md" >}}) lists every port a firewall rule needs.
- [Credentials]({{< relref "guides/serve-mcp-apps/credentials.md" >}}) stores a server's secret in Straza, so a call past the gateway has none.
