package config

// Knob records one configuration knob's faces. The knobs table below is the
// single source of truth for every knob: its yaml path, its
// environment-variable face (or an explicit reason it has none), and the docs
// section that documents it. Recording the decision HERE, once, keeps the
// faces from drifting apart, and knobs_test.go enforces it:
//
//   - a Config field with no row here fails TestKnobTableCovers;
//   - a row claiming an env face that applyEnv does not wire fails
//     TestKnobEnvFacesWired (the probe sets it, then asserts the field moved);
//   - an env variable wired in code but absent from this table fails
//     TestKnobTableMatchesApplyEnv;
//   - a Doc that names no KnobSections id fails TestKnobDocAnchorsExist.
//
// Env == "" requires NoEnvReason: "no face" is a decision, never an omission.
// tools/docsgen renders every string here into the public configuration page.
type Knob struct {
	Path        string // dot-joined yaml path ("server.approverTLS.listen"); container rows name the collection ("sinks")
	Env         string // environment face; "" = deliberately none
	NoEnvReason string // required when Env == ""
	EnvSpecial  string // non-empty = wired outside applyEnv or needs a custom probe (see knobs_test.go)
	Doc         string // KnobSection id of the configuration reference section that holds the knob
	Note        string // one plain sentence the configuration page prints beside the knob: what it does, when it is required and its default
}

// KnobSection is one section of the generated configuration reference: ID is
// the heading anchor the Doc field of a Knob names, Title is the heading text.
type KnobSection struct {
	ID    string
	Title string
}

// Knobs returns a copy of the knob table in its declared order.
func Knobs() []Knob {
	out := make([]Knob, len(knobs))
	copy(out, knobs)
	return out
}

// KnobSections returns the sections of the configuration reference in the
// order the knob table first names them. Every Doc value in the table is one
// of these ids and every id has at least one knob (knobs_test.go holds both).
func KnobSections() []KnobSection {
	return []KnobSection{
		{ID: "config-core", Title: "Core"},
		{ID: "config-server", Title: "Server"},
		{ID: "tls", Title: "TLS on the main listener"},
		{ID: "tls-approver", Title: "The approver listener"},
		{ID: "config-store-events", Title: "Store and events"},
		{ID: "config-oidc-oauth", Title: "OIDC and OAuth"},
		{ID: "config-governance", Title: "Governance"},
		{ID: "config-approval", Title: "Approval"},
		{ID: "config-apps", Title: "Apps"},
		{ID: "config-reference", Title: "The rest of the tree"},
		{ID: "config-sinks", Title: "Sinks"},
	}
}

// noEnvSecurityPosture is shared by the knobs whose env face is withheld ON
// PURPOSE, because a hidden environment variable silently changing
// enforcement posture is exactly what the CLI rules forbid: no silent
// defaults, no hidden env.
const noEnvSecurityPosture = "Set in the file only, so a change to this enforcement setting shows in the config file."

// fileOnlyTuning marks knobs where file-only is a deliberate scope decision,
// not a gap: add an env face when an operator demonstrably needs one.
const fileOnlyTuning = "Set in the file only."

// withSentinel and withBodyStore are the reasons of the keys that only mean
// something beside the key that switches their component on.
const (
	withSentinel  = "Set in the file only, with sentinel.enabled."
	withBodyStore = "Set in the file only, with bodyStore.type."
)

// removedSCIMKey is the reason of the tombstone rows; it matches the error
// validation gives.
const removedSCIMKey = "Removed. strazad refuses to start while the key is present: delete it, because roles now render as SCIM groups and membership is assignment."

