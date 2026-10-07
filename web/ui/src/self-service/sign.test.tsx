import { describe, expect, it } from "vitest";
import nodeCrypto, { webcrypto } from "node:crypto";
import { b64, decideMessage, decideMessageWithReason, exportPublicKeyB64, generateDeviceKey, rawToDer, refreshMessage, signMessageB64 } from "./sign";

// jsdom's crypto has no subtle; the page runs on the browser's WebCrypto,
// which node's webcrypto implements.
if (!globalThis.crypto || !globalThis.crypto.subtle) {
  Object.defineProperty(globalThis, "crypto", { value: webcrypto, configurable: true });
}

const hex = (s: string) => new Uint8Array(s.match(/../g)!.map((b) => parseInt(b, 16)));

describe("the canonical messages", () => {
  it("decide is four lines with no trailing newline", () => {
    const m = decideMessage("apr_1", "deny", "chal", 1);
    expect(m).toBe("apr_1\ndeny\nchal\n1");
    expect(m.split("\n")).toHaveLength(4);
    expect(m.endsWith("\n")).toBe(false);
  });
  it("a reason adds its sha256 as a fifth line, no reason keeps the four", async () => {
    expect(await decideMessageWithReason("apr_1", "approve", "chal", 7, "")).toBe("apr_1\napprove\nchal\n7");
    expect(await decideMessageWithReason("apr_1", "approve", "chal", 7, "abc"))
      .toBe("apr_1\napprove\nchal\n7\nba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  });
  it("refresh carries its domain tag", () => {
    expect(refreshMessage("dev_1", "chal")).toBe("refresh\ndev_1\nchal");
  });
});

describe("rawToDer", () => {
  it("encodes r and s with a top bit set as padded INTEGERs", () => {
    const r = new Uint8Array(32).fill(0xff);
    const s = new Uint8Array(32).fill(0x80);
    const der = rawToDer(new Uint8Array([...r, ...s]));
    expect(der[0]).toBe(0x30);
    expect(der[1]).toBe(70);
    expect(Array.from(der.subarray(2, 5))).toEqual([0x02, 0x21, 0x00]);
    expect(Array.from(der.subarray(37, 40))).toEqual([0x02, 0x21, 0x00]);
    expect(der.length).toBe(72);
  });
  it("strips leading zeros to the minimal encoding", () => {
    const der = rawToDer(hex("00000001".padEnd(64, "0") + "00000002".padEnd(64, "0")));
    expect(Array.from(der.subarray(0, 4))).toEqual([0x30, 62, 0x02, 29]);
  });
  it("refuses an odd length", () => {
    expect(() => rawToDer(new Uint8Array(63))).toThrow(/even number of bytes/);
  });
});

describe("b64", () => {
  it("uses the padded standard alphabet", () => {
    expect(b64(new Uint8Array([251, 255, 254]))).toBe("+//+");
    expect(b64(new Uint8Array([1]))).toBe("AQ==");
  });
});

describe("the key and the signature", () => {
  it("signs a message the server's DER verifier accepts, with a non-extractable private key", async () => {
    const keys = await generateDeviceKey();
    expect(keys.privateKey.extractable).toBe(false);
    const spki = await exportPublicKeyB64(keys.publicKey);
    expect(spki.endsWith("=") || spki.length % 4 === 0).toBe(true);
    const message = decideMessage("apr_1", "approve", "chal", 1700000000);
    const sig = await signMessageB64(keys.privateKey, message);
    const pub = nodeCrypto.createPublicKey({ key: Buffer.from(spki, "base64"), format: "der", type: "spki" });
    const ok = nodeCrypto.verify("sha256", Buffer.from(message, "utf8"), { key: pub, dsaEncoding: "der" }, Buffer.from(sig, "base64"));
    expect(ok).toBe(true);
    const bad = nodeCrypto.verify("sha256", Buffer.from(message + "\n", "utf8"), { key: pub, dsaEncoding: "der" }, Buffer.from(sig, "base64"));
    expect(bad).toBe(false);
  });
});
