package approval

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

// --- RFC 8291 Appendix A / §5 worked example (the gold-standard fixture) ---
//
// Every string below is copied VERBATIM from RFC 8291 (whitespace removed, as
// the RFC itself instructs). The vector pins the whole pipeline: ECDH shared
// secret → HKDF chain (PRK_key, IKM, PRK, CEK, NONCE) → RFC 8188 aes128gcm
// header → final ciphertext. Do not regenerate these values from code. They
// are the external ground truth the code is checked against.
const (
	rfc8291Plaintext = "When I grow up, I want to be a watermelon"

	rfc8291ASPublic = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIg" +
		"Dll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfc8291ASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfc8291UAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcx" +
		"aOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfc8291UAPrivate  = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfc8291Salt       = "DGv6ra1nlYgDCS1FRnbzlw"
	rfc8291AuthSecret = "BTBZMqHH6r4Tts7J_aSIgg"

	rfc8291ECDHSecret = "kyrL1jIIOHEzg3sM2ZWRHDRB62YACZhhSlknJ672kSs"
	rfc8291PRKKey     = "Snr3JMxaHVDXHWJn5wdC52WjpCtd2EIEGBykDcZW32k"
	rfc8291KeyInfo    = "V2ViUHVzaDogaW5mbwAEJXGyvs3942BVGq8e0PTNNmwR" +
		"zr5VX4m8t7GGpTM5FzFo7OLr4BhZe9MEebhuPI-OztV3" +
		"ylkYfpJGmQ22ggCLDgT-M_SrDepxkU21WCP3O1SUj0Ew" +
		"bZIHMtu5pZpTKGSCIA5Zent7wmC6HCJ5mFgJkuk5cwAvMBKiiujwa7t45ewP"
	rfc8291IKM       = "S4lYMb_L0FxCeq0WhDx813KgSYqU26kOyzWUdsXYyrg"
	rfc8291PRK       = "09_eUZGrsvxChDCGRCdkLiDXrReGOEVeSCdCcPBSJSc"
	rfc8291CEKInfo   = "Q29udGVudC1FbmNvZGluZzogYWVzMTI4Z2NtAA"
	rfc8291CEK       = "oIhVW04MRdy2XN9CiKLxTg"
	rfc8291NonceInfo = "Q29udGVudC1FbmNvZGluZzogbm9uY2UA"
	rfc8291Nonce     = "4h_95klXJ5E_qnoN"

	rfc8291Header = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z" +
		"9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6Tlz" +
		"AC8wEqKK6PBru3jl7A8"
	rfc8291Ciphertext = "8pfeW0KbunFT06SuDKoJH9Ql87S1QUrd" +
		"irN6GcG7sFz1y1sqLgVi1VhjVkHsUoEs" +
		"bI_0LpXMuGvnzQ"
	rfc8291Body = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func b64d(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("fixture is not base64url: %q: %v", s, err)
	}
	return b
}

