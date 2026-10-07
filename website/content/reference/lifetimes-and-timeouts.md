---
title: Lifetimes and timeouts
description: How long each token, credential, session, approval and record lasts, the setting that changes it, and what happens when it runs out.
pagetype: reference
weight: 6
draft: false
keywords: lifetime timeout ttl expiry session token device credential offline grace hold ticket retention janitor
---



Each row says how long one thing lasts and what happens when it runs out. The small line under a row's name is the setting that changes it, and a row without one is fixed in the code. A dotted key such as `governance.deviceTokenTTL` lives in the strazad configuration file, and the [configuration reference]({{< relref "reference/configuration.md" >}}) gives its environment variable. A key that starts with `approve.` belongs on a policy rule, as the [PolicySet grammar]({{< relref "reference/policyset-grammar.md#approve" >}}) shows.

## Sign-in and enrollment


| What | Default | When it runs out |
|---|---|---|
| The one-time code of a sign-in on Straza's own page | 10 minutes, polled every 2 seconds | `straza enroll` stops with "the device code expired before it was approved". Run it again and approve the new code. Under the enterprise profile your identity provider sets this time. |
| The ID token from Straza's own sign-in, for a person or for an AI agent's key | 10 minutes | The token is used once, to enroll or to check in, and is not presented after that. Under the enterprise profile your identity provider sets the lifetime of a person's ID token. |
| Device credential <span class="knob">governance.deviceTokenTTL</span> | 30 days, `720h` | A device credential lasts 30 days from its issue or its last renewal. A check-in renews it once it is past half that life, so a machine that stops checking in loses it 15 to 30 days after its last check-in. The machine then enrolls again with `straza enroll`. `straza doctor` warns 48 hours before the credential expires, and strazad warns at start when the setting is shorter than twice the sum of the session lifetime and 7 minutes. |
| The credential `strazactl login` keeps <span class="knob">governance.deviceTokenTTL</span> | 30 days, the same as a device credential | strazactl starts new sessions from it and keeps the renewed credential a check-in hands back. Once it is gone, strazactl asks you to run `strazactl login`. |
| The signed assertion an AI agent sends with its own key | At most 5 minutes, used once | straza signs a fresh one at every session start. strazad refuses one that lives longer or comes a second time. |
| Admin API token | Never expires, unless you set a lifetime with `strazactl api-token create --ttl` | Every request with it is refused. Mint a new token. |

## Sessions and the client


| What | Default | When it runs out |
|---|---|---|
| Session token | 300 seconds | The client renews it before then. The daemon renews it on every poll, a hook once it has 120 seconds left, and the console and strazactl once it has 60 seconds left. Every verifier accepts a token for 30 seconds past its expiry, to allow for clock skew. |
| An idle session | 600 seconds with no check-in or renewal | The session janitor, which runs once a minute, closes it and writes an audit record of the session's end with the outcome `idle-closed`. The next session start checks in again. |
| Session lifetime <span class="knob">governance.sessionMaxLifetime</span> | 12 hours | The janitor closes the session even while it renews, and the audit record of its end has the outcome `lifetime-closed`. straza and strazactl start a new session from their credential with no person acting. The console and the self-service page sign you out. |
| Offline grace <span class="knob">governance.offlineGraceTTL</span> | 15 minutes under standalone, none under enterprise | A client that cannot reach strazad keeps deciding from its last signed policy for this long after its session token expired. Then it denies every governed call with `session token expired` and `the offline grace period` in the reason. [The comparison]({{< relref "reference/standalone-and-enterprise.md#offline" >}}) shows both profiles. |
| A revoke reaching a machine with no daemon running | Until the session token expires, at most 300 seconds after it was minted | The hook denies with the server's refusal. Under standalone, while strazad cannot be reached at all, the offline grace adds its time. With `straza daemon` running, the revoke arrives at once by push. |
| The daemon's poll | 30 seconds | Each poll renews the session token. It also carries a revoke when the push stream is down. Set another interval with `straza daemon --poll`. |
| A busy session's check for a newer policy <span class="knob">snapshotLagSeconds in the client's config.yaml</span> | At most once every 30 seconds | After a decision, the client asks strazad whether a newer policy is active, and an unchanged policy costs one short answer. A busy session runs at most one tool call plus this time behind the active policy. |
| A hook's question to strazad, for a server-checked rule or an approval on a local tool | 2 seconds | The call is denied. For a server-checked rule the reason starts "Straza: security layer unreachable for a server-checked action". |

## Approvals


