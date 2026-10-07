// The shape of the vendored QR encoder the enrol sheet draws with: the
// three members lib/qr.ts calls, nothing more.
declare namespace qrcodegen {
  class QrCode {
    static encodeText(text: string, ecl: QrCode.Ecc): QrCode;
    readonly size: number;
    getModule(x: number, y: number): boolean;
  }
  namespace QrCode {
    class Ecc {
      static readonly LOW: Ecc;
      static readonly MEDIUM: Ecc;
      static readonly QUARTILE: Ecc;
      static readonly HIGH: Ecc;
    }
  }
}
export default qrcodegen;
