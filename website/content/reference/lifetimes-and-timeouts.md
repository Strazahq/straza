---
title: Lifetimes and timeouts
description: How long each token, credential, session, approval and record lasts, the setting that changes it, and what happens when it runs out.
pagetype: reference
weight: 6
draft: false
keywords: lifetime timeout ttl expiry session token sign-in deadline device credential offline grace push stream hold ticket retention janitor git grant
---



Each row says how long one thing lasts and what happens when it runs out. The small line under a row's name is the setting that changes it, and a row without one is fixed in the code. A dotted key such as `governance.deviceTokenTTL` lives in the strazad configuration file, and the [configuration reference]({{< relref "reference/configuration.md" >}}) gives its environment variable. A key that starts with `approve.` belongs on a policy rule, as the [PolicySet grammar]({{< relref "reference/policyset-grammar.md#approve" >}}) shows.

## Sign-in and enrollment


| What | Default | When it runs out |
|---|---|---|
| A browser sign-in of `straza enroll` | 5 minutes | The command stops with "The browser sign-in did not finish within 5 minutes." and closes the port it waited on. Run it again and sign in on the page it opens. |
| The authorization code of a browser sign-in on Straza's own page | 60 seconds, used once | The program that asked for it redeems it as soon as the browser returns, so a late or repeated redemption is refused and the sign-in starts again. Under the enterprise profile your identity provider sets this time. |
| How old a browser sign-in may be when it enrolls a person's machine | 10 minutes, and up to 1 minute ahead of strazad's clock | strazad refuses the enrollment with "more than 10 minutes ago", or with "ahead of the Straza server's clock". Run `straza enroll` again. Fixed in the binary. |
| The one-time code of a sign-in with `--use-device-code` on Straza's own page | 10 minutes, polled every 2 seconds and 5 seconds slower after each slow_down answer | `straza enroll` stops with "the sign-in code expired before it was approved". Run it again and approve the new code. Under the enterprise profile your identity provider sets this time. |
| The ID token from Straza's own sign-in, for a person or for an AI agent's key | 10 minutes | A person's token is used once, to enroll, and is not kept. `straza service`, the Straza background service, keeps an AI agent's token until 30 seconds before it expires and checks the agent in with it at each session start in that time. Under the enterprise profile your identity provider sets the lifetime of the ID token. |
| The sign-in deadline of a key enrollment <span class="knob">governance.signInMaxAge</span> | 7 days, `168h`, in both profiles | It counts from the sign-in at the identity provider that the key enrollment used, the ID token's `auth_time`, or from the enrollment when the token carries no sign-in time, and use never renews it. Whether that sign-in makes the person authenticate again is the identity provider's own session policy, so when you lower this setting for an AAL2 policy, set the identity provider's reauthentication period to match. strazad refuses the next key check-in and the next refresh of a key-bound session with "Your last sign-in on this machine was more than 7 days ago", naming the configured period, so a session ends within one token lifetime, 300 seconds, of the deadline, even inside its 12 hours. The person signs in again with `straza enroll`. Every person's machine that `straza service` enrolls has this deadline. |
| A bearer device credential <span class="knob">governance.deviceTokenTTL</span> | 30 days, `720h` | No client holds one. strazad refuses strazactl's sign-in and every bearer device credential, and the straza client enrolls with a key. The setting stays so that a configuration that sets it still starts. |
| The signed assertion an AI agent sends with its own key | At most 5 minutes, used once | `straza service` signs a fresh one each time it needs a new ID token for the agent. strazad refuses one that lives longer or comes a second time. |
| Admin API token | The lifetime picked at minting: in the console 1 day, 7 days, 30 days, 90 days, which it picks by default, 1 year or never, and with `strazactl api-token create --ttl` any duration, or never without the flag | Every request with it is refused. Mint a new token in the console. |

## Sessions and the client


