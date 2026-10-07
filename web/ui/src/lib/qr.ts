// The QR drawing of the enrol sheet: the encoder is loaded on
// first use, so the entry page never carries it, and the drawing is a path
// over a grid that includes the quiet zone the standard requires.

export type QrDrawing = {
  // size is the grid's side in modules, the quiet zone included.
  size: number;
  // path draws every dark module as a unit square, for one <path d>.
  path: string;
  // text is the exact string that was encoded, for the data attribute a
  // test or a walk reads back.
  text: string;
};

// QUIET is the quiet zone in modules, the minimum the QR standard names.
export const QUIET = 4;

// qrDrawing encodes text at medium error correction. It answers null when
// the text does not fit a QR code, so the caller can say so instead of
// drawing nothing.
export async function qrDrawing(text: string): Promise<QrDrawing | null> {
  const mod = await import("@/vendor/qrcodegen.js");
  const lib = mod.default;
  let qr: InstanceType<typeof lib.QrCode>;
  try {
    qr = lib.QrCode.encodeText(text, lib.QrCode.Ecc.MEDIUM);
  } catch {
    return null;
  }
  let path = "";
  for (let y = 0; y < qr.size; y++) {
    for (let x = 0; x < qr.size; x++) {
      if (qr.getModule(x, y)) path += "M" + (x + QUIET) + "," + (y + QUIET) + "h1v1h-1z";
    }
  }
  return { size: qr.size + QUIET * 2, path, text };
}
