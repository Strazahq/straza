// The audit chain check in the browser, and the local hash of a hunted
// value: both need WebCrypto, which browsers grant only to a secure
// context (TLS or localhost).
import type { AuditRow } from "./api";

export const canHash = typeof crypto !== "undefined" && !!crypto.subtle;

// sha256Hex hashes text with WebCrypto and returns lowercase hex.
export async function sha256Hex(text: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return Array.from(new Uint8Array(digest)).map((b) => b.toString(16).padStart(2, "0")).join("");
}

// verifyChain re-hashes contiguous records in ascending order the way
// internal/audit.Link does, hash = sha256(prevHash + "\n" + ce), and checks
// each record links to the one before it. startPrev is the hash the first
// record must link back to, or null when the window starts mid-chain and
// only its inner links can be checked. brokenSeq names the first record
// that fails, 0 when the chain holds.
export async function verifyChain(records: AuditRow[], startPrev: string | null): Promise<{ ok: boolean; brokenSeq: number }> {
  let prev = startPrev;
  for (const r of records) {
    if (prev !== null && r.prevHash !== prev) return { ok: false, brokenSeq: r.seq };
    if ((await sha256Hex((r.prevHash || "") + "\n" + r.ce)) !== r.hash) return { ok: false, brokenSeq: r.seq };
    prev = r.hash || "";
  }
  return { ok: true, brokenSeq: 0 };
}
