# App Manifest, v1beta1

Status: **beta** (it began as v1alpha1, revision 2 added
`limits.timeoutSeconds`, with no wire changes in the beta bump; revision 3,
2026-08-05, tightens the SCHEMA only: `credential.inject.as: header` now
requires `runtime.kind: remote` in the schema, mirroring the parser rule
that always enforced it platform-side. Found by the specoracle agreement
tests: the schema accepted a manifest the parser rejects, violating the
MUST-agree sentence below. No valid document changes shape; no client
roll needed.)
Schema: `app-manifest.schema.json`. Examples: `examples/valid-*.yaml`,
`examples/invalid-*.yaml`. The platform parser MUST agree with the schema on
every example. Registry-import fixtures live in `spec/conformance/registry/`
(real registry `server.json` captures + golden imported manifests).

Revision 6 (2026-10-01): the exposure block gains `views`, a boolean that
defaults to false, which lets a server show its MCP Apps views (§4). Additive:
every earlier valid example parses unchanged, and a platform of an earlier
revision refuses a manifest that carries the key, which is the fail-closed
answer. The schema now also requires `tools` inside an `exposure` block,
mirroring the parser rule that always refused an exposure block without
tools, so the two agree on `exposure: {views: true}`. No valid document
changes shape. Examples `valid-remote-views.yaml` and
`invalid-views-without-tools.yaml`.

