---
title: Approve in the browser
description: A person who is not an admin sets up their own browser on the self-service page and decides the requests routed to them, each decision signed by a key that never leaves that browser.
summary: >-
  A person who is not an admin decides the held calls routed to them on the self-service page,
  in a browser they set up once. The setup makes a signing key that never leaves that browser,
  so every decision is signed and nobody can decide for them from a copied link. The person
  needs the role `straza-enroll-browser`.
pagetype: how-to
weight: 15
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server on Linux. The approver lena signed in on the self-service page in headless Chromium, enabled the browser and approved alice's held kubectl apply, which her machine then ran through the Claude Code hook. That sign-in was the code sign-in of its time, and the browser sign-in that replaced it was written from the code on 2026-10-22 and not walked. The strip sentence and the approved toast of a hook hold were reworded from the code on 2026-11-03 and not walked. Home, Devices, the setup dialog and the request dialog were rewritten from the code on 2026-11-15 and not walked
  date: 2026-10-06
  except: the browser sign-in and the screens rebuilt on 2026-11-15, written from the code and not walked
applies_to: both
who: The approver, after an admin gives them the role
where: Self-service page
steps: true
keywords: approve browser self-service set up this browser devices approver device requests decide straza-enroll-browser signed decision
---


The self-service page is the part of Straza for any signed-in person, at `/self-service/` on your server, where the root address also leads. Its **Requests** tab lists the approval requests that are yours to decide. A person who is not an admin decides there from a browser they set up once. The setup makes a signing key that stays in that browser, so every decision is signed by that browser, and nobody can decide for the person from a copied link. Admins can decide the same requests in the console, as [Approve in the console]({{< relref "guides/approve/console.md" >}}) shows.

## Before you start {.nostep}


- A rule that routes requests to the person: as a holder of an approver role it names, as the sponsor of an agent, or as the person behind their own agent. [Hold a call for a person]({{< relref "guides/write-policy/hold-for-a-human.md" >}}) writes such a rule.
- A browser in a normal window that keeps its site storage. A private window throws the key away when it closes.
- The person's account on the server, at the identity provider your server signs in with.

## Give the person the browser role


Only a person who holds `straza-enroll-browser`, or `straza-admin`, may set up a browser. When your identity manager feeds Straza over SCIM, assign the role there. Otherwise, as the admin, assign it in the console or from a terminal.

{{< clicks "Roles" "straza-enroll-browser" "Assign to a user" "Assign role" >}}

Pick the person under **User** before you press **Assign role**.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl assign straza-enroll-browser --user lena
```
{{< /command >}}

{{< see >}}`assigned straza-enroll-browser to lena`{{< /see >}}

## Sign in on the self-service page


The person opens `/self-service/` on the server, for example `http://127.0.0.1:8420/self-service/` on a standalone server, and presses **Sign in** at the top right. The tab goes straight to the identity provider, or to the server's own sign-in page on a standalone server. After the sign-in the tab comes back to the self-service page, signed in.

{{< shot name="selfservice-signin" caption="The self-service page before sign-in, with **Sign in** at the top right." >}}

Before the browser is set up, **Home** shows the card `Set up this browser to approve`. A request that waits for you already shows on **Home** and under **Requests**, but only a device that is set up answers it. With nothing waiting, **Requests** says why it is empty.

{{< see >}}`Nothing waits for you` and `To answer a request, set up this browser.`{{< /see >}}

## Set up this browser


{{< clicks "Home" "Set up this browser" >}}

{{< shot name="selfservice-enable" caption="The dialog that sets up the browser, with **Name for this browser** and **Set up**." >}}

The dialog opens with `Straza makes a key that stays in this browser and cannot be copied out. Your approvals are signed with it.`

Give the browser a name under **Name for this browser**, such as `lena-laptop`. You see it on **Devices**, and your administrators see it in their list of approver devices, so pick one you recognise. Where the browser can receive notifications, the box `Notify me in this browser when a request waits` is ticked, and the browser asks its permission once. Press **Set up**.

{{< see >}}`Set up. This browser approves as lena.`{{< /see >}}

**Devices** now lists the browser, marked `this browser`, with a line such as `Approves as lena. Notifications on. Set up just now.` Its credential renews itself at every check-in, so it has no expiry to act on.

