package approval

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// RFC 8291 (Message Encryption for Web Push) + RFC 8188 (aes128gcm content
// coding), stdlib only. One message = one record: the whole payload is sealed
// under a content-encryption key and nonce derived from
//
//	ECDH(P-256 ephemeral, subscription key) → HKDF-SHA256 chain
//
// with the subscription's 16-octet auth secret as the first extract salt and a
// fresh 16-octet random salt (carried in the RFC 8188 header) as the second.
// The ephemeral public key travels as the header's keyid (65-octet
// uncompressed point, RFC 8291 §4) and is discarded after the message: a new
// key pair and salt per message (§3.1), so no two ciphertexts ever share key
// material. The gold standard for all of this is the RFC 8291 Appendix A
// worked example, pinned byte-for-byte by TestRFC8291AppendixAVector.

const (
	// webpushRecordSize is the rs field of the RFC 8188 header. We always send
	// a single record, but 4096 is what every push service expects (and what
	// the RFC 8291 example uses).
	webpushRecordSize = 4096
	// webpushMaxPlaintext is the largest payload a single 4096-octet push
	// message can carry: 4096 - 86 (header incl. 65-octet keyid) - 1 (0x02
	// delimiter) - 16 (GCM tag) = 3993 (RFC 8291 §4). Our opaque envelope is
	// ~60 octets; the guard exists so a future payload change fails loudly
	// here instead of being rejected by every push service.
	webpushMaxPlaintext = 3993
	// webpushAuthSecretLen is the subscription auth secret length (§3.2).
	webpushAuthSecretLen = 16
	// webpushPointLen is an uncompressed P-256 point: 0x04 ‖ X ‖ Y.
	webpushPointLen = 65
)

// webpushSecrets holds every intermediate of the RFC 8291 §3.3/§3.4 key
// schedule. Only cek and nonce are consumed by encryption; the rest exist so
// the Appendix A vector test can pin each step individually (a failure names
// the first divergent stage instead of just "ciphertext differs").
type webpushSecrets struct {
	ecdhSecret []byte // ECDH(as_private, ua_public), 32 octets
	prkKey     []byte // HKDF-Extract(salt=auth_secret, IKM=ecdh_secret)
	keyInfo    []byte // "WebPush: info" ‖ 0x00 ‖ ua_public ‖ as_public
	ikm        []byte // HKDF-Expand(prkKey, keyInfo, 32)
	prk        []byte // HKDF-Extract(salt=salt, IKM=ikm)
	cek        []byte // HKDF-Expand(prk, "Content-Encoding: aes128gcm"‖0x00, 16)
	nonce      []byte // HKDF-Expand(prk, "Content-Encoding: nonce"‖0x00, 12)
}

// webpushDerive runs the RFC 8291 §3.3–3.4 key schedule. uaPublic must be a
// valid 65-octet uncompressed P-256 point and authSecret exactly 16 octets;
// both are validated here (fail closed: bad subscription material never
// produces a key).
func webpushDerive(uaPublic, authSecret []byte, asPriv *ecdh.PrivateKey, salt []byte) (webpushSecrets, error) {
	var s webpushSecrets
	uaPub, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return s, fmt.Errorf("webpush: subscription p256dh is not a valid uncompressed P-256 point: %w", err)
	}
	if len(authSecret) != webpushAuthSecretLen {
		return s, fmt.Errorf("webpush: subscription auth secret is %d octets, RFC 8291 requires exactly %d", len(authSecret), webpushAuthSecretLen)
	}
	if len(salt) != 16 {
		return s, fmt.Errorf("webpush: salt is %d octets, want 16", len(salt))
	}
	s.ecdhSecret, err = asPriv.ECDH(uaPub)
	if err != nil {
		return s, fmt.Errorf("webpush: ECDH: %w", err)
	}
	if s.prkKey, err = hkdf.Extract(sha256.New, s.ecdhSecret, authSecret); err != nil {
		return s, fmt.Errorf("webpush: extract PRK_key: %w", err)
	}
	asPublic := asPriv.PublicKey().Bytes()
	s.keyInfo = make([]byte, 0, 14+len(uaPublic)+len(asPublic))
	s.keyInfo = append(s.keyInfo, "WebPush: info\x00"...)
	s.keyInfo = append(s.keyInfo, uaPublic...)
	s.keyInfo = append(s.keyInfo, asPublic...)
	if s.ikm, err = hkdf.Expand(sha256.New, s.prkKey, string(s.keyInfo), 32); err != nil {
		return s, fmt.Errorf("webpush: expand IKM: %w", err)
	}
	if s.prk, err = hkdf.Extract(sha256.New, s.ikm, salt); err != nil {
		return s, fmt.Errorf("webpush: extract PRK: %w", err)
	}
	if s.cek, err = hkdf.Expand(sha256.New, s.prk, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return s, fmt.Errorf("webpush: expand CEK: %w", err)
	}
	if s.nonce, err = hkdf.Expand(sha256.New, s.prk, "Content-Encoding: nonce\x00", 12); err != nil {
		return s, fmt.Errorf("webpush: expand NONCE: %w", err)
	}
	return s, nil
}

// webpushEncryptWith seals plaintext into a complete aes128gcm message body
// with an EXPLICIT ephemeral key and salt, the deterministic core the RFC
// 8291 Appendix A vector drives. Production callers use webpushEncrypt, which
// generates both.
//
// Body layout (RFC 8188 §2 + RFC 8291 §4):
//
//	salt(16) ‖ rs(4, big-endian 4096) ‖ idlen(1, 65) ‖ keyid(as_public, 65)
//	‖ AES-128-GCM(cek, nonce, plaintext ‖ 0x02)
//
// Single record, 0x02 padding delimiter (last record), no padding octets.
func webpushEncryptWith(uaPublic, authSecret []byte, asPriv *ecdh.PrivateKey, salt, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("webpush: refusing to encrypt an empty payload")
	}
	if len(plaintext) > webpushMaxPlaintext {
		return nil, fmt.Errorf("webpush: payload is %d octets, the single-record budget is %d (RFC 8291 §4)", len(plaintext), webpushMaxPlaintext)
	}
	sec, err := webpushDerive(uaPublic, authSecret, asPriv, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(sec.cek)
	if err != nil {
		return nil, fmt.Errorf("webpush: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("webpush: gcm: %w", err)
	}

	asPublic := asPriv.PublicKey().Bytes()
	if len(asPublic) != webpushPointLen {
		return nil, fmt.Errorf("webpush: ephemeral public key is %d octets, want %d", len(asPublic), webpushPointLen)
	}
	body := make([]byte, 0, 21+webpushPointLen+len(plaintext)+1+gcm.Overhead())
	body = append(body, salt...)
	body = binary.BigEndian.AppendUint32(body, webpushRecordSize)
	body = append(body, webpushPointLen) // idlen
	body = append(body, asPublic...)

	padded := make([]byte, 0, len(plaintext)+1)
	padded = append(padded, plaintext...)
	padded = append(padded, 0x02)
	return gcm.Seal(body, sec.nonce, padded, nil), nil
}

// webpushEncrypt is the production entry: fresh random salt + ephemeral P-256
// key per message (RFC 8291 §3.1), then the deterministic core.
func webpushEncrypt(uaPublic, authSecret, plaintext []byte) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("webpush: salt: %w", err)
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("webpush: ephemeral key: %w", err)
	}
	return webpushEncryptWith(uaPublic, authSecret, asPriv, salt, plaintext)
}
