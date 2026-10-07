---
title: Sinks and SIEM
description: Send the audit record to a SIEM or a file sink and confirm nothing is lost on the way.
pagetype: how-to
weight: 10
who: You, as the operator who owns the evidence
where: The config file and a terminal
draft: false
aliases:
  - /guides/operate/sinks-and-siem/
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server in an Alpine 3.20 container on Linux, with this page's webhook sink pointed at a stand-in receiver that answers 201 and 409 the way the demo index does, and a file sink beside it, walked through boot, list, the API call, a refusal that parked an event, replay, a rename that backfilled and the undo. The boot line, the list and the API answer pasted here were read from the demo stack's server, v1.1.0-104-g07d2df0e, on a read-only session, and its Elasticsearch was searched read-only. The replay was not run on the demo stack, because it writes there
  date: 2026-10-06
applies_to: both
keywords: sink siem audit export elasticsearch webhook dead-letter replay
---


Forward the audit record to a SIEM or a file, and confirm that nothing was lost on the way. It is for the operator who owns the evidence, and two of its checks need `jq`. A sink is a durable consumer on the event stream that posts each event to a webhook or appends it to a file, and because the consumer keeps its position on the server, an outage at the receiver delays events without dropping them. A sink with no subject list receives every audit and control event except recorded prompts and replies, so adding a sink never ships conversation content off the box by accident. Delivery is at least once, and a receiver that refuses an event for good sees it parked in a dead-letter lane you can replay.

## Add a sink to the config file


Sinks live in the `sinks` list of the config file and have no environment variables, so on Kubernetes they ride the chart's `configYaml` value. The enterprise demo stack posts every audit and revocation event straight into an Elasticsearch index, using the `headers` knob for the content type and the receiver's credentials, and an ingest pipeline on the receiver that makes the CloudEvent id the document id so a redelivery answers 409 instead of creating a duplicate:

```yaml
sinks:
  - name: elastic
    type: webhook
    url: http://elasticsearch:9200/straza-events/_doc?pipeline=straza-id
    subjects: ["straza.audit.>", "straza.revocation.>"]
    headers:
      Content-Type: application/json
      Authorization: "Basic ZWxhc3RpYzpzdHJhemFzaWVt"
```

The Authorization value is the base64 of the demo stack's `elastic:strazasiem`. Put your receiver's own credential there.


The name becomes the durable consumer's name, so renaming a sink restarts it from the beginning of the stream's retention, which is also how you backfill a new receiver. `secret` or `secretFile` adds an HMAC-SHA256 signature over every body, and the file form keeps the key out of the config document. Leave `subjects` out to get the default set, or write `["straza.>"]` to forward the recorded conversation as well, which is one explicit line. `batch: 64` switches a sink to one POST or one fsync per batch, which the default of one event per delivery cannot keep up with at recording-scale volumes. A file sink needs only `type: file` and `path`, and it syncs each line to disk before the event is acknowledged.

## Check the boot line and the list


Every configured sink logs one line at boot with its target and subjects, and a sink that took the default subjects logs that the recorded conversation is excluded:

```text
{"time":"2026-10-06T10:26:38.541285195Z","level":"INFO","msg":"sink configured","name":"elastic","type":"webhook","target":"http://elasticsearch:9200/straza-events/_doc?…","batch":0,"subjects":["straza.audit.>","straza.revocation.>"]}
```


`strazactl sinks list` is the operator's view of each sink since the server booted: what is still behind it, what it parked, what it delivered and the last failure it saw, with the failure's time in UTC.

```sh
strazactl sinks list
```

```table
NAME     TYPE     TARGET                                          BACKLOG  PARKED  DELIVERED  LAST ERROR
elastic  webhook  http://elasticsearch:9200/straza-events/_doc?…  0        0       226        -
```


