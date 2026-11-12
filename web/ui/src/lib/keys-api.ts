// The admin routes of the client assertion key, which the Agent signing
// keys tab and the Overview read. They live apart from api.ts so the
// console's entry page does not carry them.
import { get, listProviders, request } from "./api";

// SigningKeyRow is one row of GET /v1/admin/signing-keys. Every purpose is
// listed, and certificate_expires_at is set on a client assertion key that
// is not retired.
export type SigningKeyRow = {
  kid: string;
  purpose: string;
  status: string;
  created_at: string;
  rotated_at?: string;
  certificate_expires_at?: string;
};

// PromoteAnswer is the promote route's answer. previous_kid and the
// seconds it stays published are set when another key signed before.
export type PromoteAnswer = { kid: string; status: string; was: string; previous_kid?: string; previous_key_leaves_document_in_seconds?: number };

export const ASSERTION_PURPOSE = "client_assertion";

const BASE = "/v1/admin/signing-keys/client-assertion";
const enc = encodeURIComponent;

export const listSigningKeys = () => get<SigningKeyRow[]>("/v1/admin/signing-keys");
export const rotateAssertionKey = () => request<{ kid: string }>("POST", BASE + "/rotate");
export const promoteAssertionKey = (kid: string) => request<PromoteAnswer>("POST", BASE + "/" + enc(kid) + "/promote");
export const retireAssertionKey = (kid: string) => request<{ kid: string }>("POST", BASE + "/" + enc(kid) + "/retire");
export const assertionKeyCertificate = (kid: string) => request<string>("GET", BASE + "/" + enc(kid) + "/certificate", undefined, { text: true });

// entraBlueprints answers the client ids of the Entra agent identity
// blueprints the configured providers name, sorted, or an empty list when
// no provider uses Entra. It answers null when the providers could not be
// read, and every caller then shows the Entra steps, so a failed read
// never hides the certificate upload.
export async function entraBlueprints(): Promise<string[] | null> {
  try {
    const rows = await listProviders();
    return [...new Set((rows || []).map((p) => p.entra_blueprint || "").filter(Boolean))].sort();
  } catch {
    return null;
  }
}

// usesEntra says whether the Entra steps show: when a provider names a
// blueprint, and when that is not known.
export const usesEntra = (blueprints: string[] | null) => blueprints === null || blueprints.length > 0;

// thumbprintOf answers the SHA-1 of a PEM certificate's DER bytes as 40
// upper-case hexadecimal digits, the form Entra lists a certificate by. It
// answers null where the browser has no Web Crypto, such as a console
// served over plain HTTP off localhost, so no wrong value is ever shown.
export async function thumbprintOf(pem: string): Promise<string | null> {
  const subtle = globalThis.crypto?.subtle;
  const b64 = pem.replace(/-----(BEGIN|END) CERTIFICATE-----/g, "").replace(/\s+/g, "");
  if (!subtle || !b64) return null;
  let der: Uint8Array<ArrayBuffer>;
  try {
    der = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
  } catch {
    return null;
  }
  const digest = await subtle.digest("SHA-1", der);
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("").toUpperCase();
}