// TestRFC8291AppendixAVector drives the encryption pipeline with the RFC's
// fixed application-server key and salt and requires every intermediate value
// AND the final 145-octet message to match the RFC byte-for-byte. This test is
// the ship gate for the whole WebPush lane.
func TestRFC8291AppendixAVector(t *testing.T) {
	uaPublic := b64d(t, rfc8291UAPublic)
	authSecret := b64d(t, rfc8291AuthSecret)
	salt := b64d(t, rfc8291Salt)
	wantBody := b64d(t, rfc8291Body)

	// Fixture self-consistency: §5's body must be Appendix A's header ‖ ciphertext.
	if !bytes.Equal(wantBody, append(b64d(t, rfc8291Header), b64d(t, rfc8291Ciphertext)...)) {
		t.Fatal("fixture inconsistent: §5 body != Appendix A header || ciphertext")
	}
	// 86-octet header + 58-octet ciphertext (41 plaintext + 1 delimiter + 16
	// tag). The RFC's example says "Content-Length: 145", a known erratum;
	// the body it prints decodes to 144 octets, and 144 is arithmetically
	// consistent with Appendix A.
	if len(wantBody) != 144 {
		t.Fatalf("fixture body = %d octets, want 144 (86 header + 58 ciphertext)", len(wantBody))
	}

	asPriv, err := ecdh.P256().NewPrivateKey(b64d(t, rfc8291ASPrivate))
	if err != nil {
		t.Fatalf("as_private: %v", err)
	}
	if got := asPriv.PublicKey().Bytes(); !bytes.Equal(got, b64d(t, rfc8291ASPublic)) {
		t.Fatalf("as_public derived from as_private = %x, want fixture", got)
	}

	sec, err := webpushDerive(uaPublic, authSecret, asPriv, salt)
	if err != nil {
		t.Fatalf("webpushDerive: %v", err)
	}
	for _, iv := range []struct {
		name string
		got  []byte
		want string
	}{
		{"ecdh_secret", sec.ecdhSecret, rfc8291ECDHSecret},
		{"PRK_key", sec.prkKey, rfc8291PRKKey},
		{"key_info", sec.keyInfo, rfc8291KeyInfo},
		{"IKM", sec.ikm, rfc8291IKM},
		{"PRK", sec.prk, rfc8291PRK},
		{"CEK", sec.cek, rfc8291CEK},
		{"NONCE", sec.nonce, rfc8291Nonce},
	} {
		if want := b64d(t, iv.want); !bytes.Equal(iv.got, want) {
			t.Errorf("%s = %s, want %s", iv.name,
				base64.RawURLEncoding.EncodeToString(iv.got), iv.want)
		}
	}

	got, err := webpushEncryptWith(uaPublic, authSecret, asPriv, salt, []byte(rfc8291Plaintext))
	if err != nil {
		t.Fatalf("webpushEncryptWith: %v", err)
	}
	if !bytes.Equal(got, wantBody) {
		t.Errorf("push message = %s\nwant %s",
			base64.RawURLEncoding.EncodeToString(got), rfc8291Body)
	}
}

