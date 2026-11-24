// The sentences of the self-service page that are not the Approvals
// area's own: the shell, Home, Devices and the setup of this browser. The
// queue and the request dialog speak lib/approval-words.ts.
import { REFUSED_BY, bare } from "@/lib/say";
import type { SelfTab } from "./router";

// ---- the shell ----

export const PAGE_TITLE = "Self-service";
export const DOC_TITLE = "Straza self-service";
export const MENU = "Menu";
export const DISMISS = "Dismiss";
export const TABS: { key: SelfTab; label: string }[] = [
  { key: "home", label: "Home" },
  { key: "requests", label: "Requests" },
  { key: "credentials", label: "Credentials" },
  { key: "devices", label: "Devices" },
];
// TAB_HEAD is the title that opens each tab.
export const TAB_HEAD: Record<SelfTab, string> = {
  home: "Self-service",
  requests: "Requests",
  credentials: "Credentials",
  devices: "Devices",
};
export const BRAND = "Straza";
export const OPEN_CONSOLE = "Open the console";
export const SIGN_IN = "Sign in";
export const SIGN_OUT = "Sign out";
export const RESUMING = "Resuming the session.";
export const OPENING = "Opening the tab.";
export const TRY_AGAIN = "Try again";
export const CONTINUE = "Continue";

// The page-level notices.
export const REVOKED = "This browser was removed from your devices, so its key was deleted here. Set it up again to approve from it.";
export const STORAGE_BROKEN = (why: string) => "This browser's storage could not be opened (" + why + "). Close other Straza tabs, then reload.";
export const SIGNED_OUT_ENROLLED = "Signed out. This browser still approves as you.";
export const SIGNED_OUT = "Signed out.";
export const SIGN_OUT_UNCONFIRMED = "Signed out here. Straza was not reached, and the session ends on its own within minutes.";

// The requests while no device of this browser can sign and nothing waits.
// WAITS_SHOW_HERE names a phone, which WAITS_SHOW_HERE_BROWSER leaves out
// for an account that may not add one.
export const SIGN_IN_FOR_REQUESTS = "Sign in to see what waits for you";
export const SIGN_IN_FOR_REQUESTS_BODY = "To answer a request, set up this browser or add a phone.";
export const WAITS_SHOW_HERE = SIGN_IN_FOR_REQUESTS_BODY;
export const WAITS_SHOW_HERE_BROWSER = "To answer a request, set up this browser.";
export const WAITS_SHOW_HERE_DEVICE = "Answer on your other device, or set up this browser.";
export const WAITS_SHOW_HERE_PHONE = "Answer in the Straza approver app on your phone.";
export const NOT_ENROLLED_NO_ROLE = "Your account has no role that lets it approve. Ask your administrator for one.";

// ---- the Requests tab and the home view ----

export const WAITING_HEAD = "Waiting";
export const DECIDED_HEAD = "Decided lately";
export const ALL_REQUESTS = "All requests";
export const NOTHING_WAITS = "Nothing waits for you.";

// What a failed read says to a person.
export const NO_ANSWER = "Straza did not answer. Try again in a moment. If this keeps happening, tell your administrator.";
export const readProblem = (why: string) => "Your requests could not be read: " + why + ". Try again in a moment. If this keeps happening, tell your administrator.";
// readSaid is what a failed read says. A message of the server that is a
// whole sentence of its own, one that opens with a capital letter and ends
// in a full stop, is shown as it is. Any other message goes inside the
// page's own sentence, which wrap makes.
export const readSaid = (message: string, wrap: (why: string) => string) =>
  /^[A-Z][\s\S]*\.$/.test(message.trim()) ? message.trim() : wrap(bare(message));
// keyRejected is the remedy when the server no longer knows this browser's
// key, the state a restored backup or a reborn eval volume leaves behind.
// Nothing local is destroyed by that answer, so the remedy is the person's
// own.
export const keyRejected = (why: string) => "Straza no longer accepts this browser's key (" + why + "). Remove this browser under Devices, then set it up again.";
export const lastRead = (at: string) => "Last read at " + at + ".";

