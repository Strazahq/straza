// The one way the page turns notifications on once the person said yes:
// the Devices tab after its consent dialog, and the setup dialog after a
// ticked box, which is the consent.
import { notify } from "@/lib/notify";
import { enable } from "./push";
import type { Enrollment, StorageOutlook } from "./store";
import * as W from "./words";

// turnOn runs the browser's prompt, the worker, the subscription and the
// registration, and says the outcome in a toast. It answers whether
// notifications are on. A no at the browser's prompt is an outcome, not a
// failure.
export async function turnOn(rec: Enrollment): Promise<boolean> {
  try {
    if (await enable(rec)) {
      notify.ok(W.NOTIFY_ON_TOAST);
      return true;
    }
    notify.warn(W.NOTIFY_DECLINED);
  } catch (e) {
    notify.failed(W.notifyFailed(e instanceof Error ? e.message : String(e)));
  }
  return false;
}

// couldNotify says whether this browser could receive notifications by
// what is known before it is set up: a service worker and the Push API, a
// window that keeps its storage, and notifications not blocked for the
// site. Whether the deployment sends any arrives with the setup.
export function couldNotify(outlook: StorageOutlook): boolean {
  if (typeof navigator === "undefined" || !navigator.serviceWorker || typeof PushManager === "undefined" || typeof Notification === "undefined") return false;
  return outlook !== "ephemeral" && Notification.permission !== "denied";
}
