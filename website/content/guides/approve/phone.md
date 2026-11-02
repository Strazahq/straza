---
title: Approve on a phone
description: A person enrolls their own phone as an approver device on the self-service page, decides a held call in the Straza approver app, and signs each answer with its own key.
pagetype: how-to
weight: 30
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server in a Linux container. A shell script with an openssl P-256 key stood in for the phone. It paired with an enroll token from strazactl, fetched the pending request and signed the decision against the approver listener on port 8443, and the call it decided was a real hold that dana's hook call raised. The QR scan and the tap in the app on a real phone were not run, and the console form was not walked in a browser. The enroll token of that run came from strazactl, which no longer mints one. The role step and the person's own Add a phone were written from the code on 2026-11-01 and not walked
  date: 2026-10-06
applies_to: both
keywords: phone approver enroll push signature
who: The person who decides on their phone, after you as the admin give them the role
where: The self-service page, the console or a terminal with strazactl, and the Straza approver app on the person's phone
steps: true
modes: [console, cli]
mode_default: console
---


A person enrolls their own phone so they answer held calls in the Straza approver app. Each answer is signed by a key the phone keeps in its secure hardware. By the end of this page a phone is enrolled as an approver device, has decided a held call, and shows in the list of approver devices.


The person mints a one-time enroll token on the self-service page and scans it with the app. No admin route or console screen enrolls a phone for someone else. The self-service page enrolls a phone for whoever is signed in as the person. On a standalone server Straza checks the passwords itself, so anyone who may set the person's password can also add a phone in their name, as [Known limits]({{< relref "security/known-limits.md" >}}) says. Your part as the admin is to give the person the role that lets them enroll. From then on the phone holds a signing key the server knows. When a rule holds a call, Straza sends the request to the phone, the app fetches it over its own connection to the server, and a tap signs the verdict against a challenge that works once. The server checks who decided, never where from, so the phone can sit on any network.

## Before you start {.nostep}


- An admin's console sign-in, or `strazactl` on an admin API token minted in the console as [Mint an admin API token]({{< relref "guides/console/settings.md#mint-an-admin-api-token" >}}) shows. You need it to give the role and to see and revoke devices.
- A person a rule names as a decider, such as the person behind an agent or a holder of an approver role, with an account at the identity provider your server signs in with.
- The Straza approver app on that person's phone, from the [App Store](https://apps.apple.com/app/straza-approver/id6798735074) or [Google Play](https://play.google.com/store/apps/details?id=ai.straza.approver).
- A server address the phone can open. [Make the address reachable from the phone]({{< relref "guides/approve/push-and-connectivity.md#make-the-address-reachable-from-the-phone" >}}) covers how.
- A pending request to decide, such as the one [Hold a call for a person]({{< relref "guides/write-policy/hold-for-a-human.md" >}}) raises.

Push delivery is recommended, so the phone learns of a request while the app is closed. Without push, the app sees a request only while it is open. [Choose how push reaches the phone]({{< relref "guides/approve/push-and-connectivity.md#choose-how-push-reaches-the-phone" >}}) sets it up.

A person without the app can decide from a browser instead, as [Approve in the browser]({{< relref "guides/approve/browser.md" >}}) shows.

## Give the person the phone role


Only a person who holds `straza-enroll-mobile`, or `straza-admin`, may add a phone. When your identity manager feeds Straza over SCIM, assign the role there. Otherwise, as the admin, assign it in the console or from a terminal.

{{< console >}}
{{< clicks "Roles" "straza-enroll-mobile" "Assign to a user" "Assign role" >}}