// failedWhy is the sentence of a write that failed, under a heading that
// says what did not happen. A write Straza did not answer reads as
// NO_ANSWER. A refusal gives Straza's reason, and a bare HTTP status,
// which carries none, says only that Straza refused it.
export function failedWhy(err: { message: string; unreachable?: boolean }): string {
  if (err.unreachable) return NO_ANSWER;
  const why = /^HTTP \d+$/.test(err.message) ? "" : bare(err.message);
  return "Straza refused it" + (why ? ": " + why : "") + ". If this keeps happening, tell your administrator.";
}

// The refusals of a decision, each already a sentence.
export const REASON_TOO_LONG = "The reason is too long. Shorten it.";
export const SIGNATURE_REFUSED = "Straza did not accept this browser's signature, even on a second try. Reload the page and try again.";
export const CREDENTIAL_REFUSED = "Straza no longer accepts this browser's key, so nothing was decided. Remove this browser under Devices, then set it up again.";
export const NOT_ENROLLED_NOW = "This browser is no longer set up to approve, so nothing was sent.";
export const REQUEST_GONE = "This request is gone.";
// decideRefused opens with REFUSED_BY, which the block folds into its
// heading, so the block says refused once.
export const decideRefused = (why: string) => REFUSED_BY + why + ". Try again, and tell your administrator if it keeps happening.";
export const DECIDE_UNCONFIRMED = "Straza did not answer, so it is not known whether your answer landed. Look at the request again before you try once more.";

// alreadySettled ends the question when the record resolved before this
// decision landed. The lane answers the final state and no sentence, so the
// state is what the sentence has to work from.
const SETTLED_LINE: Record<string, string> = {
  approved: "This request was already approved.",
  denied: "This request was already denied.",
  expired: "This request expired before the decision landed.",
};
export const alreadySettled = (state: string) => SETTLED_LINE[state] || "This request was already decided.";

// ---- the setup card, on Home and on Devices ----

export const SETUP_HEAD = "Set up this browser to approve";
export const SETUP_SIGN_IN = "Sign in first.";
export const SETUP_BROWSER = "Set up this browser";
export const NO_STORAGE = "This browser does not let the page keep a key, so it cannot be set up to approve. Use a normal window, or approve from the Straza approver app on your phone.";
export const ELIGIBILITY_UNREAD = "Straza did not say what your account may set up.";

// ---- Home's three cards ----

export const waitsForYou = (n: number) => (n === 1 ? "1 request waits for you" : n + " requests wait for you");
// firstWaiting is the sentence of the first waiting request: who, the verb
// of the request, the call and the time left.
export const firstWaiting = (who: string, verb: string, call: string, left: string) =>
  who + " " + verb + " " + call + ". " + left + ".";
export const REVIEW = "Review";
export const NOTHING_WAITS_CARD = "Nothing waits for you";
export const REQUESTS_UNREAD_CARD = "Your requests could not be read";
export const NO_ROLE_TITLE = "You cannot approve yet";

export const needsCredential = (n: number) => (n === 1 ? "1 server needs a credential" : n + " servers need a credential");
// The sentence of the first credential that waits, by what is missing.
export const noTokenFromYou = (app: string) => app + " has no token from you, so your agents cannot call it as you.";
export const noSignInFromYou = (app: string) => app + " has no sign-in from you, so your agents cannot call it as you.";
export const tokenExpired = (app: string, who: string) => (who ? who + "'s token" : "Your token") + " for " + app + " has expired, so calls with it are refused.";
export const agentNoToken = (who: string, app: string) => who + " has no token for " + app + ", so its calls are refused.";
export const OPEN_CREDENTIALS = "Open Credentials";
export const CREDENTIALS_SET = "No server needs a credential";
export const CREDENTIALS_SIGN_IN = "Sign in to see your credentials";
export const CREDENTIALS_UNREAD = "Your credentials could not be read";

export const APPROVE_FROM_HERE = "You approve from this browser";
export const hereLine = (name: string, push: "on" | "off" | "none") =>
  name + ". " + (push === "on" ? "Notifications are on." : push === "off" ? "Notifications are off, so you see a request only while this page is open." : "This browser shows a request only while this page is open.");