**Turn on notifications** and **Turn off notifications** on that row switch whether Straza pushes each request to this browser. Where the deployment sends no browser push, the help icon beside the line says `Straza is not set up to send browser notifications.`, and the browser sees requests while the page is open. [Push delivery and connectivity]({{< relref "guides/approve/push-and-connectivity.md" >}}) sets up push.

{{< fails >}}
`Your account has no role that lets it approve. Ask your administrator for one.`
: The account lacks `straza-enroll-browser`. Ask your admin for it, as the step before shows.

`This browser does not let the page keep a key, so it cannot be set up to approve. Use a normal window, or approve from the Straza approver app on your phone.`
: The browser refuses site storage, as some private windows do. Open the page in a normal window.

`Answer in the Straza approver app on your phone.`
: The account holds `straza-enroll-mobile` only. Use **Add a phone** on **Devices**, as [Approve on a phone]({{< relref "guides/approve/phone.md" >}}) shows, or ask for the browser role.
{{< /fails >}}

## Decide a request


{{< clicks "Requests" "Waiting" >}}

{{< shot name="selfservice-requests" caption="A waiting request with **Deny** and **Approve** on its row." >}}

Each waiting request shows when it was asked, who asked, the call and the time left. A row that is yours to decide carries **Deny** and **Approve**, and both open the request, as the row does. On a phone each request is a card.

The request opens in a dialog whose title says who asks for what, such as `alice wants to run a command`. It shows the call, the time left, what happens if you approve and if you deny, and **Who can decide**, such as `You, as a holder of release-approvers. The first to answer decides.` A hold the agent's hook raised says `The agent was told to retry the call after your answer.`, and when the time runs out the call is denied.

Type an optional reason under **Reason, optional** at the foot of the dialog. The agent and the audit record read it. Then press **Approve** or **Deny** in the dialog.

{{< see >}}`Approved. kubectl apply -f deploy/app.yaml runs for alice if the agent retries it soon.`{{< /see >}}

The agent's retry of the same call then runs. Deciding needs no sign-in once the browser is set up, because the browser's key signs each decision.

{{< fails >}}
`This request was already approved.`
: Another person who may decide answered first. Nothing more is needed.

`This request expired before the decision landed.`
: Its window closed and the call was denied. A new try by the agent raises a new request.

`Straza no longer accepts this browser's key, so nothing was decided. Remove this browser under Devices, then set it up again.`
: The server no longer knows this browser, for example after a restore from backup. Remove it on **Devices** and set it up again.
{{< /fails >}}

## Check the record


The decision lands on the audit chain as an approval record with the channel `browser`, the device that signed it and the reason. An admin picks **Approvals** under **Kind of record** on the console's Audit, or runs `strazactl audit tail`.

{{< details summary="Recorded output, the resolution record" >}}
```text
#40 [alice] {"data":{"approvalId":"01a112cb-06c3-71c6-ab77-04a1572687d9","channel":"browser","decidedBy":"01a112c8-8c38-71e5-98b5-b1c98a35be95","decidedDeviceId":"apd_01a112cb-7661-7797-9e38-b77aa2690d61","decidedReason":"Release 4.2 is approved in the change board.","lane":"hook","phase":"resolution","rule":"hold-kubectl-apply","session":"01a112c9-b791-7c3c-b06c-758404a6eb9f","set":"kubectl-apply-hold","state":"approved","summary":"shell.exec: kubectl apply -f deploy/app.yaml","user":"01a112c8-8bba-7882-a955-fdee5b4e414e"},"id":"ce133752-3f15-4ca6-b64c-c16ab76a2dc4","source":"strazad","specversion":"1.0","time":"2026-10-06T19:58:17.94788989Z","type":"straza.audit.approval"}
```
{{< /details >}}

## Remove the browser


On **Devices**, **Remove** on the browser's row asks first and says what follows, `You can no longer approve from this browser. Its key is deleted now and Straza forgets the device.` The person's other devices are untouched, and **Devices** removes them the same way. **Sign out**, in the menu under the person's name at the top right, only ends the signed-in session, and the browser still decides as its person afterwards. On a shared machine, do both, and sign out at the identity provider too.

An admin revokes a lost browser in the console under **Approvals**, on the **Approver devices** tab, or with `strazactl approvers revoke <device-id>`.

## Next {.nostep}

- [Approve on a phone]({{< relref "guides/approve/phone.md" >}}) adds a phone that signs decisions in its secure hardware.
- [Approvals]({{< relref "concepts/approvals-model.md" >}}) explains who may decide which request, and why a person's own request needs a signed device.