Revision 5 (2026-09-21, https://docs.straza.ai/guides/serve-mcp-apps/caller-credentials/):
`credential.agents` gains the value `client_credentials`, valid with kind
`oauth` only. An agent with no stored sign-in of its own then runs on a token
of its own client at the provider named under `oauth.provider`. The manifest
names nothing else: the token endpoint, the assertion audience and the scopes
come from the operator's configuration of that provider, and a platform MUST
refuse the value at install time when that configuration does not say the
provider trusts the platform's client assertion keys. Additive: every earlier
valid example parses unchanged, and a platform of an earlier revision refuses
a manifest that carries the value, which is the fail-closed answer. Examples
`valid-midpoint-remote-client-credentials.yaml` and
`invalid-client-credentials-on-token.yaml`.

Revision 4 (2026-09-10, https://docs.straza.ai/guides/serve-mcp-apps/caller-credentials/):
`credential.kind` gains `token`, each caller's own pasted token, and the
credential block gains `agents`, what an agent with no row of its own uses on
a caller-kind app. Both are additive: every earlier valid example parses
unchanged. The schema and the parser now refuse a caller kind (`oauth` or
`token`) on a `command` or `oci` runtime, which no shipped manifest used and
which never resolved a credential, because one process is one identity.
Examples `valid-github-remote-token.yaml`, `invalid-token-on-command.yaml`
and `invalid-agents-on-static.yaml`.

Revision 3 (2026-07-21): the `straza.dev` apiVersion and ids and the
`straza:` manifest key replace the pre-release names; document shapes
unchanged. The pre-release ids are a clean break (§6).

Revision 2 (2026-07-14): added `limits.timeoutSeconds` (per-app upstream call
ceiling).

Words MUST/SHOULD/MAY are RFC-2119.

## 1. Document

An App manifest is one YAML document (`apiVersion: straza.dev/v1beta1`,
`kind: App`) that wraps a verbatim MCP-registry `server.json` under `server:`
and adds Straza deployment extensions under `straza:`.

- `metadata.name` MUST be unique per deployment. It is the app's policy
  identity (`apps: [github]` in PolicySet rules) and the gateway namespace
  prefix: tools are served to clients as `<name>__<toolName>`. The name
  pattern forbids `_`, so the `__` separator is unambiguous.
- `metadata.namespace` is informational (the registry namespace).
- `server:` MUST carry `name` and `version` (strings). Implementations MUST
  preserve the rest of the block byte-for-byte semantically (round-trip as
  data, never re-interpret): it is the upstream registry's contract, not
  Straza's.

## 2. Runtime

`straza.runtime.kind` selects exactly one runtime block; the manifest is
invalid when the selected block is absent or another runtime block is present.

| kind | block | transport | lifecycle |
|---|---|---|---|
| `command` | `command: {exec, args, env, workdir}` | stdio child process | managed: restart with backoff, log ring |
| `remote` | `remote: {url, auth}` | streamable HTTP | none: health via MCP ping |
| `oci` | `oci: {image, sandbox, env}` | stdio in container | managed (platforms without a container backend MUST validate, MAY refuse to start) |

`remote.auth` defaults to `inject`: the gateway adds the credential per
`straza.credential`. `passthrough` is reserved for EMA/ID-JAG token exchange
(v1.5); v1beta1 platforms MUST treat it as "no injection". In every mode the
gateway MUST NOT forward client-supplied `Authorization` headers upstream.
The client's bearer token is the Straza session token and never leaves the
gateway.

## 3. Credential

`straza.credential.kind`: `none` (default), `static` (the server's shared
secret, one row for the server and an override per role, resolved by the
credential broker), `oauth` (each caller's own grant through a configured
provider; a platform without a connect flow MUST validate and MUST fail
calls closed while no grant exists), or `token`
(each caller's own pasted token, sealed under the caller's id, revision 4).
`oauth` and `token` are the caller kinds.

- A caller kind is valid on the `remote` runtime only. A `command` or `oci`
  server is one process with one environment and therefore one identity, so
  a manifest that pairs a caller kind with either MUST be refused, and a
  platform MUST refuse to resolve a caller's row for such a runtime even
  when a stored manifest says otherwise.
- `agents` (`own` default, `sponsor`, `shared`; caller kinds only, revision
  4) says what an agent with no row of its own runs on: nothing (`own`), its
  sponsor's own row when the sponsor allowed agents on it (`sponsor`), or
  the server's shared static rows (`shared`). A human caller never falls
  back; an own row that exists but is expired or unusable MUST deny rather
  than fall through. The gateway's audit record names the source it used.
- `agents: client_credentials` (kind `oauth` only, revision 5) gives each
  agent a token of its own client at the server's provider. The platform
  runs the OAuth client credentials grant (RFC 6749 section 4.4) there and
  authenticates with a client assertion it signs (RFC 7523 section 2.2, `iss`
  and `sub` the client id). The client id MUST be the username of the
  verified calling session and MUST NOT come from the manifest, the request,
  a header or a tool argument, because whoever chooses it chooses the
  identity the upstream sees. The token stays gateway-side like every other
  credential. A human caller never gets such a token, and an agent's own
  stored sign-in wins over it.
- `inject` is required exactly when `kind != none`, and forbidden for `none`.
- `inject.as: header` is only valid for `remote` runtimes; `inject.as: env`
  only for `command` and `oci` runtimes (stdio has no headers; remotes get no
  process environment).
- `inject.template` MUST contain `{{secret}}` (default `"{{secret}}"`). The
  rendered value exists only gateway-/runtime-side. No API surface may return
  it to a client; conformance includes a never-leaks test.
- A `static`, `oauth` or `token` app with no resolvable secret for the
  calling session MUST deny the call (fail closed) rather than call
  upstream uncredentialed.

## 4. Exposure and limits

`straza.exposure.tools` (glob list, default `["*"]`) is the manager-level cap:
tools not matched simply do not exist in any catalog. Role `tool_bindings`
narrow further; a session sees the intersection.

`straza.exposure.views` (boolean, default false, revision 6) lets the server
show its MCP Apps views (extension `io.modelcontextprotocol/ui`). A view is a
`ui://` resource whose content is one HTML document of type
`text/html;profile=mcp-app`, linked from a tool by `_meta.ui.resourceUri` or
by the older flat key `_meta["ui/resourceUri"]`. When the key is true:

- The platform's own `initialize` to the server MUST advertise the extension
  with that media type, and a change of the key MUST take effect on a new
  upstream session.
- The platform reads each view an exposed tool links and serves it only on
  the server's own gateway endpoint, to a caller whose catalog holds a tool
  that links it. A view that cannot be read, is not a `ui://` resource, is
  not exactly one text item of that media type, or exceeds 2 MiB is not
  served, and the tools that link it are listed without that link. A server
  may have at most 32 views served.
- A change of a served view or of a tool's link counts as a change of the
  server's tool inventory.

When the key is false or absent, the platform's `initialize` is unchanged,
no view is served, and no listed tool carries `_meta.ui` or the flat key.
Changing the key is a decision about what a server may put in front of
people, so a platform SHOULD reserve it to the same standing as a change of
the runtime.

`straza.limits` (`cpu` and `mem` as Kubernetes quantities; `rps`;
`timeoutSeconds`) are always parsed and persisted; `rps` and
`timeoutSeconds` are enforced, and `cpu` and `mem` are recorded and not yet
enforced. `rps` bounds `tools/call` per (session, app) at the gateway.
`timeoutSeconds` caps a single gateway→upstream `tools/call` for this app;
`0`/absent means the server-wide `apps.upstreamTimeout` (default 30 s)
applies. Both MUST be >= 0.

## 5. Registry import

`strazactl apps import` converts one MCP-registry `server.json` object into a
manifest. Rules (deterministic; fixtures in `spec/conformance/registry/`):

1. `server:` = the input, verbatim.
2. `metadata.namespace` = the part of the registry `name` before the last
   `/`; `metadata.name` = the part after it, lowercased, with every character
   outside `[a-z0-9-]` replaced by `-` and leading/trailing `-` trimmed
   (overridable with `--name`).
3. Runtime selection precedence: first `remotes[]` entry with
   `type: streamable-http` → `remote`; else first `packages[]` entry with
   `transport.type: stdio` in registry-type order `oci`, `npm`, `pypi` →
   that runtime. Anything else (e.g. SSE-only remotes) MUST be rejected.
   `--runtime` overrides the precedence among available options.
   - `npm` → `command`: `exec: npx`, `args: [-y, <identifier>@<version>]`.
   - `pypi` → `command`: `exec: uvx`, `args: [<identifier>==<version>]`.
   - `oci` → `oci`: `image: <identifier>` (docker `runtimeArguments` are NOT
     translated, because the platform sandboxes containers itself).
4. Non-secret `environmentVariables` with a fixed `value` → runtime `env`
   entries.
5. Exactly one `isSecret: true` variable or header is supported in v1beta1
   (more → reject): it becomes `credential: {kind: static}` with
   `inject.as: env` (packages) or `inject.as: header` (remotes),
   `inject.name` = the variable/header name, and `inject.template` = the
   registry `value` with its `{placeholder}` replaced by `{{secret}}`
   (no value → `"{{secret}}"`).
6. `exposure`, `limits`: defaults.

## 6. Versioning

`straza.dev/app-manifest/v1beta1`. Changes to this artifact follow the
additive-change rule in the spec README: schema + fixtures + CHANGELOG +
version bump in the same PR.

New manifests MUST declare `apiVersion: straza.dev/v1beta1`. The
pre-release ids are a clean break (2026-07-21, the brand rename) and are
NOT accepted, with no deprecated grace.