// testWebPushDecrypt is the receiver side of RFC 8291/8188, implemented
// independently in test code (from the UA private key, as a real subscriber
// would): parse the aes128gcm header, run the HKDF chain from the UA side,
// open AES-128-GCM, strip the 0x02 delimiter. Shared by the round-trip
// property test and the delivery-path tests.
func testWebPushDecrypt(t *testing.T, uaPriv *ecdh.PrivateKey, authSecret, body []byte) []byte {
	t.Helper()
	if len(body) < 21+65+16+1 {
		t.Fatalf("body too short to be a keyid-bearing aes128gcm record: %d", len(body))
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	if rs != 4096 {
		t.Fatalf("record size = %d, want 4096", rs)
	}
	idlen := int(body[20])
	if idlen != 65 {
		t.Fatalf("keyid length = %d, want 65 (uncompressed P-256 point)", idlen)
	}
	asPublicBytes := body[21 : 21+idlen]
	asPub, err := ecdh.P256().NewPublicKey(asPublicBytes)
	if err != nil {
		t.Fatalf("keyid is not a valid P-256 point: %v", err)
	}
	ecdhSecret, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatalf("UA-side ECDH: %v", err)
	}
	uaPublic := uaPriv.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, ecdhSecret, authSecret)
	if err != nil {
		t.Fatalf("Extract PRK_key: %v", err)
	}
	keyInfo := "WebPush: info\x00" + string(uaPublic) + string(asPublicBytes)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	if err != nil {
		t.Fatalf("Expand IKM: %v", err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		t.Fatalf("Extract PRK: %v", err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatalf("Expand CEK: %v", err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatalf("Expand NONCE: %v", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	padded, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatalf("GCM open: %v", err)
	}
	// Single record: plaintext ‖ 0x02 (no padding is sent).
	if len(padded) == 0 || padded[len(padded)-1] != 0x02 {
		t.Fatalf("padding delimiter missing: last octet %x", padded[len(padded)-1:])
	}
	return padded[:len(padded)-1]
}

// TestWebPushEncryptRoundTrip encrypts under a fresh random subscription
// (ephemeral sender key + random salt, the production path) and requires the
// independent UA-side decrypter to recover the exact payload.
func TestWebPushEncryptRoundTrip(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate UA key: %v", err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatalf("auth secret: %v", err)
	}
	payload := []byte(`{"v":1,"ref":"apr-123","kind":"decide"}`)

	body, err := webpushEncrypt(uaPriv.PublicKey().Bytes(), authSecret, payload)
	if err != nil {
		t.Fatalf("webpushEncrypt: %v", err)
	}
	if want := 21 + 65 + len(payload) + 1 + 16; len(body) != want {
		t.Errorf("body length = %d, want %d (header+keyid+plaintext+delimiter+tag)", len(body), want)
	}
	if got := testWebPushDecrypt(t, uaPriv, authSecret, body); !bytes.Equal(got, payload) {
		t.Errorf("round trip = %q, want %q", got, payload)
	}

	// Two encryptions of the same payload must differ (fresh salt + ephemeral
	// key per message, RFC 8291 §3.1).
	body2, err := webpushEncrypt(uaPriv.PublicKey().Bytes(), authSecret, payload)
	if err != nil {
		t.Fatalf("second webpushEncrypt: %v", err)
	}
	if bytes.Equal(body, body2) {
		t.Error("two messages encrypted identically: salt/ephemeral key is not fresh per message")
	}
}

// TestWebPushEncryptGuards: malformed subscription material and oversized
// plaintext are refused (fail closed), never a partial or garbage message.
func TestWebPushEncryptGuards(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate UA key: %v", err)
	}
	goodPub := uaPriv.PublicKey().Bytes()
	goodAuth := make([]byte, 16)

	notOnCurve := append([]byte{0x04}, bytes.Repeat([]byte{0xFF}, 64)...)
	compressed := append([]byte{0x02}, goodPub[1:33]...)

	cases := []struct {
		name  string
		pub   []byte
		auth  []byte
		plain []byte
	}{
		{"short public key", goodPub[:64], goodAuth, []byte("x")},
		{"long public key", append(goodPub, 0), goodAuth, []byte("x")},
		{"compressed point refused", compressed, goodAuth, []byte("x")},
		{"point not on curve", notOnCurve, goodAuth, []byte("x")},
		{"auth secret 15 octets", goodPub, goodAuth[:15], []byte("x")},
		{"auth secret 17 octets", goodPub, append(goodAuth, 0), []byte("x")},
		{"empty plaintext", goodPub, goodAuth, nil},
		{"plaintext over the single-record budget", goodPub, goodAuth, bytes.Repeat([]byte("a"), webpushMaxPlaintext+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := webpushEncrypt(tc.pub, tc.auth, tc.plain); err == nil {
				t.Error("want error, got nil")
			}
		})
	}

	// The boundary itself is fine: exactly webpushMaxPlaintext octets fits the
	// 4096-octet message budget of RFC 8291 §4.
	body, err := webpushEncrypt(goodPub, goodAuth, bytes.Repeat([]byte("a"), webpushMaxPlaintext))
	if err != nil {
		t.Fatalf("max-size plaintext refused: %v", err)
	}
	if len(body) != 4096 {
		t.Errorf("max-size message = %d octets, want exactly 4096", len(body))
	}
}

// TestWebPushHeaderShape pins the RFC 8188 header of a production message:
// 16-octet salt, rs=4096, idlen=65, keyid = the ephemeral uncompressed point.
func TestWebPushHeaderShape(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate UA key: %v", err)
	}
	body, err := webpushEncrypt(uaPriv.PublicKey().Bytes(), make([]byte, 16), []byte("hi"))
	if err != nil {
		t.Fatalf("webpushEncrypt: %v", err)
	}
	if rs := binary.BigEndian.Uint32(body[16:20]); rs != 4096 {
		t.Errorf("rs = %d, want 4096", rs)
	}
	if body[20] != 65 {
		t.Errorf("idlen = %d, want 65", body[20])
	}
	if body[21] != 0x04 {
		t.Errorf("keyid first octet = %#x, want 0x04 (uncompressed point)", body[21])
	}
	if _, err := ecdh.P256().NewPublicKey(body[21:86]); err != nil {
		t.Errorf("keyid is not a valid P-256 point: %v", err)
	}
	if strings.Contains(string(body[:16]), rfc8291Salt) {
		t.Error("production salt equals the RFC fixture salt: salt is not random")
	}
}
