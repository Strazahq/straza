---
title: Slack
description: strazad posts each held call to a Slack channel as a card, and a tap on Approve or Deny there decides it.
pagetype: how-to
weight: 20
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: Not run against a Slack workspace, so the app settings, the card, the taps and the email mapping were read at source. The five configuration refusals and the enabled switch were run at the boot of a throwaway standalone server in a Linux container, where a notify edit was also stored and published with strazactl on an admin API token. The console and slack channel rows were read from the demo stack's channel status route, and their console words at source
  date: 2026-10-06
applies_to: both
keywords: slack approve channel
who: You, as the admin, with admin rights on the Slack workspace
where: The Slack app settings, the strazad configuration and the console
steps: true
modes: [console, cli]
mode_default: console
---


You, as the admin, connect one Slack channel so the people who decide held calls answer them there. strazad posts each held call as a card with Approve and Deny buttons. By the end of this page the channel is configured and checked, and you know what a tap on each button does.

{{< note title="Not run against a Slack workspace" >}}The steps were written from the code and have not been run against a live Slack workspace. Check each one against your workspace the first time.{{< /note >}}


Slack is a notifier with buttons. A tap on a button is a real decision when the person tapping maps to a Straza user the request names as a decider. The card carries no command text beyond the redacted preview, and the agent's own justification stays off Slack unless you opt in. The console keeps working underneath, so a Slack outage costs you a notification, never a decision.

## Before you start {.nostep}

- Admin rights on the Slack workspace.
- Write access to the strazad configuration.
- Deciders whose Slack profile email equals their Straza email, because that is how a tap is attributed.


{{< now title="Check each approver's Slack email first" >}}A Slack account whose profile has no email, or an email that differs from the Straza email your identity manager sends, cannot decide from Slack. The deployment learns about it only from the ephemeral reply and a server log line. Check the mapping for your approvers before the first real request.{{< /now >}}

## Create the Slack app


In Slack's app settings, create an app for your workspace and set three things:

- A bot token with the scopes the code calls: `chat:write` to post and update the card, and `users:read.email` to read the tapping user's email.
- Interactivity, with its request URL set to your server's public address plus `/v1/approval/callbacks/slack`. strazad receives the button taps there.
- The app's signing secret, kept at hand. strazad verifies every callback with it.

## Configure strazad


Add the channel to the server configuration and restart strazad.

```yaml
approval:
  channels:
    slack:
      enabled: true
      channel: C0123456789
      botTokenFile: /etc/straza/slack-bot-token
      signingSecretFile: /etc/straza/slack-signing-secret
      includeJustification: false
```

The small line under each setting is its environment variable.

| Setting | What it sets |
|---|---|
| `approval.channels.slack.enabled`<span class="knob">STRAZA_APPROVAL_SLACK_ENABLED</span> | Turns the channel on. Only `true` and `1` count, so a typo cannot half-enable it. |
| `approval.channels.slack.channel`<span class="knob">STRAZA_APPROVAL_SLACK_CHANNEL</span> | The Slack channel id, not its name. |
| `approval.channels.slack.botTokenFile`<span class="knob">STRAZA_APPROVAL_SLACK_BOT_TOKEN_FILE</span> | The file that holds the bot token. `botToken` takes the value inline instead. |
| `approval.channels.slack.signingSecretFile`<span class="knob">STRAZA_APPROVAL_SLACK_SIGNING_SECRET_FILE</span> | The file that holds the signing secret. `signingSecret` takes the value inline instead. |
| `approval.channels.slack.includeJustification` | Forwards the agent's stated reason to Slack. Off by default. |

`includeJustification` has no environment face on purpose. Forwarding the agent's stated reason to a third party is a privacy decision, so it stays a deliberate file edit.


{{< fails >}}
`approval.channels.slack: channel is required when the Slack channel is enabled`
: Set `channel` to the Slack channel id.

`approval.channels.slack: botToken or botTokenFile is required when enabled`
: Point `botTokenFile` at the file that holds the bot token.

`approval.channels.slack: signingSecret or signingSecretFile is required when enabled`
: Point `signingSecretFile` at the file that holds the app's signing secret.

`approval.channels.slack: set botToken or botTokenFile, not both`
: Keep one form of the secret, the file. The signing secret refuses both forms the same way.
{{< /fails >}}

## Check the channel


{{< only form="console" >}}The channel's status and its test card exist only in the console.{{< /only >}}

{{< clicks "Approvals" "Channels" >}}

{{< shot name="channels-push" caption="The Channels tab on a server where Slack is not configured." >}}

