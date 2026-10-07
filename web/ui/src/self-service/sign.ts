// The device key and the signature contract of the browser approver, the
// browser half of the phone app's contract. The key is a WebCrypto ECDSA
// P-256 pair generated non-extractable, so the private key never becomes
// bytes this page, or an XSS in it, can read: signing is a call into the
// browser's key store and the CryptoKey object is what gets persisted. Two
// signed strings, verified in internal/approval: decide is id, verdict,
// challenge and unix seconds on four lines, with the sha256 of a reason as
// a fifth; refresh is the word refresh, the device id and the challenge.
// The refresh domain tag keeps a captured decide signature from being
// replayed as a token refresh, so neither string is assembled anywhere
// else. WebCrypto emits the P1363 r||s pair and Go verifies ASN.1 DER, and
// rawToDer is that conversion with its own known-answer test, because a
// wrong encoding fails as a generic challenge_rejected.

const SPKI_ALG: EcKeyGenParams = { name: "ECDSA", namedCurve: "P-256" };
const SIGN_ALG: EcdsaParams = { name: "ECDSA", hash: "SHA-256" };

// generateDeviceKey mints the enrolment key pair. Non-extractable applies
// to the private key; WebCrypto always marks a generated public key
// extractable, which is what exportPublicKeyB64 needs.
export function generateDeviceKey(): Promise<CryptoKeyPair> {
  return crypto.subtle.generateKey(SPKI_ALG, false, ["sign", "verify"]);
}

// exportPublicKeyB64 answers the SPKI DER as padded standard base64, which
// is what the server's base64.StdEncoding decodes.
export async function exportPublicKeyB64(publicKey: CryptoKey): Promise<string> {
  return b64(new Uint8Array(await crypto.subtle.exportKey("spki", publicKey)));
}

export function decideMessage(requestID: string, verdict: string, challenge: string, ts: number): string {
  return requestID + "\n" + verdict + "\n" + challenge + "\n" + String(ts);
}

// decideMessageWithReason adds hex(sha256(reason)) as a fifth line so the
// signature binds the words to the verdict. No reason gives the four-line
// string, byte for byte.
export async function decideMessageWithReason(requestID: string, verdict: string, challenge: string, ts: number, reason: string): Promise<string> {
  const base = decideMessage(requestID, verdict, challenge, ts);
  if (!reason) return base;
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(reason)));
  let hex = "";
  for (const b of digest) hex += b.toString(16).padStart(2, "0");
  return base + "\n" + hex;
}

export function refreshMessage(deviceID: string, challenge: string): string {
  return "refresh\n" + deviceID + "\n" + challenge;
}

// signMessageB64 signs one canonical message and answers base64 DER, the
// wire shape of both signature fields.
export async function signMessageB64(privateKey: CryptoKey, message: string): Promise<string> {
  const raw = new Uint8Array(await crypto.subtle.sign(SIGN_ALG, privateKey, new TextEncoder().encode(message)));
  return b64(rawToDer(raw));
}

// rawToDer converts a P1363 r||s signature into an ASN.1 DER SEQUENCE of
// two INTEGERs. Both halves share one fixed width, so the split is by
// length, and each half is re-encoded as a minimal two's-complement
// INTEGER: leading zeros stripped, one 0x00 prepended when the top bit is
// set. For P-256 the body is at most 72 bytes, so the SEQUENCE length is
// one short-form byte.
export function rawToDer(raw: Uint8Array | ArrayBuffer): Uint8Array {
  const sig = raw instanceof Uint8Array ? raw : new Uint8Array(raw);
  if (sig.length === 0 || sig.length % 2 !== 0) {
    throw new Error("raw ECDSA signature must be an even number of bytes (r||s), got " + sig.length);
  }
  const half = sig.length / 2;
  const r = derInteger(sig.subarray(0, half));
  const s = derInteger(sig.subarray(half));
  const body = r.length + s.length;
  const out = new Uint8Array(2 + body);
  out[0] = 0x30;
  out[1] = body;
  out.set(r, 2);
  out.set(s, 2 + r.length);
  return out;
}

function derInteger(bytes: Uint8Array): Uint8Array {
  let i = 0;
  while (i < bytes.length - 1 && bytes[i] === 0) i++;
  const v = bytes.subarray(i);
  const pad = (v[0] & 0x80) !== 0 ? 1 : 0;
  const out = new Uint8Array(2 + pad + v.length);
  out[0] = 0x02;
  out[1] = v.length + pad;
  out.set(v, 2 + pad);
  return out;
}

// b64 encodes bytes as padded standard base64.
export function b64(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode.apply(null, Array.from(bytes.subarray(i, i + 0x8000)));
  }
  return btoa(s);
}