The same view is `GET /v1/admin/sinks` for a dashboard, with one entry per stream the sink drains, because a subject list that spans the audit stream and the control stream gets one durable consumer on each. `ADMIN_API_TOKEN` holds an admin API token minted with `strazactl api-token create --name docs --scope config:read`:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" http://localhost:8420/v1/admin/sinks | jq -c '.[0] | {name, parked, streams: [.streams[] | {stream, pending, delivered, parked}]}'
```

```text
{"name":"elastic","parked":0,"streams":[{"stream":"STRAZA_AUDIT","pending":0,"delivered":197,"parked":0},{"stream":"STRAZA_EVENTS","pending":0,"delivered":29,"parked":0}]}
```

## Find an event at the receiver


Proof that a sink works is the event at the other end. Search the receiver for an event you know happened, such as the identity event of a kill switch from the [Kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}), by action and user id, and replace the user id below with one from your own record. On the enterprise demo stack, Elasticsearch listens on the loopback address with the demo credentials `elastic` and `strazasiem`, and the search below found the creation of alice's account, which midPoint pushed over SCIM. The search names `data.user.keyword`, the whole id, because a search on `data.user` also matches events whose ids share a part with it:

```sh
curl -s -u elastic:strazasiem 'http://127.0.0.1:9200/straza-events/_search?q=data.action:user.create%20AND%20data.user.keyword:01a0f844-189a-756d-9121-8fa16f63c02d' | jq -c '{total: .hits.total.value, hit: (.hits.hits[0] | {_id, type: ._source.type, time: ._source.time, data: ._source.data})}'
```

```text
{"total":1,"hit":{"_id":"59bab0c9-a2c4-445e-ae3a-00033b523d3b","type":"straza.audit.admin","time":"2026-10-01T16:20:07.452441153Z","data":{"actor":"midpoint","actorId":"52fc38ba-4242-4cdf-a75e-443133027f1a","kind":"human","actorVia":"api-token","origin":"scim","action":"user.create","userType":"human","user":"01a0f844-189a-756d-9121-8fa16f63c02d","target":"01a0f844-189a-756d-9121-8fa16f63c02d","username":"alice"}}}
```

The document id equals the CloudEvent id, which is the dedupe contract every receiver should implement: at-least-once delivery becomes exactly one document.

## Know what happens when the receiver refuses


Delivery has four outcomes. A 2xx acknowledges the event, and so does a 409, because a receiver that already holds the event is the goal. A 5xx, a 408, 425 or 429, a network error or a timeout redelivers with an exponential backoff from one second to one minute, for up to 4320 attempts, which is about three days at the ceiling. Any other 4xx cannot be fixed by repeating the same request, so it gets three attempts. When a budget runs out the event is parked: written to a bounded dead-letter stream of its own, counted, and only then acknowledged at the source, so the spool behind it keeps moving and nothing is dropped. The server logs one warning per ten seconds per sink with the reason and the number of suppressed repeats, and the metric `straza_sink_deadletter_total` counts the parks.


A webhook sink follows a redirect only while it stays on the scheme, host and port of its url and keeps the POST, which a 307 or 308 does. Any other redirect is refused before anything reaches its target. So are a loop of 10 redirects and a Location that is not a valid address. Each counts as a network error, so the event is redelivered, and the server log and `strazactl sinks list` show a sentence that names the address and the redirect. Set the sink's url to the receiver's final address.


Once the receiver is healthy, replay the lane. Each parked event is posted again oldest first and leaves the lane when the receiver accepts it, the call is bounded to 1000 events by default so you run it again while events remain, and the replay stops at the first refusal rather than re-parking or dropping anything. With nothing parked it says so:

```sh
strazactl sinks replay elastic
```

```text
replayed 0 event(s) to sink elastic; 0 remain parked
```

## Verify


A healthy sink shows a backlog near zero and no parked count in `strazactl sinks list`. A growing backlog with a last error means the receiver is refusing or unreachable and the events are waiting on the server, and a parked count carries the words `(replay needed)` on the row so the state is never a footnote. The audit chain itself is verified independently of any sink with `strazactl audit verify`, and what the record contains is explained in [Evidence]({{< relref "concepts/evidence.md" >}}).

## Undo


Removing a sink from the config file and restarting stops delivery and keeps the durable consumer, so adding the same name back resumes where it stopped. A sink removed for good leaves its consumer on the stream. The consumer holds only its position and keeps no event from expiring, so leaving it costs nothing. With your own NATS server you can remove it with the NATS tools: its name is `sink-` followed by the sink's name.
