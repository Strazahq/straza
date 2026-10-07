// The page's side of the enrolment record: load, save, patch and wipe over
// the IndexedDB primitives, plus the two reads that say whether this
// browser will keep what it stores. The login session is not here: it is
// the console's session module, shared per tab, so Open the console
// resumes the same sign-in.
import { type Enrollment, type PushRegistration, clearRecord, patchRecord, readRecord, storageAvailable as idbAvailable, writeRecord } from "./idb";

export type { Enrollment, PushRegistration } from "./idb";

export function storageAvailable(): boolean {
  return idbAvailable();
}

// loadEnrollment answers the stored enrolment or null. A record missing its
// key pair or device token is dropped: it can only produce signatures
// nobody can verify, and not enrolled is the honest, recoverable state.
export async function loadEnrollment(): Promise<Enrollment | null> {
  const rec = await readRecord();
  if (!rec || !rec.deviceId || !rec.deviceToken || !rec.keys || !rec.keys.privateKey) {
    if (rec) await wipeEnrollment();
    return null;
  }
  return rec as Enrollment;
}

export function saveEnrollment(rec: Enrollment): Promise<unknown> {
  return writeRecord(rec);
}

// patchEnrollment merges fields into the stored record, the token refresh
// path. A missing record is not recreated.
export function patchEnrollment(patch: Partial<Enrollment>): Promise<Partial<Enrollment> | null> {
  return patchRecord(patch);
}

// savePush and clearPush hold the one push registration this browser owns,
// next to the enrolment, so a revoke takes the push state with it.
export function savePush(registration: PushRegistration): Promise<unknown> {
  return patchRecord({ push: registration });
}

export function clearPush(): Promise<unknown> {
  return patchRecord({ push: null });
}

// wipeEnrollment destroys the device credential and its key pair: on a
// device_revoked answer, and on the person's own revoke of this browser.
export function wipeEnrollment(): Promise<unknown> {
  return clearRecord().catch(() => null);
}

export type StorageOutlook = "durable" | "ephemeral" | "unknown";

// storageOutlook is a best-effort read of whether storage here survives the
// window closing, asked before enrolling because a private window throws
// the key away and the person is left wondering why the page forgot them.
// durable means the browser promised not to evict this origin, ephemeral is
// the tiny quota every private mode reports, unknown is neither signal.
export async function storageOutlook(): Promise<StorageOutlook> {
  const s = typeof navigator !== "undefined" ? navigator.storage : null;
  if (!s) return "unknown";
  try {
    if (s.persisted && await s.persisted()) return "durable";
  } catch {
    // fall through to the quota heuristic
  }
  try {
    if (s.estimate) {
      const est = await s.estimate();
      if (est && typeof est.quota === "number" && est.quota > 0 && est.quota < 200 * 1024 * 1024) return "ephemeral";
    }
  } catch {
    // no estimate: unknown
  }
  return "unknown";
}

// requestDurable asks the browser to keep this origin's storage, from the
// enrol click alone, where a prompt has context. A refusal is normal.
export async function requestDurable(): Promise<boolean> {
  const s = typeof navigator !== "undefined" ? navigator.storage : null;
  if (!s || !s.persist) return false;
  try {
    return await s.persist();
  } catch {
    return false;
  }
}
