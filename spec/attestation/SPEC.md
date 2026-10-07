# Attestation, v1beta1

Status: **beta** (no wire changes from v1alpha3, see Changelog).

The check-in payload (`checkin-request.schema.json`) carries the harness
identity, the client platform, and the measured hashes of every artifact the
client-side decision depends on. Levels computed by the server:

- `managed` means the check-in claims a managed install (`managed: true`) and
  **verifies** against the expected-hash registry (see below).
- `advisory` means measurements are present but unverified: an unmanaged
  (user-mode) install, or a managed claim for which the registry holds no
  applicable rows.
- `none` means no measured hashes, **or tamper evidence**: a managed claim
  whose measurements mismatch or omit a registered artifact.

## Measurement keys

| Artifact | Meaning |
|---|---|
| `self` | The straza binary. |
| `config` | The straza managed configuration (server URL, pinned keys). |
| `hooks.<harness>` | The managed hook-wiring file for one harness. |

`platform` is the client's `GOOS/GOARCH` (e.g. `linux/amd64`); it selects
platform-scoped registry rows.

## Verification algorithm (server, at session start)

1. No hashes → `none`.
2. `managed: false` → `advisory` (user-mode installs are advisory at best).
3. `managed: true`: select registry rows whose `harness` is empty or equals
   the check-in harness, and whose `platform` is empty or equals the payload
   platform. Group the selected rows by artifact: each group is that
   artifact's **allowed set** (multiple rows per artifact support upgrade
   windows).
   - No selected rows → `advisory` (nothing to verify against).
   - Every selected artifact must be present in the payload with a hash from
     its allowed set; a mismatch **or a missing measurement** → `none`.
   - Otherwise → `managed`. Extra unregistered measurements are recorded but
     ignored.
4. Attestation is session-scoped: fixed at session start, never re-derived
   from a refresh payload.
5. Renewal: when the check-in authenticated with a
   `device_token` that is past half its lifetime and every step above passed
   (denylist, user and device status, the attestation minimum), the answer
   carries a fresh `device_token` with `device_token_expires_in` and the
   client replaces its stored credential. No other lane and no refused
   check-in ever carries one. An active device therefore never re-enrolls,
   while one idle for the full lifetime still expires.
6. Client binding: a device row records the kind of client that enrolled
   it, `kit` or `human`, from the enrol request's `client_kind` (openapi
   0.114.0), and a row from before the kind was recorded counts as `kit`.
   The harness names of the human clients (`strazactl`, `console`,
   `self-service`) open a session only on a `human` row and only for a user
   who is a person; every other harness name opens a session only on a
   `kit` row. A mismatch is refused with 403 before the attestation minimum
   and its exemption are judged, and writes one `straza.audit.authn` login
   failure naming the reason.

The profile knob `governance.minAttestation` (`none` | `advisory` | `managed`;
enterprise default `managed`) gates token issuance: a check-in below the
minimum receives **no session token**, with an actionable reason.

Registry rows are managed via `/v1/admin/attestation-hashes` (see
`pkg/api/openapi.yaml`). The server registers the hooks rows of its own
harness-config render at every boot; an administrator adds a row by hand
only for a local render the server never published (`strazactl attestation
add`, the line `straza install --managed` prints in that one case).

Examples in `examples/` are replayed verbatim by the platform contract tests
(`internal/server`): `${ID_TOKEN}`/`${DEVICE_ID}` placeholders are substituted
at test runtime; the managed example's hashes are pre-registered by the test.

Changes to this artifact follow the additive-change rule in the spec README:
schema + fixtures + CHANGELOG + version bump in the same PR.

## Changelog
- **v1beta1** (2026-09-23, client binding): verification algorithm step 6.
  A device row records the kind of client that enrolled it from the enrol
  request's `client_kind` (openapi 0.114.0), and a check-in whose harness
  name belongs to the other class of client is refused with 403 and one
  chained login failure. The check-in request shape, the examples and the
  version are unchanged: the binding is judged on the stored row, and the
  harness name was already required.
- **v1beta1** (2026-09-09, client stamp): the request may carry `client`
  (`version`, `commit`), the straza build making the check-in; the server
  records it on the session and the sessions list shows it as
  `client_version` (openapi 0.95.0). Additive: older clients omit it, the
  earlier examples are unchanged, and nothing verifies the value (it is a
  self-reported build stamp for fleet hygiene, not attestation).
- **v1beta1** (2026-09-05, device credential renewal): the check-in ANSWER may carry
  `device_token` + `device_token_expires_in`, a renewed enroll credential
  issued when the presented one is past half its lifetime (verification
  algorithm step 5; openapi 0.87.0). Additive: the request shape and the
  examples are unchanged, older clients ignore the fields, older servers
  never send them.
- **v1beta1** (2026-07-27, client binary rename): references to the `straza`
  binary (was `straz`); CloudEvents source `straza` (spec/events revision 14);
  ID-token audience `straza`. Wire shapes unchanged: artifact keys (`self`,
  `config`, `hooks.<harness>`) are name-independent. No legacy fallback: the
  rename precedes the first release (internal testing only).
- **v1beta1** (2026-07-21, the brand rename): the namespace and brand rename
  brings the `straza.dev` schema id, measurement and registration references
  to the `straz` binary (was `agentguard`), and the CloudEvents source
  `straz`; check-in payload shape unchanged.
- **v1beta1** (2026-07-14): schema id stabilized for the spec v1.0 tag;
  no wire changes from v1alpha3.
- **v1alpha3** (2026-07-14): added optional `device_token` as a third
  check-in authentication alternative, the long-lived enroll credential
  (purpose-scoped JWT, `use=device`, bound to user+device, default 720 h,
  `governance.deviceTokenTTL`). Its device binding is authoritative; a
  `device_id` sent alongside it is ignored. Denylist (user/device) and
  user/device status are re-checked at every use.
- **v1alpha2** (2026-07-14): added optional `attestation.platform`;
  defined the expected-hash registry verification algorithm; `managed` is now
  reachable.
- v1alpha1: initial draft; attested check-ins capped at `advisory`.