Pick the person under **User** before you press **Assign role**.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl assign straza-enroll-mobile --user lena
```
{{< /command >}}

{{< see >}}`assigned straza-enroll-mobile to lena`{{< /see >}}
{{< /cli >}}

## Mint the enroll token


The person mints the token on their own self-service page. It works once and expires after ten minutes, so they mint it with their phone in hand.

They open `/self-service/` on the server and sign in, as [Approve in the browser]({{< relref "guides/approve/browser.md#sign-in-on-the-self-service-page" >}}) shows.

{{< clicks "This browser" "Add a phone" >}}

**Add a phone** opens a sheet for the person who is signed in, such as `For you, lena. The phone approves the requests routed to you.` They press **Create the QR code**.

{{< shot name="enroll-qr" caption="The QR code for lena, with the one-time code, the servers the phone tries and how it trusts them." >}}

{{< see >}}The QR under `Scan with the Straza approver app`, with the line `The code expires in` and the time left.{{< /see >}}

Beside the QR, the sheet shows the code itself under `One-time code`. It names the servers the phone tries under `Servers the phone tries, in order`, and how the phone trusts each one under `How the phone trusts it`.

{{< console >}}

As the admin, you add your own phone the same way, or from the console.

{{< clicks "Approvals" "Approver devices" "Add a phone" >}}

The console's sheet is for your own phone only. Under **For someone else** it gives the self-service address with **Copy**, so you can send it to the person who should enroll.
{{< /console >}}

{{< fails >}}
`You do not hold the role straza-enroll-mobile, so you cannot add a phone of your own. Your identity manager assigns it.`
: The console says this to an admin whose account lacks the phone role. Give your own account the role as the step before shows. The self-service page shows **Add a phone** only to an account that may enroll a phone.

`The code expired before a phone scanned it. Create another.`
: The ten minutes ran out. Press **Create another** with the phone in hand.

`This code is too large to draw. In the approver app, choose Enter the code manually and paste the enrollment text below.`
: The QR cannot carry this enrollment. Press **Copy** under **Enrollment text**, and paste the text in the app's **Enter the code manually**.
{{< /fails >}}


The QR carries everything the app needs to trust your server:

- The server list, which names the dedicated approver listener when one exists.
- A pin of that listener's certificate, so the phone verifies that it reached your server without any public certificate authority.
- Your project's identity, so a phone that serves several deployments keeps them apart.

Behind an ingress that terminates TLS with a public certificate, the QR carries no pin. The sheet then says `public certificate` under `How the phone trusts it`, and the phone trusts the system store instead.


The pin is the anchor of trust. Replacing the approver listener's certificate strands every enrolled phone until each one enrolls again, so plan that as a fleet event. [Known limits]({{< relref "security/known-limits.md" >}}) lists it with what reduces it.

## Pair the phone


On the phone, the person opens the Straza approver app, chooses to add a server and scans the QR.

Enrollment makes a P-256 key in the phone's secure hardware. It sends the token and the public key to `/v1/approver/enroll` on the approver listener and receives a device credential. The credential lasts 30 days and renews itself with a signature of the same key, as [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#approvals" >}}) lists.

A failed pairing needs a fresh token. The first attempt that reaches the server with a well-formed key spends it, whatever happens after that. The listener serves only the approver routes, which is why a browser opened at that address sees nothing.


In the example run, a script that speaks the app's contract performed the same exchange. The server answered with the device id `apd_01a1130f-0376-7385-9c96-c1c9252ab7cb`, a credential valid for `2592000` seconds and the project reference. The device credential is a bearer secret and is not shown.

## Decide from the phone


When a rule holds a call, Straza sends the request to the person's phone as a push notification. Its payload is only a version, a reference and a kind, never the command, the arguments or a name. The app fetches the detail itself from your server. While the app is open it also polls the pending queue, at an interval the app sets, so a phone without push still decides.

The app shows the request with the requester, the rule, the tool and the redacted call preview, and a fresh challenge that is valid for one decision. The person approves or denies, and the app signs the request id, the verdict, the challenge and the time with the phone's key.

```text
{"id":"01a1130f-1ad9-7546-a960-0be1759b7fce","expires_at":"2026-10-06T21:16:59Z","requester":{"username":"dana","kind":"human"},"rule_id":"hold-kubectl-apply","set_name":"kubectl-apply-hold","summary":{"tool":"shell.exec"},"challenge":"JMte","class":"hold","args_preview":"kubectl apply -f deploy/app.yaml"}
```

This is the pending row the example device fetched, trimmed to the fields that matter, with the challenge masked to its first four characters. Signing it produced `{"state":"approved"}` from the server, and dana's retry of the held call was allowed.


The server verifies the signature against the enrolled key and refuses a timestamp more than five minutes off. It consumes the challenge, so a replay fails. Then it runs the same checks every surface runs: a fresh check that the device's owner is a decider the request names, the request's self-approval setting, and no AI agent as a decider.

Because the phone signs, it is also where a person confirms a request of their own. In the audit log, the resolution record names the channel `phone` and the deciding device.

```text
#52 [dana] {"data":{"approvalId":"01a1130f-1ad9-7546-a960-0be1759b7fce","channel":"phone","decidedBy":"01a1130b-64c0-7016-9be3-4ad1a237f431","decidedDeviceId":"apd_01a1130f-0376-7385-9c96-c1c9252ab7cb","phase":"resolution","rule":"hold-kubectl-apply","set":"kubectl-apply-hold","state":"approved","summary":"shell.exec: kubectl apply -f deploy/app.yaml"},"type":"straza.audit.approval"}
```

This record is trimmed to the fields that matter here.

## See and revoke devices

{{< console >}}

{{< clicks "Approvals" "Approver devices" >}}

{{< shot name="approver-devices" caption="Approver devices with one enrolled browser, its key and whether it is notified." >}}

The tab lists every enrolled phone and browser under the columns Device, Person, Key, Enrolled, Last seen and Notified. The Key column reads `hardware key` for a key held in the phone's secure hardware, or `software key`. On a phone a software key shows in amber, with the line `the phone's own claim, not attested` under it.

To revoke a lost or retired phone, press **Revoke** on its row, then **Revoke device**.

{{< see >}}`Revoked.`, followed by who can no longer approve from which device.{{< /see >}}
{{< /console >}}

{{< cli >}}

`strazactl approvers list` shows every enrolled device with its owner, key posture and push routes. A device with no push route still decides when the app is open, and the row says so.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl approvers list
```
{{< /command >}}

```table
ID                                        USER   NAME            PLATFORM  KEY       ATTESTATION  PUSH                   ENROLLED             LAST SEEN
apd_01a1130f-0376-7385-9c96-c1c9252ab7cb  alice  stand-in phone  android   software  none         none (never notified)  2026-10-06 21:11:53  2026-10-06 21:11:59
```

The example's device reports a software key with no attestation, which is what a script or a browser produces. A real phone enrolls a key held in its secure hardware.

Revoke a lost or retired phone by its id.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl approvers revoke apd_01a1130f-0376-7385-9c96-c1c9252ab7cb
```
{{< /command >}}

```text
revoked approver device apd_01a1130f-0376-7385-9c96-c1c9252ab7cb: its credential is dead; re-enrolling needs a fresh QR
```
{{< /cli >}}


The credential fails on the device's next call, and its push routes stop. Its person is untouched, and their other devices keep working. To use the phone again, the person enrolls it again on their own self-service page.

## Next {.nostep}

- [Push delivery and connectivity]({{< relref "guides/approve/push-and-connectivity.md" >}}) gives the phone an address it can reach and wakes it for each request.
- [Approve in the browser]({{< relref "guides/approve/browser.md" >}}) lets a person decide from a browser they enabled.
- [Approvals]({{< relref "concepts/approvals-model.md" >}}) explains who may decide which request, and why a person's own request needs a signed device.
