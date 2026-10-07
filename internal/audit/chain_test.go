package audit

import "testing"

func TestLink(t *testing.T) {
	tests := []struct {
		name     string
		prev, ce string
		want     string
	}{
		// Known-answer vectors pin the wire hash, sha256(prev || "\n" || ce)
		// hex, because strazactl and the web console recompute it
		// independently; any drift here is a wire break.
		{name: "genesis record", prev: Genesis, ce: `{"a":1}`,
			want: "7395dc66860499cb2b8970e17094ec53ed1ff54e7067ad619f884917096166b0"},
		{name: "boundary ab|c", prev: "ab", ce: "c",
			want: "0acdf9a2665198da784232d827b01ae3d24af5db060d723950cfcd47cd82ac07"},
		{name: "boundary a|bc", prev: "a", ce: "bc",
			want: "b4c620724d95022be9765d6f660a36f90835acab2437b5e8cd71812940b29346"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Link(tc.prev, tc.ce); got != tc.want {
				t.Fatalf("Link(%q, %q) = %s, want %s", tc.prev, tc.ce, got, tc.want)
			}
		})
	}
	// The "\n" separator removes prev/ce boundary ambiguity: without it both
	// calls below would hash the same "abc" bytes.
	if Link("ab", "c") == Link("a", "bc") {
		t.Fatal(`Link("ab","c") == Link("a","bc"): separator fails to disambiguate the boundary`)
	}
}

// mkChain builds an intact chain over ces starting at startPrev, seqs from 1.
func mkChain(ces []string, startPrev string) []Record {
	recs := make([]Record, len(ces))
	prev := startPrev
	for i, ce := range ces {
		h := Link(prev, ce)
		recs[i] = Record{Seq: int64(i + 1), CE: ce, PrevHash: prev, Hash: h}
		prev = h
	}
	return recs
}

func TestVerify(t *testing.T) {
	intact := mkChain([]string{`{"n":1}`, `{"n":2}`, `{"n":3}`}, Genesis)
	clone := func(mut func([]Record)) []Record {
		c := append([]Record(nil), intact...)
		if mut != nil {
			mut(c)
		}
		return c
	}

	tests := []struct {
		name      string
		records   []Record
		startPrev string
		wantOK    bool
		wantSeq   int64
	}{
		{name: "intact chain from genesis",
			records: clone(nil), startPrev: Genesis, wantOK: true},
		{name: "empty records",
			records: nil, startPrev: Genesis, wantOK: true},
		{name: "tampered ce detected at its seq",
			records:   clone(func(r []Record) { r[1].CE += " " }),
			startPrev: Genesis, wantOK: false, wantSeq: 2},
		{name: "broken prev linkage detected at its seq",
			records:   clone(func(r []Record) { r[2].PrevHash = "deadbeef" }),
			startPrev: Genesis, wantOK: false, wantSeq: 3},
		{name: "wrong stored hash detected",
			records:   clone(func(r []Record) { r[0].Hash = Link(Genesis, "forged") }),
			startPrev: Genesis, wantOK: false, wantSeq: 1},
		{name: "incremental verify from checkpoint",
			records: clone(nil)[1:], startPrev: intact[0].Hash, wantOK: true},
		{name: "incremental verify broken tail",
			records:   clone(func(r []Record) { r[2].CE += "!" })[1:],
			startPrev: intact[0].Hash, wantOK: false, wantSeq: 3},
		{name: "incremental verify wrong checkpoint",
			records: clone(nil)[1:], startPrev: Genesis, wantOK: false, wantSeq: 2},
		// Username is a read-time enrichment living OUTSIDE the CE: mutating
		// it after the fact must never break the chain.
		{name: "username not covered by hash",
			records: clone(func(r []Record) {
				for i := range r {
					r[i].Username = "mallory"
				}
			}),
			startPrev: Genesis, wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, seq := Verify(tc.records, tc.startPrev)
			if ok != tc.wantOK || seq != tc.wantSeq {
				t.Fatalf("Verify = (%v, %d), want (%v, %d)", ok, seq, tc.wantOK, tc.wantSeq)
			}
		})
	}
}