| What | Default | When it runs out |
|---|---|---|
| Decision window of a hold <span class="knob">approve.timeoutSeconds</span> | 90 seconds. A rule may set 1 to 3600. | The request expires and counts as a deny. A held MCP call answers "Straza: approval request expired after 90s with no decision", followed by the request's reference. |
| How long a held MCP call keeps its connection open <span class="knob">approval.gatewayHoldSeconds</span> | 120 seconds, never longer than the decision window | The call answers that the approval is pending, with the seconds left in the window. The agent waits with the `straza__approval_await` tool or sends the call again. |
| How long a hold's approval stays usable <span class="knob">approve.retryTTLSeconds</span> | 60 seconds after the decision. A rule may set 1 to 600. | One identical call by the same person in the same session runs on the approval. After that, the same call asks for a new approval. |
| Decision window of a ticket <span class="knob">approve.ticketTTLSeconds</span> | 24 hours. A rule may set up to 30 days. | The ticket expires and counts as a deny. The next identical call opens a new ticket. |
| A ticket's grant <span class="knob">approve.grantTTLSeconds</span> | 1 hour after the approval. A rule may set up to 24 hours. | One later call that matches the approved one, from any session of the requester, runs on the grant. After that, the call opens a new ticket. |
| A ticket's reminder <span class="knob">approval.push.ticketReminderBefore</span> | 2 hours before the ticket expires | Straza sends one reminder when a channel that sends reminders is configured. A ticket whose whole window is shorter gets none. |
| The expiry check of pending requests | Every 15 seconds | A request past its window becomes expired within 15 seconds. |
| One wait of the `straza__approval_await` tool <span class="knob">max_wait_seconds in the tool call</span> | 30 seconds, at most 60 | The tool answers with the request's state and `timed_out` set to true. The agent calls it again. |
| The credential of a phone or browser that approves | 30 days | The device renews it with a signature of its own key, even after it expired, while the device stays enrolled and its person stays active. |
| The enroll token for a phone or browser | 10 minutes, used once | The device cannot pair with it. Mint a new one with **Add a phone** in the console or with `strazactl approvals enroll-token`. |
| A signed decision from a phone or browser | The challenge lasts 5 minutes, and the device clock must be within 5 minutes of strazad | The decision is refused. |
| A button press in Slack | Its signed time must be within 5 minutes of strazad | The press is refused, so a replayed request does nothing. |
| Approval records <span class="knob">approval.retention</span> | 30 days | The janitor, which runs once an hour, deletes decided and expired records older than this. |

## Changes and drafts


| What | Default | When it runs out |
|---|---|---|
| A draft an AI agent submits | 14 days after its latest revision | The draft closes as expired, and the audit record carries the action `draft.expire`. A draft from the console, strazactl or the apps directory does not expire. |
| The check of a new draft revision | Every 30 seconds, and at once when an agent submits | A revision that nobody checked yet gets its check on the next pass. |
| The scan of the apps directory <span class="knob">apps.pollInterval</span> | Every second | A manifest added or changed in the directory becomes a draft for a person to publish. |

## MCP servers


| What | Default | When it runs out |
|---|---|---|
| One tool call to an MCP server <span class="knob">limits.timeoutSeconds in the manifest, else apps.upstreamTimeout</span> | 30 seconds | The call fails with an error that starts `upstream call failed`. |
| A server's health check <span class="knob">apps.healthInterval</span> | Every 20 seconds | Each check also compares the server's tools with the last inventory. |
| The views a server links | Read again every 10 minutes | A copy the server stops answering for is dropped at most about 20 minutes after its last answer. |
| A person's sign-in at a server's OAuth provider, from Connect to the callback | 10 minutes | The callback is refused. Start connecting again. |
| A person's OAuth token for a server <span class="knob">oauth.refreshWindow, oauth.refreshInterval</span> | Refreshed once it expires within 10 minutes, checked every minute | The refresh worker renews it before it expires. |
| An AI agent's token from your identity provider for a server | Renewed 60 seconds before it expires, and kept for 24 hours at most | Straza fetches a new token from the provider. |

## Audit and retention


| What | Default | When it runs out |
|---|---|---|
| Recorded conversation turns <span class="knob">governance.captureRetention</span> | 30 days | The janitor, which runs once an hour, deletes them. |
| A message on the audit stream <span class="knob">events.auditStreamMaxAge</span> | 60 days, twice the conversation retention | The stream drops it. A sink that stays down longer misses the records that aged out. |
| A published row in the outbox table <span class="knob">governance.outboxBulkRetention</span> | 48 hours | The janitor deletes it, because the audit chain already holds the record. |
| Delivery to a sink | Retries from 1 second, doubling up to 1 minute, for 4,320 attempts, about three days. A refusal that repeating cannot fix gets 3 attempts. | The event is parked in the sink's dead-letter lane. Replay it with `strazactl sinks replay` once the receiver is healthy. |
| The wait for room in a full audit queue <span class="knob">governance.auditBackpressure: block</span> | 25 seconds | The decision is refused with "Straza: this action did not run, because its audit record could not be written while Straza cannot reach its database." |
| The sentinel's deny count <span class="knob">governance.sentinel.denyBurstWindow</span> | Over 1 minute | An older deny no longer counts toward a burst. |
| The sentinel's memory of a denied shell command <span class="knob">governance.sentinel.variantWindow</span> | 10 minutes | A variant of the command no longer raises a verdict. |
| The sentinel's memory of a written file <span class="knob">governance.sentinel.writeExecWindow</span> | 30 minutes | Running the file no longer raises a verdict. |

## Keys and connections


| What | Default | When it runs out |
|---|---|---|
| The previous session signing key, after a rotation | The new key signs about 60 seconds after the rotation, and the previous key verifies for 30 days more, or for the device credential's lifetime when that is longer | The previous key stops verifying once every credential it signed has expired. [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md" >}}) describes the rotation. |
| The previous client assertion key, after a rotation | The new key signs about 60 seconds after the rotation, and the previous key leaves the key document about 60 seconds after that | Your identity provider no longer finds the previous key in the document. |
| An admin change | strazactl waits 30 seconds, and strazad stops a write after 5 minutes | strazactl says the change may still be running and how to check it. strazad answers that the change did not finish and may or may not have been applied. |
| The push stream to a client | A heartbeat every 25 seconds | A proxy in front of strazad must keep an idle stream open for longer than this. |
| An idle keep-alive connection | 120 seconds | strazad closes it. The listener sets no read or write timeout, because held calls and event streams stay open on purpose. |
