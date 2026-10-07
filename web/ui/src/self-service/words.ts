// The sentences of the self-service page that are not the Approvals
// area's own: the shell, the two new tabs and this browser. The queue,
// the request dialog and the decide dialog speak lib/approval-words.ts.
import type { SelfTab } from "./router";

// ---- the shell ----

export const PAGE_TITLE = "Self-service";
export const DOC_TITLE = "Straza self-service";
export const PAGE_DESC = "Your approval requests, server credentials and approval devices.";
export const TABS: { key: SelfTab; label: string }[] = [
  { key: "requests", label: "Requests" },
  { key: "credentials", label: "Credentials" },
  { key: "browser", label: "This browser" },
];
export const BRAND = "Straza";
export const OPEN_CONSOLE = "Open the console";
export const SIGN_IN = "Sign in";
export const SIGN_OUT = "Sign out";
export const signedInAs = (user: string) => "Signed in as " + user;
export const SIGN_IN_TITLE = "Sign in";
export const SIGN_IN_BODY = "You get a short code, enter it at your identity provider, and this page signs you in. Deciding from an enrolled browser needs no sign-in; setting credentials and enabling a browser do.";
export const ENABLE_BROWSER = "Enable this browser";
export const RESUMING = "Resuming the session.";
export const OPENING = "Opening the tab.";

// The page-level notices.
export const REVOKED_BY_ADMIN = "This browser was revoked by an administrator, so its key has been destroyed here. Enable this browser again to enroll a new one.";
export const STORAGE_BROKEN = (why: string) => "This browser's storage could not be opened (" + why + "), so an enrollment cannot be kept here.";
export const SIGNED_OUT_ENROLLED = "Signed out. This browser still decides as its enrolled person; deciding needs no sign-in.";
export const SIGNED_OUT = "Signed out.";
export const SIGN_OUT_UNCONFIRMED = "Signed out in this browser. The server could not be reached to end the session; it expires on its own within minutes.";

// The queue when this browser cannot read it yet.
export const NOT_ENROLLED_TITLE = "This browser is not enrolled to decide.";
export const NOT_ENROLLED_SIGN_IN = "Sign in, then enable this browser under This browser to see what waits for you.";
export const NOT_ENROLLED_ENABLE = "Enable this browser under This browser to see what waits for you.";
export const NOT_ENROLLED_PHONE = "Your account may enroll a phone, not a browser. Add your phone with Add a phone under This browser, and it sees what waits for you.";
export const NOT_ENROLLED_NO_ROLE = "Your account holds no enrollment role. Enrollment roles are assigned in your identity manager.";

// ---- the Requests tab ----

// WHOSE_TO_DECIDE is the line under the queue. The console's ROW_HINT says
// that a row opens the request; this says where the two buttons come from,
// since the page holds the requests a person raised beside the ones they
// decide.
export const WHOSE_TO_DECIDE = "Deny and Approve appear on the rows that are yours to decide: your own agent's calls, the calls of the agents you sponsor, and the calls routed to a role you hold.";

// credentialRejected is the remedy when the server no longer knows this
// browser's credential, the state a restored backup or a reborn eval volume
// leaves behind. Nothing local is destroyed by that answer, so the remedy is
// the person's own. It reads inside the console's failed-read sentence, so
// it opens in lower case and carries no closing stop.
export const credentialRejected = (why: string) => "the server rejected this browser's credential (" + why + "). Revoke this browser under This browser, then enable it again";

// The refusals of a decision on this lane, each already a sentence: the
// approver surface answers a machine code where the admin API answers words.
export const REASON_TOO_LONG = "The reason is over 500 bytes. Shorten it; details belong in a ticket.";
export const SIGNATURE_REFUSED = "The server would not accept this browser's signature, even after a fresh attempt. Reload the page and try again.";
export const CREDENTIAL_REFUSED = "The server refused this browser's credential, so nothing was decided. Revoke this browser under This browser, then enable it again.";
export const NOT_ENROLLED_NOW = "This browser is no longer enrolled to decide, so nothing was sent.";
export const REQUEST_GONE = "This request is gone from the server, so there is nothing left to decide here.";

// alreadySettled ends the question when the record resolved before this
// decision landed. The lane answers the final state and no sentence, so the
// state is what the sentence has to work from.
const SETTLED_LINE: Record<string, string> = {
  approved: "This request was already approved, so there is nothing left to decide here.",
  denied: "This request was already denied, so there is nothing left to decide here.",
  expired: "This request expired before the decision landed, so there is nothing left to decide here.",
};
export const alreadySettled = (state: string) => SETTLED_LINE[state] || "This request was already decided, so there is nothing left to decide here.";

// ---- the This browser tab ----

// The device row of this browser, in the Approvals area's columns. The
// words the console already has stay in lib/approval-words.ts; these are
// the ones only a page about one's own browser needs.
export const THIS_BROWSER_BADGE = "this browser";
export const SEEN_NOW = "now";
export const SEEN_UNKNOWN = "unknown";
export const SUBJECT_THIS_BROWSER = "This browser's enrollment";
export const NO_EXPIRY_LINE = "The key stays in this browser and its credential renews itself on every check-in, so there is no expiry to act on.";
export const SIGN_OUT_VS_REVOKE = "Sign out (top right) ends your signed-in session. Revoke destroys the deciding key. On a shared machine, do both, and also sign out at the identity provider.";

