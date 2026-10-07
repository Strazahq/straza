// The one IndexedDB record of the self-service page, reachable from both
// halves of the surface: the page and the service worker. A worker cannot
// import a page module at runtime, so the database name, the store name,
// the record key and the primitives live here, free of window, document
// and the storage that exists only in the page. The record holds a
// non-extractable CryptoKey, which is structured-cloneable, so IndexedDB
// keeps it without it ever existing as bytes. The names are the ones the
// approvals page wrote, so a browser enrolled there stays enrolled here.

export const DB_NAME = "straza-approvals";
const DB_VERSION = 1;
const STORE = "enrollment";
const RECORD_KEY = "current";

// PushRegistration is the exact body this browser PUT to /v1/approver/push,
// kept verbatim because DELETE removes the row by the same triple.
export type PushRegistration = { kind: "webpush"; token_or_endpoint: string; p256dh: string; auth: string };

// Enrollment is the stored record: the device the server knows, its
// credential, the key pair, and the push registration when one exists.
export type Enrollment = {
  deviceId: string;
  deviceToken: string;
  tokenExpiresAt: number;
  project: { id?: string; name?: string } | null;
  user: { id?: string; username?: string } | null;
  webpush: { vapid_public_key?: string } | null;
  keys: CryptoKeyPair;
  deviceName: string;
  enrolledAt: string;
  push?: PushRegistration | null;
};

function idb(): IDBFactory | null {
  return typeof indexedDB !== "undefined" && indexedDB ? indexedDB : null;
}

// storageAvailable says whether this browser offers IndexedDB at all.
export function storageAvailable(): boolean {
  return idb() != null;
}

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const db = idb();
    if (!db) {
      reject(new Error("this browser does not offer IndexedDB, so an enrollment cannot be kept here"));
      return;
    }
    const req = db.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      if (!req.result.objectStoreNames.contains(STORE)) req.result.createObjectStore(STORE);
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error || new Error("IndexedDB refused to open"));
    req.onblocked = () => reject(new Error("another tab is holding IndexedDB open"));
  });
}

// withStore runs one request against the store in a transaction of the
// given mode and resolves with its result once the transaction completed.
export function withStore<T>(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T> | null): Promise<T | undefined> {
  return openDB().then((db) => new Promise<T | undefined>((resolve, reject) => {
    let out: T | undefined;
    const tx = db.transaction(STORE, mode);
    const req = fn(tx.objectStore(STORE));
    if (req) {
      req.onsuccess = () => { out = req.result; };
      req.onerror = () => reject(req.error || new Error("IndexedDB request failed"));
    }
    tx.oncomplete = () => { db.close(); resolve(out); };
    tx.onabort = () => { db.close(); reject(tx.error || new Error("IndexedDB transaction aborted")); };
    tx.onerror = () => { db.close(); reject(tx.error || new Error("IndexedDB transaction failed")); };
  }));
}

// readRecord answers the raw stored record, or undefined. Validation is the
// caller's: the page wants a usable enrolment, the worker only the device
// token and the VAPID advert.
export function readRecord(): Promise<Partial<Enrollment> | undefined> {
  return withStore<Partial<Enrollment>>("readonly", (s) => s.get(RECORD_KEY) as IDBRequest<Partial<Enrollment>>);
}

export function writeRecord(rec: Enrollment): Promise<unknown> {
  return withStore("readwrite", (s) => s.put(rec, RECORD_KEY));
}

// patchRecord merges fields into the stored record. A missing record is not
// recreated: the wipe that removed it wins, in the page (a revoked device)
// and in the worker (a rotated subscription arriving after a wipe must not
// resurrect a credential the person destroyed).
export async function patchRecord(patch: Partial<Enrollment>): Promise<Partial<Enrollment> | null> {
  const rec = await readRecord();
  if (!rec) return null;
  const next = Object.assign({}, rec, patch);
  await writeRecord(next as Enrollment);
  return next;
}

export function clearRecord(): Promise<unknown> {
  return withStore("readwrite", (s) => s.clear());
}