export const MANAGE = "Manage";
export const NOT_SET_UP = "This browser is not set up";
export const NO_PHONE_YET = "No phone yet";
export const PHONE_APPROVES = "You approve from the Straza approver app on a phone.";

// ---- the Devices tab ----

export const THIS_BROWSER_BADGE = "this browser";
export const deviceLine = (user: string, push: "on" | "off" | "none", ago: string) =>
  "Approves as " + user + ". " + (push === "on" ? "Notifications on." : push === "off" ? "Notifications off." : "No notifications in this browser.") + " Set up " + ago + ".";
export const TURN_ON = "Turn on notifications";
export const TURN_OFF = "Turn off notifications";
export const REMOVE = "Remove";
export const removeName = (name: string) => "Remove " + name;
export const DEVICES_FOOT = "Signing out does not remove a device.";
// otherDeviceLine is the line under a device that is not this browser. It
// says where the key is only when the platform attested it.
export const otherDeviceLine = (hardware: boolean, notifies: boolean, seen: string) =>
  (hardware ? "Key in the phone's secure hardware. " : "") + (notifies ? "Notifications on." : "Notifications off.") + (seen ? " Last seen " + seen + "." : " Not seen yet.");
export const removeOtherBody = (name: string) => "You can no longer approve from " + name + ".";
export const removedOtherToast = (name: string) => "Removed. You can no longer approve from " + name + ".";
export const DEVICE_NOT_REMOVED = "Device not removed.";
export const DEVICES_UNREAD = "Your other devices could not be read.";
export const SIGN_IN_FOR_DEVICES = "Sign in to see your other devices.";

// What Home's third card says of the person's devices.
export const approveFrom = (n: number) => (n === 1 ? "You approve from 1 device" : "You approve from " + n + " devices");
export const devicesSetUp = (n: number) => (n === 1 ? "1 device is set up" : n + " devices are set up");
// devicesLine names the devices and says whether requests reach them all.
export const devicesLine = (names: string[], silent: string[]) =>
  (names.length <= 1 ? names.join("") : names.slice(0, -1).join(", ") + " and " + names[names.length - 1]) + ". " +
  (silent.length === 0 ? (names.length === 1 ? "Notifications are on." : names.length === 2 ? "Notifications are on for both." : "Notifications are on for all of them.")
    : "Notifications are off on " + (silent.length <= 1 ? silent.join("") : silent.slice(0, -1).join(", ") + " and " + silent[silent.length - 1]) + ".");
export const THIS_BROWSER_NAME = "This browser";
export const NO_DEVICE_YET = "No device yet";

// Requests before this browser is set up. The lines that name a phone have
// a twin without it for an account that may not add one.
export const SETUP_TO_ANSWER = "set up a device to answer";
export const ANSWER_ON_DEVICE = "answer on your device";
export const ANSWER_NEEDS_DEVICE = "Set up this browser or add a phone under Devices to answer.";
export const ANSWER_NEEDS_BROWSER = "Set up this browser to answer.";
export const ANSWER_ON_DEVICE_LINE = WAITS_SHOW_HERE_DEVICE;
export const answerAfterSetup = (who: string, verb: string, call: string) =>
  who + " " + verb + " " + call + ". Set up this browser or a phone to answer.";
export const answerAfterBrowser = (who: string, verb: string, call: string) =>
  who + " " + verb + " " + call + ". Set up this browser to answer.";
// A person whose role lets them approve from a phone only is never offered
// the setup of a browser: they answer on the phone, or add one first.
export const ANSWER_ON_PHONE = "answer on your phone";
export const ADD_PHONE_TO_ANSWER = "add a phone to answer";
export const ANSWER_ON_PHONE_LINE = "Answer this in the Straza approver app on your phone.";
export const ANSWER_NEEDS_PHONE = "Add a phone under Devices to answer.";
export const answerOnPhone = (who: string, verb: string, call: string) =>
  who + " " + verb + " " + call + ". Answer it in the Straza approver app on your phone.";
export const answerAfterPhone = (who: string, verb: string, call: string) =>
  who + " " + verb + " " + call + ". Add a phone to answer.";
