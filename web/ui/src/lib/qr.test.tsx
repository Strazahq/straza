import { describe, expect, it } from "vitest";
import { QUIET, qrDrawing } from "./qr";

describe("the QR drawing", () => {
  it("encodes a short payload into a grid with the quiet zone around it", async () => {
    const payload = JSON.stringify({ v: 1, servers: ["https://127.0.0.1:8443"], token: "set_0123" });
    const d = await qrDrawing(payload);
    expect(d).not.toBeNull();
    // Version 1 is 21 modules; the payload needs more, and every version
    // is 21 plus a multiple of 4, plus the quiet zone on both sides.
    expect((d!.size - QUIET * 2 - 21) % 4).toBe(0);
    expect(d!.size).toBeGreaterThan(21 + QUIET * 2);
    expect(d!.path.startsWith("M")).toBe(true);
    expect(d!.text).toBe(payload);
  });

  it("answers null for a text no QR code can carry", async () => {
    expect(await qrDrawing("x".repeat(5000))).toBeNull();
  });
});
