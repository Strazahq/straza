// Package audit defines the tamper-evident audit hash chain.
// The hash function is shared by the server consumer (which writes the chain)
// and strazactl (which re-verifies it), so both compute identical digests.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
)

// Genesis is the prev_hash of the first record.
const Genesis = ""

// Link returns the hash of a record given the previous record's hash and the
// record's canonical CloudEvent JSON: sha256(prevHash || "\n" || ce). The
// separator prevents prev/ce boundary ambiguity.
func Link(prevHash, ce string) string {
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write([]byte{'\n'})
	h.Write([]byte(ce))
	return hex.EncodeToString(h.Sum(nil))
}

// Record is one chain entry as verified by clients. Username is a read-time
// enrichment resolved from data.user by the admin API. It lives OUTSIDE the
// CE on purpose: the hash chain covers the CE bytes, which must never be
// mutated after the fact.
type Record struct {
	Seq      int64  `json:"seq"`
	CE       string `json:"ce"`
	PrevHash string `json:"prevHash"`
	Hash     string `json:"hash"`
	Username string `json:"username,omitempty"`
}

// Verify walks records in seq order and reports the first break: a record
// whose prev_hash does not equal the previous record's hash, or whose hash
// does not equal Link(prevHash, ce). ok is true only if the whole chain is
// intact. The records slice must be contiguous from the caller's start point;
// startPrev is the hash expected before the first record (Genesis for a full
// verify, or the last-known-good hash for an incremental one).
func Verify(records []Record, startPrev string) (ok bool, brokenSeq int64) {
	prev := startPrev
	for _, r := range records {
		if r.PrevHash != prev {
			return false, r.Seq
		}
		if r.Hash != Link(r.PrevHash, r.CE) {
			return false, r.Seq
		}
		prev = r.Hash
	}
	return true, 0
}