Every channel has a row with its status, the server's own detail line, who it reaches and the last delivery attempt. On a deployment where Slack is off, the two rows read as follows.

```table
CHANNEL   STATUS           DETAIL                                                                  REACHES                            LAST DELIVERY
console   always on        always available: approvers check it themselves, nothing is delivered   everyone with the approvals area   nothing to deliver
slack     not configured   not configured (approval.channels.slack)                                nobody                             never
```

The columns and their words are the console's own, read at source. The console row never carries a test button, because nothing is delivered to it.


A configured Slack row carries **Send a test**, which posts a content-free line through the real delivery path.

{{< see >}}`Straza test notification. The Slack approval channel is wired correctly.` in the channel, and the attempt with its outcome under Last delivery.{{< /see >}}


strazad's Slack client follows a redirect only within the scheme, host and port of slack.com/api, and a post follows only a 307 or 308. Any other redirect fails the post with a sentence that names the address and the redirect, and the row's last delivery shows it.

## Decide from Slack


When a request is raised, strazad posts one card. It carries the header `Approval requested`, the requester and the action, the redacted call parameters when a preview exists, a line that says what an approval binds, and the two buttons. The button values are decision tokens bound to that request and verdict. They expire with the request, so a stale card cannot decide anything.


A person who may decide taps **Approve** or **Deny**, and strazad checks the tap in four steps:

1. strazad verifies the Slack signature and rejects a callback older than five minutes as a replay.
2. It checks the decision token.
3. It asks Slack for the tapping user's email and maps that email to a Straza user.
4. Last, it checks that this user is a decider the request names at that moment, as the person behind the agent or as a holder of one of its approver roles.

Emails are not unique. When several Straza users carry the email, the tap maps to the oldest active person, else to the oldest disabled or locked person, and never to an AI agent or a service account. An email that matches only AI agents or service accounts maps to no user.

Some taps never decide:

- A decision on your own request is refused from Slack, because Slack carries no device signature, unless the operator sets `approval.unsignedOwnDecisions`.
- An AI agent never decides.
- A person whose Straza user is disabled or locked is refused before any decision. The reply names the Straza user the Slack email matched and says why. The refusal writes one `straza.audit.authn` login failure with `via` `slack` that names the person, the request and the reason.

The person tapping sees a short reply in Slack. Every reply after the mapping names the Straza user the email matched.

{{< see >}}`Recorded: approved. Your Slack email matched the Straza user ana.`{{< /see >}}


{{< fails >}}
`Your Slack identity is not linked to a Straza user, so this decision was not recorded.`
: The Slack profile email matches no Straza person. Make it equal the email your identity manager sends Straza.

`This approval button is no longer valid.`
: The card is stale, because its buttons expire with the request. Decide in the console, or wait for the agent's next request.

`This request was already approved by ana in Slack at 2026-09-29 08:32:23 UTC, so this tap recorded nothing new.`
: The request already holds the verdict you tapped. Nothing more is needed.

`Could not record decision:` followed by the reason in plain words
: The reason says what failed. For a disabled or locked Straza user it begins `your Slack email matches the Straza user ana, which is disabled, so it cannot decide a request.` Another person who may approve must decide.
{{< /fails >}}


Once a request resolves on any surface, the card's buttons are replaced by a note with the verdict, the decider and the time, such as `Approved by alice · 14:30`. A request that expires undecided gets the note `Expired` in place of its buttons. A decision made from Slack is recorded under the channel `slack`.

## Choose which rules reach Slack


By default every configured channel announces every held call. A rule narrows that with `approve.notify`, a list of `console`, `slack` and `push`. `notify: [console]` alone keeps a rule off Slack and off phones. [PolicySet grammar]({{< relref "reference/policyset-grammar.md#approve" >}}) lists it with the other keys of `approve`.

{{< console >}}

The rule cards keep `notify` and do not edit it, so change it as text. Open the policy under **Policies**, edit `notify` on its **YAML** tab, and press **Save and publish**.
{{< /console >}}

{{< cli >}}

Edit `notify` in the set's file, then store and publish the set.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl policy apply -f <file>
strazactl policy activate <name>
```
{{< /command >}}
{{< /cli >}}

Routing narrows the announcement only. Who may decide stays with the rule's decider settings. The console, the CLI and an already posted card keep working whatever the list says.

## Undo {.nostep}


Set `enabled: false` and restart strazad. Cards already posted keep their buttons, but a tap no longer decides anything, because strazad stops answering the Slack callback. Decide those requests in the console, as [Approve in the console]({{< relref "guides/approve/console.md" >}}) shows.