var knobs = []Knob{
	{Path: "profile", Env: "STRAZA_PROFILE", EnvSpecial: "resolveProfile", Doc: "config-core", Note: "The governance profile, standalone or enterprise. The default is standalone."},
	{Path: "dataDir", Env: "STRAZA_DATA_DIR", Doc: "config-core", Note: "The directory of local state: the SQLite database, the embedded event bus's storage, the key-encryption key and the approver certificate. The default is `data`, under the directory strazad starts in."},

	{Path: "server.listen", Env: "STRAZA_LISTEN", Doc: "config-server", Note: "The address the API, the console and the MCP gateway listen on. The default is `127.0.0.1:8420` under standalone and `:8420` under enterprise."},
	{Path: "server.publicUrl", Env: "STRAZA_PUBLIC_URL", Doc: "config-server", Note: "The base URL clients reach strazad at, with no trailing slash. It is the session-token issuer and the base of every link strazad hands out, so it cannot be empty. The default is `http://127.0.0.1:8420`."},
	{Path: "server.projectName", Env: "STRAZA_PROJECT_NAME", Doc: "config-server", Note: "The name approver apps show for this deployment when a phone holds several, a label only. The default is `straza-` and the last four hex characters of the project id."},
	{Path: "server.tls.certFile", Env: "STRAZA_TLS_CERT_FILE", Doc: "tls", Note: "The PEM certificate, leaf first, that turns on HTTPS on the main listener together with keyFile. Empty by default, which serves plain HTTP, fit only for loopback or behind a proxy that terminates TLS."},
	{Path: "server.tls.keyFile", Env: "STRAZA_TLS_KEY_FILE", Doc: "tls", Note: "The PEM private key of certFile, set together with it. Empty by default."},
	{Path: "server.maxBodyBytes", NoEnvReason: fileOnlyTuning, Doc: "config-server", Note: "The largest request body strazad accepts, in bytes. /mcp and /v1/audit/batch have limits of their own, and 0 turns the cap off. The default is 1 MiB."},
	{Path: "server.metricsToken", Env: "STRAZA_METRICS_TOKEN", Doc: "config-server", Note: "A bearer token that GET /metrics then requires. Empty by default, which leaves /metrics open to anyone who reaches the listener."},
	{Path: "server.loginPerIPRPS", NoEnvReason: fileOnlyTuning, Doc: "config-server", Note: "Requests per second each client address may make to the sign-in routes that take a credential with no session: the password form in both profiles, which under enterprise signs in only the emergency admin, and under enterprise also the agent token endpoint. 0 turns the limit off. Behind a reverse proxy every client shares the proxy's address. The default is 2."},
	{Path: "server.approverTLS.listen", Env: "STRAZA_APPROVER_TLS_LISTEN", Doc: "tls-approver", Note: "The address of a second listener, HTTPS only, that serves only the approver routes a phone uses. Set it with certFile, keyFile and publicUrl, or let autoMint fill it in as `:8443`. Empty by default."},
	{Path: "server.approverTLS.certFile", Env: "STRAZA_APPROVER_TLS_CERT_FILE", Doc: "tls-approver", Note: "The PEM certificate of the approver listener. A phone pins it at enrollment, so a new certificate means enrolling the phone again. Empty by default."},
	{Path: "server.approverTLS.keyFile", Env: "STRAZA_APPROVER_TLS_KEY_FILE", Doc: "tls-approver", Note: "The PEM private key of the approver listener's certificate. Empty by default."},
	{Path: "server.approverTLS.publicUrl", Env: "STRAZA_APPROVER_TLS_PUBLIC_URL", Doc: "tls-approver", Note: "The https URL a phone dials, written into the enrollment QR code as it stands, so the phone must be able to reach it. Empty by default."},
	{Path: "server.approverTLS.autoMint", Env: "STRAZA_APPROVER_TLS_AUTO_MINT", Doc: "tls-approver", Note: "Lets strazad fill in at boot the approver keys you leave unset: a self-signed pair kept under `<dataDir>/approver-tls`, the address `:8443` and a URL on the host's first LAN address. The default is true under standalone and false under enterprise."},
	{Path: "server.approverTLS.perIPRPS", NoEnvReason: fileOnlyTuning, Doc: "tls-approver", Note: "Requests per second each connecting address may make to the approver listener, never read from X-Forwarded-For, and 0 turns the limit off. The default is 10."},
	{Path: "server.approverPublicUrl", Env: "STRAZA_APPROVER_PUBLIC_URL", Doc: "tls-approver", Note: "The https URL where an ingress serves the approver routes while publicUrl stays private. The approverTLS publicUrl wins when that listener is set. Empty by default, which sends a phone to publicUrl."},

	{Path: "log.level", Env: "STRAZA_LOG_LEVEL", Doc: "config-core", Note: "The lowest level strazad logs, debug, info, warn or error. The default is info."},
	{Path: "log.format", Env: "STRAZA_LOG_FORMAT", Doc: "config-core", Note: "The format of the log lines on standard error, json or text. The default is json."},

	{Path: "store.driver", Env: "STRAZA_STORE_DRIVER", Doc: "config-store-events", Note: "The database, sqlite or postgres. The default is sqlite under standalone and postgres under enterprise."},
	{Path: "store.dsn", Env: "STRAZA_STORE_DSN", Doc: "config-store-events", Note: "The PostgreSQL connection string, required with postgres, or the path of the SQLite file. The default under SQLite is `<dataDir>/straza.db`."},

	{Path: "events.embedded", NoEnvReason: "Set in the file only. Setting STRAZA_EVENTS_URL turns the embedded bus off.", Doc: "config-store-events", Note: "Runs the event bus inside strazad with no network socket, and false needs events.url. The default is true in both profiles."},
	{Path: "events.url", Env: "STRAZA_EVENTS_URL", Doc: "config-store-events", Note: "The address of an external NATS server with JetStream. strazad uses it only while embedded is false, so in the file set both. Empty by default."},
	{Path: "events.auditStreamMaxAge", Env: "STRAZA_EVENTS_AUDIT_STREAM_MAX_AGE", Doc: "config-store-events", Note: "How long the audit stream keeps a message. The stream carries each record from the outbox to the audit chain and to every sink, so a sink that stays down longer than this misses the records that aged out. The default is twice governance.captureRetention, 1440h while that keeps its default."},
	{Path: "events.auditStreamMaxBytes", Env: "STRAZA_EVENTS_AUDIT_STREAM_MAX_BYTES", Doc: "config-store-events", Note: "The most disk the audit stream may use, in bytes. When it is full it drops its oldest messages, so publishing never stops, and it should stay well below the size of the bus's volume. The default is 2 GiB."},
	{Path: "events.pushEdgeMaxConns", Env: "STRAZA_EVENTS_PUSH_EDGE_MAX_CONNS", Doc: "config-store-events", Note: "How many clients may hold the push stream open at one strazad. A client over the limit polls instead until a retry gets in. The default is 65536."},

	{Path: "oidc.issuer", Env: "STRAZA_OIDC_ISSUER", Doc: "config-oidc-oauth", Note: "The discovery URL of your identity provider, matched byte for byte, trailing slash included. The enterprise profile needs it for sign-in, while standalone signs people in with its built-in issuer. Empty by default."},
	{Path: "oidc.discoveryUrl", Env: "STRAZA_OIDC_DISCOVERY_URL", Doc: "config-oidc-oauth", Note: "The full address of your identity provider's discovery document, for a server that cannot reach the issuer's own address, such as a container that reaches the provider by an internal name. The document must still name oidc.issuer exactly, and tokens must still carry it. Empty by default, which fetches the document at the issuer."},
	{Path: "oidc.clientId", Env: "STRAZA_OIDC_CLIENT_ID", Doc: "config-oidc-oauth", Note: "The audience strazad expects on ID tokens, which is Straza's client id at your identity provider. The default is straza."},
	{Path: "oidc.jitProvision", Env: "STRAZA_OIDC_JIT", Doc: "config-oidc-oauth", Note: "Creates a user at their first verified sign-in when no user of that name exists. The default is true under standalone and false under enterprise, where your identity manager creates users over SCIM."},
	{Path: "oidc.bootstrapAdmin", Env: "STRAZA_OIDC_BOOTSTRAP_ADMIN", Doc: "config-oidc-oauth", Note: "A username whose first verified sign-in is created and made straza-admin while nobody holds straza-admin, so an enterprise install gets its first admin. Remove it after that sign-in. Empty by default."},

	{Path: "oauth.providers", NoEnvReason: "Set in the file only, and the github provider alone has the three variables below.", Doc: "config-oidc-oauth", Note: "A map of OAuth providers for each caller's own sign-in, keyed by the name a manifest's credential.oauth.provider names. Each entry takes clientId, clientSecret or clientSecretFile, authUrl, tokenUrl and scopes, and clientCredentials with assertionAudience and scopes for an agent's own token. A provider named github defaults both URLs. [Each caller's own credential]({{< relref \"guides/serve-mcp-apps/caller-credentials.md\" >}}) shows a full block."},
	{Path: "oauth.providers.github.clientId", Env: "STRAZA_OAUTH_GITHUB_CLIENT_ID", EnvSpecial: "map", Doc: "config-oidc-oauth", Note: "The client id of the OAuth app registered at GitHub."},
	{Path: "oauth.providers.github.clientSecret", Env: "STRAZA_OAUTH_GITHUB_CLIENT_SECRET", EnvSpecial: "map", Doc: "config-oidc-oauth", Note: "The client secret of that OAuth app."},
	{Path: "oauth.providers.github.clientSecretFile", Env: "STRAZA_OAUTH_GITHUB_CLIENT_SECRET_FILE", EnvSpecial: "map", Doc: "config-oidc-oauth", Note: "Path of a file that holds the client secret, which wins over clientSecret."},
	{Path: "oauth.refreshInterval", NoEnvReason: fileOnlyTuning, Doc: "config-oidc-oauth", Note: "How often the refresh worker looks for connections whose token is about to expire. The default is 1m."},
	{Path: "oauth.refreshWindow", NoEnvReason: fileOnlyTuning, Doc: "config-oidc-oauth", Note: "A token that expires within this window is refreshed on the worker's next pass. The default is 10m."},

	{Path: "governance.offlineGraceTTL", NoEnvReason: noEnvSecurityPosture, Doc: "config-governance", Note: "How long a client keeps deciding from its last signed snapshot while strazad cannot be reached, and 0 denies at once. The default is 15m under standalone and 0 under enterprise."},
	{Path: "governance.localToolDefault", NoEnvReason: noEnvSecurityPosture, Doc: "config-governance", Note: "The decision for a tool call that no policy rule matches and that is not an MCP tool, allow or deny. It covers the local tools, a tool name Straza does not know and a call that names no tool. The default is allow under standalone and deny under enterprise."},
	{Path: "governance.auditBackpressure", NoEnvReason: noEnvSecurityPosture, Doc: "config-governance", Note: "What strazad does with server audit records while the database cannot take them, and when its in-memory queue of 4,096 records is full. block, the enterprise default, keeps each record in the queue and tries it again until the database answers, so during an outage the queue fills after 4,096 server decisions, about 7 minutes at 10 decisions per second or 41 seconds at 100. After that each new server decision waits up to 25 seconds for room and is then refused with a reason, so nothing runs without its audit record. The queued records are written once the database is back, unless strazad stops or crashes first. drop-with-counter, the standalone default, keeps deciding: it loses a record the database cannot take after four attempts and counts it in straza_audit_lost_total, and it drops a record the full queue cannot take and counts it in straza_audit_dropped_total."},
	{Path: "governance.minAttestation", Env: "STRAZA_MIN_ATTESTATION", Doc: "config-governance", Note: "The lowest attestation a check-in needs to get a session: none, advisory or managed, where managed needs hashes that match the registered ones. The default is none under standalone and managed under enterprise."},
	{Path: "governance.deviceTokenTTL", Env: "STRAZA_DEVICE_TOKEN_TTL", Doc: "config-governance", Note: "The lifetime of the device credential a machine keeps after enrollment, 720h by default and renewed at a check-in past half its life; strazad warns at boot when it is shorter than twice sessionMaxLifetime plus seven minutes, because a session that runs its full lifetime would then end with an expired credential and a locked-out machine."},
	{Path: "governance.sessionMaxLifetime", Env: "STRAZA_SESSION_MAX_LIFETIME", Doc: "config-governance", Note: "The longest a session stays active from its start, 12h by default, after which the janitor closes it and the client starts a new one from its device credential without any human action."},
	{Path: "governance.captureRetention", Env: "STRAZA_CAPTURE_RETENTION", Doc: "config-governance", Note: "How long recorded conversation turns are kept before the janitor deletes them. The default is 720h, which is 30 days."},
	{Path: "governance.transcriptBytesWatermark", Env: "STRAZA_TRANSCRIPT_BYTES_WATERMARK", Doc: "config-governance", Note: "The size of the stored transcripts in bytes above which the janitor logs a warning on every pass, so a filling disk shows early. The default is 10 GiB."},
	{Path: "governance.auditIngestBacklogLimit", Env: "STRAZA_AUDIT_INGEST_BACKLOG_LIMIT", Doc: "config-governance", Note: "While this many audit records wait to be published, strazad answers a client's audit upload with 429, and the client keeps the records and retries. 0 turns the limit off. The default is 50000."},
	{Path: "governance.auditIngestPerSessionRPS", Env: "STRAZA_AUDIT_INGEST_PER_SESSION_RPS", Doc: "config-governance", Note: "Audit uploads per second one session may make at one strazad, and 0 turns the limit off. The default is 5."},
	{Path: "governance.outboxBulkRetention", Env: "STRAZA_OUTBOX_BULK_RETENTION", Doc: "config-governance", Note: "How long an audit or capture record stays in the outbox table once it is published, since the audit chain and the read models hold it. The default is 48h."},
	{Path: "governance.sentinel.enabled", NoEnvReason: fileOnlyTuning, Doc: "config-governance", Note: "Turns on the audit sentinel, which reads the audit stream and records a warning or critical verdict on patterns such as a burst of denies or a written file that is then run. It alerts only and blocks nothing. The default is false in both profiles."},
	{Path: "governance.sentinel.denyBurstWarn", NoEnvReason: withSentinel, Doc: "config-governance", Note: "How many denies within denyBurstWindow raise a warning verdict. The default is 5."},
	{Path: "governance.sentinel.denyBurstCritical", NoEnvReason: withSentinel, Doc: "config-governance", Note: "How many denies within denyBurstWindow raise a critical verdict, at least denyBurstWarn. The default is 10."},
	{Path: "governance.sentinel.denyBurstWindow", NoEnvReason: withSentinel, Doc: "config-governance", Note: "The window the deny counts are taken over. The default is 1m."},
	{Path: "governance.sentinel.variantWindow", NoEnvReason: withSentinel, Doc: "config-governance", Note: "How long a denied shell command is remembered, so that a variant of it raises a verdict. The default is 10m."},
	{Path: "governance.sentinel.writeExecWindow", NoEnvReason: withSentinel, Doc: "config-governance", Note: "How long a written path is remembered, so that running it raises a verdict. The default is 30m."},
	{Path: "governance.sentinel.baselineMinEvents", NoEnvReason: withSentinel, Doc: "config-governance", Note: "How many audit events a user needs before their usual mix of tools counts as a baseline. The default is 50."},

	{Path: "approval.retention", Env: "STRAZA_APPROVAL_RETENTION", Doc: "config-approval", Note: "How long a decided or expired approval record is kept before the janitor deletes it. The default is 720h, which is 30 days."},
	{Path: "approval.gatewayHoldSeconds", Env: "STRAZA_APPROVAL_GATEWAY_HOLD_SECONDS", Doc: "config-approval", Note: "How many seconds a call that needs approval holds the gateway's connection open for a decision before it answers that the decision is pending. It never lengthens the rule's decision window, so lower it when your clients time out sooner. The default is 120."},
	{Path: "approval.unsignedOwnDecisions", NoEnvReason: noEnvSecurityPosture, Doc: "config-approval", Note: "Lets a person decide their own request from the console, strazactl and Slack, which carry no device signature. False, the default, accepts such a decision only from an enrolled phone or browser. True means an agent that runs on that person's machine can approve its own calls."},
	{Path: "approval.channels.slack.enabled", Env: "STRAZA_APPROVAL_SLACK_ENABLED", Doc: "config-approval", Note: "Posts approval requests to Slack, which then needs channel, a bot token and a signing secret. The default is false."},
	{Path: "approval.channels.slack.botToken", Env: "STRAZA_APPROVAL_SLACK_BOT_TOKEN", Doc: "config-approval", Note: "The Slack bot token, set here or in botTokenFile but not both. Empty by default."},
	{Path: "approval.channels.slack.botTokenFile", Env: "STRAZA_APPROVAL_SLACK_BOT_TOKEN_FILE", Doc: "config-approval", Note: "Path of a file that holds the Slack bot token, the better form because a token in an environment variable can be read by anyone who can inspect the process. Empty by default."},
	{Path: "approval.channels.slack.signingSecret", Env: "STRAZA_APPROVAL_SLACK_SIGNING_SECRET", Doc: "config-approval", Note: "The Slack signing secret that proves a request came from Slack, set here or in signingSecretFile but not both. Empty by default."},
	{Path: "approval.channels.slack.signingSecretFile", Env: "STRAZA_APPROVAL_SLACK_SIGNING_SECRET_FILE", Doc: "config-approval", Note: "Path of a file that holds the Slack signing secret, the better form for the same reason. Empty by default."},
	{Path: "approval.channels.slack.channel", Env: "STRAZA_APPROVAL_SLACK_CHANNEL", Doc: "config-approval", Note: "The id of the Slack channel requests are posted to, required when Slack is enabled."},
	{Path: "approval.channels.slack.includeJustification", NoEnvReason: "Set in the file only, because it sends the agent's justification to Slack.", Doc: "config-approval", Note: "Adds the agent's justification to the Slack message, while the console always shows it. The default is false."},
	{Path: "approval.push.fcm.enabled", Env: "STRAZA_APPROVAL_PUSH_FCM_ENABLED", Doc: "config-approval", Note: "Turns on the direct Firebase sender for Android phones; it needs serviceAccountFile and projectId, refuses boot beside the relay, and is off by default."},
	{Path: "approval.push.fcm.serviceAccountFile", Env: "STRAZA_APPROVAL_PUSH_FCM_SERVICE_ACCOUNT_FILE", Doc: "config-approval", Note: "Path of the Firebase service account file that signs every send, required when fcm.enabled is true and empty by default."},
	{Path: "approval.push.fcm.projectId", Env: "STRAZA_APPROVAL_PUSH_FCM_PROJECT_ID", Doc: "config-approval", Note: "The Firebase project id that names the send endpoint and is part of the app config the phone receives, required when fcm.enabled is true or any of appId, apiKey and senderId is set, and empty by default."},
	{Path: "approval.push.fcm.appId", Env: "STRAZA_APPROVAL_PUSH_FCM_APP_ID", Doc: "config-approval", Note: "The Firebase Android app id (mobilesdk_app_id in google-services.json) handed to enrolling phones, a public identifier that travels with apiKey, senderId and projectId as one all-or-nothing set, empty by default."},
	{Path: "approval.push.fcm.apiKey", Env: "STRAZA_APPROVAL_PUSH_FCM_API_KEY", Doc: "config-approval", Note: "Firebase's web API key (current_key in google-services.json) handed to enrolling phones, a public identifier in the same all-or-nothing set, empty by default."},
	{Path: "approval.push.fcm.senderId", Env: "STRAZA_APPROVAL_PUSH_FCM_SENDER_ID", Doc: "config-approval", Note: "The sender id of the Firebase project (project_number in google-services.json) handed to enrolling phones, a public identifier in the same all-or-nothing set, empty by default."},
	{Path: "approval.push.webpush.vapidKeyFile", Env: "STRAZA_APPROVAL_PUSH_WEBPUSH_VAPID_KEY_FILE", Doc: "config-approval", Note: "Path of the VAPID private key, the one setting that turns WebPush on: when the path is set and the file is absent strazad mints the key there at boot, the key is the deployment's push identity, and empty (the default) leaves WebPush off."},
	{Path: "approval.push.webpush.contact", Env: "STRAZA_APPROVAL_PUSH_WEBPUSH_CONTACT", Doc: "config-approval", Note: "A mailto: or https: address the push services may use to reach the operator, optional, valid only beside vapidKeyFile, and empty by default."},
	{Path: "approval.push.apns.keyFile", Env: "STRAZA_APPROVAL_PUSH_APNS_KEY_FILE", Doc: "config-approval", Note: "Path of the Apple-issued .p8 auth key, the one setting that turns on sending straight to APNs: strazad never mints it, refuses boot when the file is unreadable or the relay is on, and empty (the default) leaves it off."},
	{Path: "approval.push.apns.keyId", Env: "STRAZA_APPROVAL_PUSH_APNS_KEY_ID", Doc: "config-approval", Note: "The ten-character id of the key, shown beside it in the Apple developer portal, required when keyFile is set."},
	{Path: "approval.push.apns.teamId", Env: "STRAZA_APPROVAL_PUSH_APNS_TEAM_ID", Doc: "config-approval", Note: "Your Apple developer team id, the team the key belongs to, required when keyFile is set."},
	{Path: "approval.push.apns.topic", Env: "STRAZA_APPROVAL_PUSH_APNS_TOPIC", Doc: "config-approval", Note: "The bundle id of the approver app build you sign, sent as the apns-topic header, required when keyFile is set."},
	{Path: "approval.push.apns.environment", Env: "STRAZA_APPROVAL_PUSH_APNS_ENVIRONMENT", Doc: "config-approval", Note: "Which APNs cluster to send to: production (the default, right for every TestFlight and App Store build) or sandbox for a build signed with a development profile, and a mismatch fails every send as BadDeviceToken."},
	{Path: "approval.push.relay.enabled", Env: "STRAZA_APPROVAL_PUSH_RELAY_ENABLED", Doc: "config-approval", Note: "Turns on the hosted push relay, which sends to iOS and Android phones with no Apple or Firebase account on your side; it refuses boot beside a direct FCM or APNs sender and is off by default."},
	{Path: "approval.push.relay.url", Env: "STRAZA_APPROVAL_PUSH_RELAY_URL", Doc: "config-approval", Note: "The relay to register with and send through; empty (the default) means the Straza-operated relay at https://push.straza.ai."},
	{Path: "approval.push.relay.tokenFile", Env: "STRAZA_APPROVAL_PUSH_RELAY_TOKEN_FILE", Doc: "config-approval", Note: "Path of the anonymous deployment token the relay hands out, required when the relay is enabled; strazad mints it there at first boot and mints it again once if the relay refuses it."},
	{Path: "approval.push.allowedPushHosts", Env: "STRAZA_APPROVAL_PUSH_ALLOWED_HOSTS", Doc: "config-approval", Note: "The hosts a UnifiedPush or WebPush endpoint may point at, checked when a phone or browser registers and again at every send; empty (the default) refuses every such registration."},
	{Path: "approval.push.ticketReminderBefore", Env: "STRAZA_APPROVAL_PUSH_TICKET_REMINDER_BEFORE", Doc: "config-approval", Note: "How long before a pending ticket expires the single reminder is sent; empty means the built-in 2h default, and a ticket whose whole window is shorter gets no reminder."},
	{Path: "approval.preview.enabled", Env: "STRAZA_APPROVAL_PREVIEW_ENABLED", Doc: "config-approval", Note: "Shows a redacted preview of the call's arguments on every approval surface. Redaction always applies, and false computes and stores no preview at all. The default is true."},

	{Path: "apps.dir", Env: "STRAZA_APPS_DIR", Doc: "config-apps", Note: "The watched apps directory: a manifest added or changed there becomes a draft that a person publishes, and a removed one becomes a removal draft. The default is `<dataDir>/apps`."},
	{Path: "apps.pollInterval", NoEnvReason: fileOnlyTuning, Doc: "config-apps", Note: "How often strazad scans the apps directory. The default is 1s."},
	{Path: "apps.healthInterval", NoEnvReason: fileOnlyTuning, Doc: "config-apps", Note: "How often strazad checks each server's health and compares its tools with the last inventory. The default is 20s."},
	{Path: "apps.upstreamTimeout", NoEnvReason: fileOnlyTuning, Doc: "config-apps", Note: "The longest one tool call to a server may take, unless the server's manifest sets limits.timeoutSeconds. The default is 30s."},
	{Path: "apps.allowLoopbackUpstreams", Env: "STRAZA_APPS_ALLOW_LOOPBACK_UPSTREAMS", Doc: "config-apps", Note: "Lets strazad dial a remote server published at a loopback address such as 127.0.0.1, which reaches strazad's own host: true by default under the standalone profile and false under enterprise, while unspecified, link-local and cloud metadata addresses are refused whatever it says and private addresses always pass."},
	{Path: "apps.catalog.policyFilter", NoEnvReason: fileOnlyTuning, Doc: "config-apps", Note: "Leaves a tool that policy denies out of a session's tool list, instead of listing it and denying the call. The default is true."},
	{Path: "apps.catalog.warnSize", NoEnvReason: fileOnlyTuning, Doc: "config-apps", Note: "When a role's catalog holds more tools than this, strazad logs a warning and counts it in straza_gateway_catalog_oversize_total, and -1 turns the check off. The default is 100."},
	{Path: "apps.catalog.pageSize", NoEnvReason: fileOnlyTuning, Doc: "config-apps", Note: "The most tools one tools/list answer carries, the client following a cursor for the rest, and 0 sends the whole catalog at once. The default is 200."},

	{Path: "secrets.kekFile", Env: "STRAZA_SECRETS_KEK_FILE", Doc: "config-core", Note: "The file that holds the 32-byte key-encryption key every stored secret is sealed with. strazad creates it at first boot when it is absent, and a backup needs it beside the store. The default is `<dataDir>/secret.key`."},

	// Tombstones (unified role model, spec/scim-profile rev 12): the fields
	// exist only so validation can refuse them with a teaching error.
	{Path: "scim.groupRolePrefix", NoEnvReason: removedSCIMKey, Doc: "config-reference"},
	{Path: "scim.groupRoleMap", NoEnvReason: removedSCIMKey, Doc: "config-reference"},
	{Path: "scim.autoCreateRoles", NoEnvReason: removedSCIMKey, Doc: "config-reference"},

	{Path: "admin.roleAreas", NoEnvReason: fileOnlyTuning, Doc: "config-reference", Note: "Maps a role name to the admin areas its holders administer, written as admin API token grants such as audit:read. Your identity manager decides who holds the role, and a role the map leaves out grants nothing. straza-admin, straza-global-mcp-admin and straza-draft-config cannot appear here, and full cannot be granted. [Delegated admin]({{< relref \"guides/operate/delegated-admin.md\" >}}) shows it. Empty by default."},
	{Path: "admin.secondPerson", NoEnvReason: noEnvSecurityPosture, Doc: "config-reference", Note: "Makes a second person publish every change that widens access: when true, a person who wrote a revision of a draft other than a Check again that only took live values, who minted an admin API token that wrote one, or who sponsors an agent that wrote one, cannot publish it while its check lists a risk, and a direct admin write that widens access is refused until it goes through a draft. The drafts publish route and the direct admin routes enforce it. False, the default in both profiles, lets the author publish. It does not cover assignments, which stay immediate, or a credential of the person that an agent can read on that machine, and [Drafts and publishing]({{< relref \"guides/changes/who-may-draft.md\" >}}) explains both."},

	{Path: "capture.bodyStore.type", NoEnvReason: "Set in the file only. Only the four credential keys below have variables.", Doc: "config-reference", Note: "Where transcript bodies are kept. Empty, the default, keeps them in the database, and s3 sends them to an S3-compatible bucket, which only the enterprise profile accepts."},
	{Path: "capture.bodyStore.endpoint", NoEnvReason: withBodyStore, Doc: "config-reference", Note: "The host and port of the S3-compatible service, required with type s3."},
	{Path: "capture.bodyStore.bucket", NoEnvReason: withBodyStore, Doc: "config-reference", Note: "The bucket the bodies go to, required with type s3."},
	{Path: "capture.bodyStore.prefix", NoEnvReason: withBodyStore, Doc: "config-reference", Note: "A key prefix inside the bucket. Empty by default."},
	{Path: "capture.bodyStore.region", NoEnvReason: withBodyStore, Doc: "config-reference", Note: "The region of the bucket. Empty by default."},
	{Path: "capture.bodyStore.accessKey", Env: "STRAZA_CAPTURE_BODYSTORE_ACCESS_KEY", Doc: "config-reference", Note: "The access key of the bucket's account. accessKeyFile wins when both are set."},
	{Path: "capture.bodyStore.accessKeyFile", Env: "STRAZA_CAPTURE_BODYSTORE_ACCESS_KEY_FILE", Doc: "config-reference", Note: "Path of a file that holds the access key, preferred over accessKey."},
	{Path: "capture.bodyStore.secretKey", Env: "STRAZA_CAPTURE_BODYSTORE_SECRET_KEY", Doc: "config-reference", Note: "The secret key of the bucket's account. secretKeyFile wins when both are set."},
	{Path: "capture.bodyStore.secretKeyFile", Env: "STRAZA_CAPTURE_BODYSTORE_SECRET_KEY_FILE", Doc: "config-reference", Note: "Path of a file that holds the secret key, preferred over secretKey."},
	{Path: "capture.bodyStore.disableSSL", NoEnvReason: withBodyStore, Doc: "config-reference", Note: "Turns off TLS to the endpoint, for a service inside a private network. The default is false."},

	{Path: "sinks", NoEnvReason: fileOnlyTuning, Doc: "config-sinks", Note: "A list of sinks, each with name, type (webhook or file), url, secret or secretFile, headers, path, subjects and batch. [Sinks and SIEM]({{< relref \"guides/audit/sinks-and-siem.md\" >}}) shows them."},
}