// The tab before this browser is enrolled: one sentence of consequence, then
// the honest next step for the account that is looking at it.
export const DECIDING_MEANS = "Deciding means signing with a key that stays in this browser, so nobody can decide for you from a copied link.";
export const ENABLE_SIGN_IN_FIRST = "Sign in with your organisation account first, then enable this browser.";
export const ENABLE_HERE = "Your account may enroll this browser: use Enable this browser above.";
export const ENABLE_PHONE_ONLY = "Your account may enroll a phone, not a browser: use Add a phone above.";
export const ELIGIBILITY_UNREAD = "Straza could not say what your account may enroll, so nothing is offered here.";
export const TRY_AGAIN = "Try again";
export const KEY_MECHANICS = "The key is made by the browser itself and cannot be exported or read by anything else. The server keeps only its public half. You sign in once to prove who the key belongs to, and every decision lands in the audit chain signed by it.";
export const NO_STORAGE = "This browser will not let the page store a key, so deciding cannot be enabled here. Use a normal window, or decide from the Straza approver app on your phone.";

// Enable this browser, the sheet and its run.
export const ENABLE_SUB = "A key is made here and never leaves this browser. You sign in once to prove who it belongs to.";
export const DEVICE_NAME_LABEL = "Name for this browser";
export const DEVICE_NAME_HINT = "Your administrators see it in the approver device list, so pick one you will recognise when you need to revoke it.";
export const DEVICE_NAME_REQUIRED = "A name is required, so your administrators can tell this browser from your others.";
export const ENABLE = "Enable";
export const SIGN_IN_AND_ENABLE = "Sign in and enable";
export const PRIVATE_WINDOW = "This looks like a private window: its storage is thrown away when it closes, and the key goes with it. You can still enable deciding, but you redo it next time.";
export const OUTLOOK_UNKNOWN = "This browser may clear the key without warning. If this is a private window, deciding here ends when the window closes.";
export const STEP_SIGN_IN = "Signing you in.";
export const STEP_CHANNELS = "Checking what your account may enroll.";
export const STEP_MINT = "Asking the server for a one-time token.";
export const STEP_KEY = "Making the key that stays in this browser.";
export const cooldownLine = (seconds: number) => "Straza is rate limiting enrollment tokens for your account. Try again in " + seconds + " seconds.";
export const ELIGIBILITY_FAILED = "Straza could not say what your account may enroll, so nothing was attempted. Try again.";
// storeRefused is the one outcome that leaves a device on the server this
// browser cannot use, so it names the device an administrator has to revoke.
export const storeRefused = (why: string, name: string) => "This browser refused to store the enrollment (" + why + "), so the device named " + name + " exists on the server but cannot be used here. Ask an administrator to revoke it.";
export const enabledToast = (user: string) => "Enabled. This browser decides as " + user + ".";

// Revoke this browser.
export const REVOKE_BODY = "You can no longer approve from it. The key is deleted now and Straza forgets the device.";
export const REVOKE_BROWSER_HELP = "A browser enrolls again from this page. Other devices of the same person are untouched.";
export const REVOKED_TOAST = "Revoked. This browser no longer decides.";
export const REVOKE_UNCONFIRMED = "This browser's key is gone. The device row may remain on the server, and an administrator can revoke it.";

// The push lane. Each reason the control cannot be offered is one line in
// the Notified cell, in the register of the column's other lines, because a
// control that is simply missing reads as broken.
export const PUSH_OFF_DEPLOYMENT = "this deployment sends no push notifications to browsers";
export const PUSH_OFF_PLATFORM = "this browser has no service worker or Push API, so it sees requests only while the page is open";
export const PUSH_OFF_PRIVATE = "a private window throws the subscription away when it closes, so this is not offered here";
export const PUSH_OFF_BLOCKED = "notifications are blocked for this site: allow them in the browser's own settings first";
export const PUSH_UNAVAILABLE = "unavailable";
export const TURN_ON = "Turn on";
export const TURN_OFF = "Turn off";
export const CONSENT_TITLE = "Turn on push notifications in this browser?";
export const CONSENT_BODY = "Your browser asks for permission next, and asks only once. A no there can be undone only in the browser's own site settings.";
export const CONSENT_HELP = "What arrives names the tool that waits and who asked. The message Straza sends carries only a reference, never the command or its parameters; this browser fetches the rest itself, signed in as this device.";
export const CONTINUE = "Continue";
export const NOTIFY_ON_TOAST = "Notifications on. Straza sends each request to this browser.";
export const NOTIFY_OFF_TOAST = "Notifications off. This browser sees requests only while the page is open.";
export const NOTIFY_DECLINED = "You did not allow notifications, so this browser will not be sent any. Nothing was registered.";
export const WORKER_SLOW = "this browser did not start the background worker that receives notifications";
export const notifyFailed = (why: string) => "Notifications could not be turned on: " + why + ".";
export const notifyRemovedLocally = (why: string) => "This browser is unsubscribed, but Straza could not be told (" + why + "). It stops sending the first time a message bounces off this browser.";
export const pushLost = (why: string) => "This browser's subscription is gone and could not be renewed (" + why + "), so notifications are off until you turn them on again.";

// ---- Add a phone on the self-service page ----

export const ADD_PHONE = "Add a phone";
