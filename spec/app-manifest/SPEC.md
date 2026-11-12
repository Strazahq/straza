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
every example, except one whose header marks it `INVALID (semantic)`: the
schema admits that example and the parser refuses it, because its rule is
one JSON Schema cannot express. Registry-import fixtures live in
`spec/conformance/registry/` (real registry `server.json` captures + golden
imported manifests).

Revision 10 (2026-11-08, https://docs.straza.ai/guides/serve-mcp-apps/caller-credentials/):
section 3 gains the case of a provider whose operator configuration names a
Microsoft Entra agent identity blueprint. `credential.agents:
client_credentials` then gives each agent the token of its own Entra agent
identity, and the client id is the Entra agent identity the platform holds
on the verified agent's own record instead of the username. Only the text
changes. The grammar and the schema stay as they are, so every earlier valid
example parses unchanged and a platform of an earlier revision reads every
manifest the same. Example `valid-entra-remote-client-credentials.yaml`
(derived from this revision's text and the reference parser in
internal/manager, not fired against a live platform: [doc]).

Revision 9 (2026-10-24): the `straza:` block gains `git`, which lets an
agent run plain git against a git host through Straza (§8). The block names
the resource server's public base URL, the git host's base URL and the tools
a fetch and a push are decided as. Straza signs a short git grant for the
resource server (spec/grant) only after it decides the grant as a call of
the mapped tool, so a role governs git as it governs any tool. The block
needs the `objects` block, and each mapped tool must be pinned there with
`{argument: <name>}`. The schema requires the objects block. JSON Schema
cannot tie a value of `git.ops` to a key of `objects.tools`, so the example
`invalid-git-tool-not-argument.yaml` carries the `INVALID (semantic)`
marker: the schema admits it and the parser refuses it. Additive: every
earlier valid example parses unchanged, and a platform of an earlier
revision refuses a manifest that carries the key, which is the fail-closed
answer. A platform of an earlier revision that reopens a stored manifest
drops the key, so every replica of a deployment runs this revision before
the first git block is published. Examples `valid-remote-git.yaml`,
`invalid-git-without-objects.yaml` and `invalid-git-tool-not-argument.yaml`
(derived from this revision's text and the reference parser in
internal/manager, not fired against a live platform: [doc]).

Revision 8 (2026-10-19): a block that names its listing tool under
`objects.tools` gives it the reach `all`, and no other (§7). The listing
tool returns every object. A pin of `none` or an argument would let a role
on some objects that binds it read objects the role does not cover today.
A pin of `credential` would do the same once the platform checks a role's
own login, because until then the platform decides `credential` as it
decides `all`. The tool marked `ai.straza/lists-objects` counts as marked
`all` (§7.1). So a block that names another tool as its listing tool cannot
pin the real one looser either: the draft check refuses such a pin while it
can read the server's tools, and the gateway treats it as drift, so only a
role on every object may call that tool. Fired against the eval stack on
2026-10-20: a draft check and a spec validate of a block that
pins `repo_list: none` were refused with the rule's sentence [live]. The
rule tightens the parser and leaves the
schema as it is, because JSON Schema cannot tie a key of `objects.tools` to
the value of `objects.list.tool`. So the example
`invalid-objects-listing-tool-reach.yaml` carries the `INVALID (semantic)`
marker: the schema admits it and the parser refuses it. No earlier example
names its listing tool, so every earlier valid example parses unchanged. A
block stored before revision 8 keeps the reaches it pins until a draft
carries the manifest again. A draft that pins a listing tool to another
reach is refused, with the fix in the refusal.

Revision 7 (2026-10-18): the `straza:` block gains `objects`, which names the
one object type a server's tools act on, the read-only tool that lists those
objects, and how each tool reaches them (§7). A server announces the same
declaration in `ai.straza/*` tool marks, which only pre-fill a draft.
Additive: every earlier valid example parses unchanged, and a platform of an
earlier revision refuses a manifest that carries the key, which is the
fail-closed answer. A platform of an earlier revision that reopens a stored
manifest drops the key, so every replica of a deployment runs this revision
before the first type is pinned. Examples `valid-remote-objects.yaml`,
`invalid-objects-bad-type.yaml`, `invalid-objects-two-forms.yaml` and
`invalid-objects-unknown-reach.yaml`.

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
- On a provider whose operator configuration names a Microsoft Entra agent
  identity blueprint (revision 10), the platform gets that token in the two
  grants of the Entra agent identity exchange. The blueprint authenticates
  with a certificate assertion the platform signs and asks for an exchange
  token for the agent's Entra agent identity, which then presents it and
  gets its own token. The client id is then the Entra agent identity the
  platform holds on the verified calling agent's own record, written by the
  identity source or an administrator. It MUST NOT come from the manifest,
  the request, a header or a tool argument either. An agent whose record
  holds none MUST be refused before the identity provider is asked, and the
  exchange token MUST NOT be kept, logged or returned.
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

## 7. Objects

`straza.objects` (revision 7) says which object type a server's tools act
on, which tool lists the objects, and how each tool reaches them. A server
has at most one object type. The block is the pinned declaration: a platform
decides with this copy only, never with the server's live marks.

```yaml
straza:
  objects:
    type: repo
    plural: repos
    list:
      tool: repo_list
      attributes:
        - {name: team, setBy: org}
        - {name: topic, setBy: object}
    tools:
      pr_create: {argument: repo}
      search_code: all
      get_me: none
```

- `type` (required) is one lowercase word of up to 32 letters, digits or
  hyphens that starts with a letter (`^[a-z][a-z0-9-]{0,31}$`).
- `plural` is the word people read for many objects, up to 40 characters of
  the same form. Absent means the type plus `s`.
- `list.tool` (required) names the server's listing tool. A tool name here
  and in `tools` follows the MCP specification: 1 to 128 ASCII letters,
  digits, underscores, hyphens or dots.
- `list.attributes` names at most 16 attributes the listing returns and the
  platform keeps. Each has a `name` (`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`,
  unique in the list) and `setBy`, who can set the value in the source
  system: `org` means only the organization's admins, `object` means the
  object's own admins as well. Absent `setBy` reads as `object`.
- `tools` maps a tool name to its reach, one of four:
  - `{argument: <name>}`: the call names one object in that argument. The
    argument MUST be a top-level property of the tool's `inputSchema`, of
    type string, listed in `required`. Its name matches
    `^[A-Za-z_][A-Za-z0-9_-]{0,63}$`.
  - `credential`: the call does not name the object, and the credential the
    call carries limits what it reaches.
  - `all`: the tool can reach every object, such as a search across them.
  - `none`: the tool touches no object, such as a tool that answers who the
    caller is.

  A tool the mapping does not name reaches every object, as `all` does,
  until a person decides. So a tool the server adds later never slips past a
  limit. The listing tool lists every object by nature, so it reads as
  `all` with no decision to make, and a block MUST NOT name it with another
  reach (revision 8).

A platform MUST NOT publish a block whose listing tool the running server
lacks or does not mark `readOnlyHint: true`, whose `argument` breaks the
rule above against the live `inputSchema`, or whose reach for a tool is
looser than the server's own mark. A change of this block alone SHOULD NOT
restart the server's instances.

### 7.1 Tool marks

A server that adopts the convention marks its tools in its `tools/list`
answer with `_meta` keys under the prefix `ai.straza/`, as the MCP
specification asks of third-party extensions:

```json
{ "name": "pr_create",
  "_meta": { "ai.straza/object": { "type": "repo", "argument": "repo" } } }
{ "name": "search_code",
  "_meta": { "ai.straza/object": { "type": "repo", "reach": "all" } } }
{ "name": "repo_list", "annotations": { "readOnlyHint": true },
  "_meta": { "ai.straza/lists-objects": { "type": "repo",
    "attributes": [ { "name": "team", "setBy": "org" } ] } } }
```

`ai.straza/object` carries a `type` and either an `argument` or a `reach`
(`all`, `none` or `credential`), never both. `ai.straza/lists-objects` sits
on exactly one tool. Every mark of a server names the same type. The tool
that carries `ai.straza/lists-objects` carries no `ai.straza/object` mark,
or one with the reach `all`, and it counts as marked `all` (revision 8).

Marks only pre-fill a draft of this block. A person publishes the pinned
copy. A pinned reach MAY be stricter than the server's mark, where `all` is
stricter than every other reach and every reach is stricter than `none`,
and MUST NOT be looser. The `ai.straza/*` keys count toward the server's
tool inventory, so a changed mark is drift that a person resolves with a
new draft.

### 7.2 The listing

The platform calls the listing tool with an ordinary `tools/call`, with the
arguments `{}` for the first page and `{"cursor": "<nextCursor>"}` for each
page after. The tool answers in `structuredContent`:

```json
{ "objects": [ { "value": "Strazahq/straza", "id": "9012331", "title": "straza",
                 "attributes": { "team": "product" } } ],
  "nextCursor": "4" }
```

- `value` is the canonical string a tool argument carries, a non-empty
  string of valid UTF-8 that names one object in the listing.
- `id` never changes and is never reused. It is a JSON string or an integer
  number, which the platform keeps as its decimal string, and names one
  object in the listing. An item with no `id` uses its value as the id.
- `title` is the name people read.
- `attributes` maps a name to a string or a list of strings. The platform
  keeps only the attributes `list.attributes` names.
- `nextCursor` absent or empty ends the listing.

A listing counts only when every page arrived. A result with `isError: true`
is a failure. Limits: at most 100,000 objects per server, 512 bytes per
value and per id, 256 bytes per title, and 16 attributes per object, each a
string of at most 128 bytes or a list of at most 16 such strings. A page may
take 30 seconds and a whole listing 5 minutes. A listing over a limit,
with a conflict, or that fails in any other way changes nothing: the
platform keeps the last good list.

An item that repeats an earlier item of the same listing exactly, with the
same value, id, title and kept attributes, is dropped, because a server
that pages by page number shifts its items when an object is created while
the platform lists. A conflict fails the listing as a whole: one value with
two ids, one id with two values, or one value or id seen twice with a
different title or attributes.

### 7.3 The listing credential

- On a `remote` runtime whose credential kind is `static`, `oauth` or
  `token`, the listing call carries the server's listing credential, a
  secret stored for that purpose and nothing else. The platform MUST NOT
  fall back to the server's own secret, a role's or a caller's, and MUST
  NOT send the listing credential on any other tool call. Without one, the
  listing fails.
- On a `remote` runtime with credential kind `none`, the listing call
  carries no credential.
- On a `command` or `oci` runtime, the server is one process with one
  identity, so the listing goes to that process with no per-call
  credential.

### 7.4 What a declaration promises

Every declaration, whether the server's marks or a person wrote it, carries
three promises that the platform cannot check:

1. The call touches only the object the argument names.
2. The value names exactly one object at the time of the call, with no
   rename redirect, alias or case folding.
3. The server accepts calls only from the gateway, or verifies a credential
   only the gateway can obtain.

### 7.5 Later revisions

`source: admin` and `fields`, which let a person declare objects for a
server that marks nothing and say where each item's value, id, title and
attributes sit, come in a later revision. A platform of this revision
refuses them as unknown keys.

## 8. Git

`straza.git` (revision 9) lets an agent run plain git against a git host
through Straza. The agent's git reaches `straza service`, which asks the
platform for a git grant: a short token that lets the resource server at
`url` serve one fetch or one push of one repo (spec/grant). The platform
decides each grant as a call of the tool that `ops` maps the operation to,
with every gate a `tools/call` of that tool passes. The resource server
holds the git host's credential, so the block names none.

```yaml
straza:
  objects:
    type: repo
    list:
      tool: repo_list
    tools:
      git_fetch: {argument: repo}
      git_push: {argument: repo}
  git:
    url: https://ghapp.example.com
    upstream: https://github.com
    ops:
      fetch: git_fetch
      push: git_push
```

- `url` (required) is the resource server's public base URL. Its canonical
  form is the `aud` of every git grant for this server, and the resource
  server serves git under `<url>/git/<owner>/<name>.git/`. A resource
  server compares `aud` with its own public URL byte for byte, so the two
  MUST name the same canonical URL.
- `upstream` (required) is the base URL of the git host the server's repos
  live on, such as `https://github.com`. The platform takes a repo URL as
  this server's when `upstream` followed by `/` prefixes it. It reads the
  owner and name from the next two path segments with `.git` removed, and
  looks the repo up in the server's object list without regard to case.
  The platform tells clients every upstream, so that git sends only those
  hosts through Straza.
- Both URLs are absolute and use `https`, or `http` on a loopback host only:
  `localhost`, `::1` or an IPv4 address in 127.0.0.0/8. They carry no
  userinfo, query or fragment. A platform MUST refuse any other URL. It
  MUST store each in canonical form: the scheme and host in lower case, no
  port when it is the scheme's default (443 for https, 80 for http), and no
  trailing slash. The path keeps its case.
- `ops` (required) maps `fetch`, `push` or both to tool names of the
  server, which follow the tool name rule of §7. At least one op is mapped.
  A fetch grant is decided as a call of the fetch tool, and a push grant as
  a call of the push tool. The platform gives no git grant for an op the
  block does not map.
- The block MUST NOT appear without `objects` (§7), and each mapped tool
  MUST be pinned under `objects.tools` with the reach `{argument: <name>}`.
  A git grant names exactly one repo, and the platform passes it to the
  decision in the argument the pin names. So the object gate checks that
  repo, and an approval's fingerprint and card show it. A tool with another
  reach names no argument to pass the repo in.