| What | Default | When it runs out |
|---|---|---|
| Session token | 300 seconds | `straza service` renews it when a call arrives with 120 seconds or less left, and the console renews its own once 60 seconds are left. A session that no call uses lapses at its expiry, and the window's next call checks in again with the machine's key. `straza service` keeps a window for seven days past its expiry, and drops the oldest unused one sooner when its user holds 256, so after that only a session start in it checks in again. A window the kill switch revoked keeps its refusal while it is kept, unless the harness starts a session in it again. Every verifier accepts a token for 30 seconds past its expiry, to allow for clock skew. |
| An idle session | 600 seconds with no check-in or renewal | The session janitor, which runs once a minute, closes it and writes an audit record of the session's end with the outcome `idle-closed`. The window's next call checks in again. |
| Session lifetime <span class="knob">governance.sessionMaxLifetime</span> | 12 hours | The janitor closes the session even while it renews, and the audit record of its end has the outcome `lifetime-closed`. `straza service` starts a new session with the machine's key, with no person acting. The console and the self-service page sign you out. |
| Offline grace <span class="knob">governance.offlineGraceTTL</span> | 15 minutes under standalone, none under enterprise | A client that cannot reach strazad keeps deciding from its last signed policy for this long after its session token expired. Then it denies every governed call with `session token expired` and `the offline grace period` in the reason. [The comparison]({{< relref "reference/standalone-and-enterprise.md#offline" >}}) shows both profiles. |
| A revoke reaching a session whose push stream is down | At the first call that renews the session, once 120 seconds or less of its token remain, so at most about 180 seconds after the token was minted for a session that keeps working | The refused renewal marks the session revoked with the server's sentence, and the call denies. A stream that reconnects and is refused renews at once, and a session that makes no call decides nothing meanwhile. Under standalone, while strazad cannot be reached at all, the offline grace adds its time. While `straza service` holds the session's push stream, the revoke arrives at once by push. |
| The nonce in a DPoP proof | A new nonce every 60 seconds, and strazad accepts the current and the previous one | strazad answers 401 with a fresh nonce in the `DPoP-Nonce` header and writes no audit record, and `straza service` signs the request again once. A proof id is refused a second time for 3 minutes, on every replica for a key enrollment or check-in and on the same replica for a request with a session token. Every request the service sends carries a proof. |
| The push stream `straza service` holds for a session | Open while the session's token is live | The service opens it again after 1 second when it drops, and doubles that wait up to 30 seconds while it keeps failing. A session whose token lapses closes its stream, and the next renewal opens it again. |
| A new policy reaching a live session <span class="knob">snapshotLagSeconds in the managed config.yaml</span> | At once by the policy push on the session's stream. Without the push, a session that keeps working picks it up within about one call plus `snapshotLagSeconds`, 30 seconds by default, and an idle one at its first call back when that call renews the session, which it does once 120 seconds or less of its token remain, and otherwise from the call after it | After a decision, `straza service` asks the server for a newer snapshot at most once every `snapshotLagSeconds`. It fetches the snapshot that the push, that check or the renewal names and verifies it before the session uses it. A session that keeps working renews about 3 minutes after its last token was minted. A fetch that failed is not tried again for that session for 30 seconds, or for `snapshotLagSeconds` when the managed config sets it. |
| The service's question to strazad, for a server-checked rule or an approval on a local tool | 2 seconds, and after a 401 one renewal of at most 3 seconds and a second question of 2 seconds | The call is denied. For a server-checked rule the reason starts "Straza: security layer unreachable for a server-checked action". |

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
| The enroll token for a phone or browser | 10 minutes, used once | The device cannot pair with it. The person mints a new one with **Add a phone** on their self-service page, or in the console for an admin's own phone. |
| A signed decision from a phone or browser | The challenge lasts 5 minutes, and the device clock must be within 5 minutes of strazad | The decision is refused. |
| A button press in Slack | Its signed time must be within 5 minutes of strazad | The press is refused, so a replayed request does nothing. |
| Approval records <span class="knob">approval.retention</span> | 30 days | The janitor, which runs once an hour, deletes decided and expired records older than this. |

