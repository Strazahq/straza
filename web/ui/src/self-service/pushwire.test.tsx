import { describe, expect, it } from "vitest";
import { b64urlToBytes, bytesToB64url, registrationBody, sameRegistration, subscribeOptions } from "./pushwire";

describe("base64url", () => {
  it("decodes unpadded and padded input to the exact bytes", () => {
    expect(Array.from(b64urlToBytes("AQID"))).toEqual([1, 2, 3]);
    expect(Array.from(b64urlToBytes("AQI="))).toEqual([1, 2]);
    expect(b64urlToBytes("-_8")).toBeInstanceOf(Uint8Array);
    expect(Array.from(b64urlToBytes("-_8"))).toEqual([251, 255]);
  });
  it("refuses the standard alphabet and a single trailing character", () => {
    expect(() => b64urlToBytes("+/8")).toThrow(/unpadded base64url/);
    expect(() => b64urlToBytes("AQIDA")).toThrow(/single character/);
  });
  it("encodes without padding, from a buffer or bytes", () => {
    expect(bytesToB64url(new Uint8Array([251, 255]))).toBe("-_8");
    expect(bytesToB64url(new Uint8Array([1]).buffer)).toBe("AQ");
    const point = new Uint8Array(65);
    expect(bytesToB64url(point)).toHaveLength(87);
  });
});

describe("the subscription and its registration", () => {
  const key = bytesToB64url(new Uint8Array(65).fill(4));
  it("subscribes with bytes and userVisibleOnly", () => {
    const o = subscribeOptions(key);
    expect(o.userVisibleOnly).toBe(true);
    expect(o.applicationServerKey).toBeInstanceOf(Uint8Array);
    expect(o.applicationServerKey.length).toBe(65);
    expect(() => subscribeOptions("")).toThrow(/VAPID/);
  });
  it("builds the exact body the server pins and refuses a keyless subscription", () => {
    const sub = { endpoint: "https://push.example/abc", getKey: (n: string) => new Uint8Array(n === "auth" ? 16 : 65).fill(7).buffer };
    const body = registrationBody(sub);
    expect(Object.keys(body)).toEqual(["kind", "token_or_endpoint", "p256dh", "auth"]);
    expect(body.kind).toBe("webpush");
    expect(body.p256dh).toHaveLength(87);
    expect(body.auth).toHaveLength(22);
    expect(() => registrationBody({ endpoint: "https://push.example/abc", getKey: () => null })).toThrow(/encryption keys/);
    expect(() => registrationBody({ endpoint: "" })).toThrow(/no endpoint/);
  });
  it("compares registrations field by field", () => {
    const a = { kind: "webpush" as const, token_or_endpoint: "e", p256dh: "p", auth: "a" };
    expect(sameRegistration(a, { ...a })).toBe(true);
    expect(sameRegistration(a, { ...a, auth: "b" })).toBe(false);
    expect(sameRegistration(a, null)).toBe(false);
  });
});