export const answerOnDevice = (who: string, verb: string, call: string) =>
  who + " " + verb + " " + call + ". Answer on a device you have set up, or set this browser up.";
export const ABOUT_NOTIFICATIONS = "About notifications in this browser";

// Removing this browser.
export const removeTitle = (name: string) => "Remove " + name + "?";
export const REMOVE_BODY = "You can no longer approve from this browser. Its key is deleted now and Straza forgets the device.";
export const REMOVE_DEVICE = "Remove device";
export const REMOVED_TOAST = "Removed. This browser no longer approves.";
export const REMOVE_UNCONFIRMED = "This browser's key is gone, but Straza did not confirm it forgot the device. If it is still listed under Devices, remove it there.";

// Notifications. Each reason they cannot be offered is a sentence behind
// the help icon beside the device's line.
export const PUSH_OFF_DEPLOYMENT = "Straza is not set up to send browser notifications.";
export const PUSH_OFF_PLATFORM = "This browser cannot receive notifications, so it shows a request only while this page is open.";
export const PUSH_OFF_PRIVATE = "Notifications are not offered in a private window.";
export const PUSH_OFF_BLOCKED = "Notifications are blocked for this site. Allow them in the browser's site settings first.";
export const CONSENT_TITLE = "Turn on notifications in this browser?";
export const CONSENT_BODY = "Your browser asks next, and only once. A no can be undone only in its site settings.";
export const CONSENT_HELP = "A notification names the tool and who asked. It never carries the command or its parameters.";
export const NOTIFY_ON_TOAST = "Notifications on.";
export const NOTIFY_OFF_TOAST = "Notifications off. This browser shows a request only while this page is open.";
export const NOTIFY_DECLINED = "You did not allow notifications, so none are sent to this browser.";
export const WORKER_SLOW = "this browser did not start the worker that receives notifications";
export const notifyFailed = (why: string) => "Notifications could not be turned on: " + why + ". Reload the page and try again.";
export const notifyRemovedLocally = (why: string) => "Notifications are off in this browser. Straza could not be told (" + why + ") and stops sending soon.";
export const pushLost = (why: string) => "Notifications stopped in this browser and could not be renewed (" + why + "). Turn them on again.";

// ---- the setup dialog ----

export const SETUP_SUB = "Straza makes a key that stays in this browser and cannot be copied out. Your approvals are signed with it.";
export const DEVICE_NAME_LABEL = "Name for this browser";
export const DEVICE_NAME_HINT = "Shown under Devices.";
export const DEVICE_NAME_REQUIRED = "Give this browser a name.";
export const NOTIFY_ME = "Notify me in this browser when a request waits";
export const NOTIFY_HINT = "Your browser asks for permission once.";
export const SET_UP = "Set up";
export const PRIVATE_WINDOW = "This looks like a private window. The key is lost when it closes.";
export const OUTLOOK_UNKNOWN = "This browser may clear the key without warning.";
export const SHEET_SIGN_IN = "Your session ended. Sign in again to finish the setup.";
export const SETTING_UP = "Setting up this browser.";
export const cooldownLine = (seconds: number) => "Too many tries in a row. Try again in " + seconds + " seconds.";
export const ELIGIBILITY_FAILED = "Straza did not say what your account may set up. Try again.";
export const KEY_FAILED = "This browser could not make the key. Use a normal window of a current browser.";
export const SETUP_FAILED = "Setup did not finish.";
// storeRefused is the one outcome that leaves a device at Straza which this
// browser cannot use, so it names the device to remove under Devices.
export const storeRefused = (why: string, name: string) => "This browser could not keep the key (" + why + "). Remove " + name + " under Devices, then set up again in a normal window.";
export const setUpToast = (user: string) => "Set up. This browser approves as " + user + ".";

// ---- Add a phone ----

export const ADD_PHONE = "Add a phone";
// PHONE_PASTE stands in for the QR code when the page is open on the phone
// itself.
export const PHONE_PASTE = "This page is open on a phone, so there is no code to scan. In the Straza approver app, choose Enter the code manually and paste this text.";
