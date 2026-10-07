# Canonical object documents, v1beta1 (revision 6)

Revision 6 (2026-10-01, https://docs.straza.ai/guides/serve-mcp-apps/catalogs-per-role/):
an application role that reaches an MCP server belongs to it. A Role
document of kind `application` that creates a role with a binding names
that binding's server in `spec.server`, whoever creates it, and the role's
name begins with the server's folded name and a hyphen. A global admin may
give such a role the every-tool matcher `["*"]`, and a server's admin may
not. An application role created without `spec.server` reaches no MCP
server: a binding it did not already have is refused, and a binding it
already had stays as it was. No field is added or removed, so a revision 5
reader sees the same documents and `fixtures/role-dev.yaml` is unchanged.

Revision 5 (2026-09-24, https://docs.straza.ai/guides/operate/drafts-and-publishing/):
the apply half of section 4 ships as the bundle a config draft reads. A
bundle is a YAML stream of App documents (spec/app-manifest), Role
documents (section 2), PolicySet documents (spec/policyset) and Removal
documents (section 5). It states the desired state of the objects it names
and leaves every other object as it is: nothing is pruned that a Removal
does not name. A Role document is the whole role for its description, its
binding and its implications, so a role whose document has no
`spec.bindings` has no access row after the publish, and one with no
`spec.implies` implies nothing. `spec.packs` is read and never applied,
because knowledge packs are bound directly. `spec.kind` is required in a
bundle, as every export writes it. `strazactl bindings apply` is removed:
a Role document already carries its access row, and two spellings of one
row broke the one-canonical-form rule. No document shape of revision 4
changes, so every export is a valid bundle document.

Revision 4 (2026-09-18, https://docs.straza.ai/guides/serve-mcp-apps/catalogs-per-role/):
an application role reaches one MCP server, so `spec.bindings` on a Role
document of kind `application` carries at most one entry. The document
shape is unchanged and no field is added or removed: a revision 3 reader
sees the same document, `fixtures/role-dev.yaml` already carries one
binding, and the server refuses the second access row on the API with a
sentence naming the way out, one role per server composed by a business
role. A document with two bindings is the export of no role.

Revision 3 (2026-09-18, https://docs.straza.ai/guides/operate/delegate-one-server/):
a Role document gains `spec.server`, the name of the MCP server that owns
the role, present only on a server-owned role and omitted on every other
role. A server-owned role is an application role named `<server>-<suffix>`
that its server's admin defines and that reaches that server alone. The
field is additive: an exporter that predates it omits it, and a reader
that predates it ignores it. Its one binding stays in `spec.bindings` as
before, so a revision 2 reader sees the same reach.

Revision 2 (2026-08-25): the control
plane's kind spelling renames `console` to `straza` on every wire
(openapi 0.81.0), and the spec.kind enum below is trued to name
`approver` (the kind has existed since spec/policyset revision 15; its
omission here was a doc gap, exports always carried it).

## 0. Why this exists

A role you can diff in a pull request is a role an auditor can certify and a
pipeline can recreate. This spec defines the canonical, VCS-ready YAML face
of Straza configuration objects (the midPoint repo-object idea, YAML-shaped):
`GET /v1/admin/roles/{id}/export` serves it, `strazactl roles export <name>`
and the console's Download button consume the SAME server serializer, so
there is exactly one spelling.

## 1. Document contract

- **Name-keyed, id-free**: documents reference objects by name only, never
  by row id, so the same document applies to another deployment.
- **Deterministic**: every list is sorted (implies, packs, and bindings by
  app name; tools within a binding lexically), so re-exports are
  byte-stable and diffs are honest.
- **Desired state only**: no timestamps, no origin markers, no counts.
  Runtime facts belong to the API, not the document.
- Serialization is gopkg.in/yaml.v3 defaults (4-space indent); the
  conformance fixture pins the exact bytes.

## 2. kind: Role (the aggregate)

```yaml
apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: <role name>
spec:
    kind: business | application | approver | straza
    description: <string, omitted when empty>
    server: <app name>  # the MCP server that owns the role; omitted on a global role
    implies:        # sorted role names; omitted when empty
        - <role name>
    bindings:       # at most one entry since revision 4; omitted when empty
        - app: <app name>
          tools:    # sorted glob list; ["*"] = every tool; [] grants nothing
            - <glob>
    packs:          # sorted pack names; omitted when empty
        - <pack name>
```

`kind: straza` is the control plane (openapi 0.59.0, spelled `console`
until revision 2): such a role grants strazad itself and carries no
bindings or packs by API invariant, so those sections never appear in
its export.

## 3. Fixtures

`fixtures/role-dev.yaml` is round-verified byte-for-byte by
`TestRoleExportGolden` on every test run (the running serializer is the
source, not hand-derivation).

The bundle fixtures are derived from this document and the app-manifest
and policyset specifications, not captured from a live wire.
`fixtures/bundle-onboard.yaml` holds an App, two Roles it owns and their
PolicySet, and `fixtures/bundle-removal.yaml` holds one Removal.
`fixtures/invalid-bundle-two-bindings.yaml`,
`fixtures/invalid-bundle-missing-kind.yaml` and
`fixtures/invalid-bundle-secret-env.yaml` are each refused for the reason
the file name gives: an application role with two bindings, a Role with no
`spec.kind`, and an environment entry under a secret's name that holds its
value. `TestBundleFixtures` reads every fixture here with the bundle reader
on every test run, the two Role exports included, and runs the intake
checks over each bundle the reader accepts, which is where the secret
check runs.

## 4. Apply: the bundle

A bundle is one or more texts. Each text is a YAML stream whose documents
are separated by lines holding exactly `---`. The reader keeps each
document's bytes and cross-checks their number with a YAML decoder, and a
stream it cannot split cleanly is refused rather than guessed. A
PolicySet's bytes are the set's text as stored and published, comments
included. An App's bytes are kept as sent and read with the manifest
parser. A Role document is read strictly and kept in the export's form, so
a draft always holds what `GET /v1/admin/roles/{id}/export` would serve.
One object is named once in a bundle. A bundle applies whole or not at
all, and a person publishes it: `strazactl drafts create -f <dir>`, a file
in the watched apps directory, the console, or an agent through the
built-in straza app's drafting tool.

## 5. kind: Removal

```yaml
apiVersion: straza.dev/v1beta1
kind: Removal
metadata:
    name: <object name>
spec:
    kind: App | Role | PolicySet
```

A Removal removes the object it names when the bundle is published. A
bundle may not both write and remove one object. Removing an App removes
its access rows, its stored secrets, every person's connection, the roles
it owns and its admin role. Removing a Role ends every membership of it.
Removing a PolicySet takes it out of the published snapshot and deletes it.

## 6. Deliberately NOT in revision 1

- Other kinds (Pack, Group; App already has app.yaml in the GitOps
  channel).