## Git grants {#git}


| What | Default | When it runs out |
|---|---|---|
| A git grant | 120 seconds from its issue, fixed in the binary | `straza service` holds it in memory for the git command that asked for it, never gives it to git, and drops it at its expiry. A fetch grant opens every request of one fetch, and a push grant that names updates is spent at the push. A resource server refuses a grant that claims a lifetime over 300 seconds, and one that arrives 60 seconds or more after its expiry. The next git command asks for a new grant. |
| The wait while an approval on a git grant is open <span class="knob">approve.timeoutSeconds</span> | The rule's decision window, 90 seconds by default | git waits the whole window, with a wait line at once and then every 5 seconds. When the window ends with no decision, git stops with "Straza: approval request expired after 90s with no decision", followed by the request's reference. A ticket rule keeps no git command waiting: the command stops at once, and runs on the approval when the person runs it again. |
| One request for a git grant while an approval is open <span class="knob">approval.gatewayHoldSeconds</span> | The first request holds nothing, and each later one holds at most 5 seconds, never longer than this setting | `straza service` asks again at once with the same request, until the approval is decided or its window ends. |
| A git command's question whether Straza serves its repository | 30 seconds | git stops with "Straza: `straza service` did not answer in time, so this git command did not run. Run `straza doctor` to see why." |
| A git host a server's git block adds or drops, reaching a machine | At the next session start or session renewal on that machine. A session that keeps working renews about 3 minutes after its last token was minted | `straza service` rewrites the machine's host file from the server's answer, so git hands that host to Straza or stops doing so. |

## Changes and drafts


| What | Default | When it runs out |
|---|---|---|
| A draft an AI agent submits | 14 days after its latest revision | The draft closes as expired, and the audit record carries the action `draft.expire`. A draft from the console, strazactl or the apps directory does not expire. |
| The check of a new draft revision | Every 30 seconds, and at once when an agent submits | A revision that nobody checked yet gets its check on the next pass. |
| The scan of the apps directory <span class="knob">apps.pollInterval</span> | Every second | A manifest added or changed in the directory becomes a draft for a person to publish. |

## MCP servers


| What | Default | When it runs out |
|---|---|---|
| One tool call to an MCP server <span class="knob">limits.timeoutSeconds in the manifest, else apps.upstreamTimeout</span> | 30 seconds | The call fails with an error that says the server did not answer within the time, that the call may have taken effect, and to check whether it did before trying once more. Straza does not send it again. |
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
| The previous session signing key, after a rotation | The new key signs about 60 seconds after the rotation, and the previous key verifies for 30 days more, or for `governance.deviceTokenTTL` when that is longer | The previous key stops verifying once every credential it signed has expired. [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md" >}}) describes the rotation. |
| The previous client assertion key, after a rotation | The new key signs about 60 seconds after the rotation, and the previous key leaves the key document about 60 seconds after that | Your identity provider no longer finds the previous key in the document. |
| An admin change | strazactl waits 30 seconds, and strazad stops a write after 5 minutes | strazactl says the change may still be running and how to check it. strazad answers that the change did not finish and may or may not have been applied. A session or device revoke that runs past the 5 minutes gets that same answer, and its write either committed whole or changed nothing, so read the session or device back before you run it again. |
| The push stream to a client | A heartbeat every 25 seconds | A proxy in front of strazad must keep an idle stream open for longer than this. |
| An idle keep-alive connection | 120 seconds | strazad closes it. The listener sets no write timeout, because held calls and event streams stay open on purpose. |
| Reading a request's header and body | 30 seconds, and 4 minutes for the body of `/mcp`, `/mcp/{server}` and `/v1/audit/batch` once the session token verified | strazad stops reading and drops the request, so a client that stalls mid-body no longer holds the connection. An MCP request whose body came too slowly gets a JSON-RPC error that says Straza cut it off. Once the body is read the bound ends, so held calls and event streams run on. |
