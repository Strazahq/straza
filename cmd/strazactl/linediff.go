package main

import (
	"fmt"
	"io"
)

// diffOp is one line of a diff walk: ' ' shared, '-' only in the stored
// side, '+' only in the local side.
type diffOp struct {
	t byte
	s string
}

// lcsMaxCells bounds the quadratic LCS table (cells, not bytes). Real policy
// edits shrink to a small middle after the prefix/suffix trim in diffOps; a
// middle bigger than this is rendered as a whole-block replacement instead
// of growing an unbounded table on the shared box.
const lcsMaxCells = 4 << 20

// diffOps computes line ops between two documents. The shared prefix and
// suffix are trimmed first so the LCS only ever walks the changed middle;
// the console's lineDiff does the same walk without the trim, on the same
// inputs, so the two renderings agree.
func diffOps(a, b []string) []diffOp {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	midA, midB := a[p:len(a)-s], b[p:len(b)-s]

	var mid []diffOp
	if (len(midA)+1)*(len(midB)+1) > lcsMaxCells {
		for _, l := range midA {
			mid = append(mid, diffOp{'-', l})
		}
		for _, l := range midB {
			mid = append(mid, diffOp{'+', l})
		}
	} else {
		mid = lcsOps(midA, midB)
	}

	out := make([]diffOp, 0, p+len(mid)+s)
	for _, l := range a[:p] {
		out = append(out, diffOp{' ', l})
	}
	out = append(out, mid...)
	for _, l := range a[len(a)-s:] {
		out = append(out, diffOp{' ', l})
	}
	return out
}

// lcsOps is the plain LCS walk (the console lineDiff, translated).
func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	out := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffOp{' ', a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, diffOp{'-', a[i]})
			i++
		default:
			out = append(out, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffOp{'+', b[j]})
	}
	return out
}

// unifiedCtx is the unified-diff context width, diff(1)'s default.
const unifiedCtx = 3

// writeUnified renders ops as unified-diff hunks with unifiedCtx lines of
// context. The ---/+++ headers are the caller's (they carry names, not paths).
func writeUnified(w io.Writer, ops []diffOp) {
	// Change ranges in op-index space; ranges whose context would touch or
	// overlap fold into one hunk.
	type rng struct{ s, e int }
	var ranges []rng
	for i, op := range ops {
		if op.t == ' ' {
			continue
		}
		if len(ranges) > 0 && i-ranges[len(ranges)-1].e <= 2*unifiedCtx {
			ranges[len(ranges)-1].e = i + 1
		} else {
			ranges = append(ranges, rng{i, i + 1})
		}
	}

	// Line number (1-based) each op holds on its side.
	aAt := make([]int, len(ops))
	bAt := make([]int, len(ops))
	aLine, bLine := 1, 1
	for i, op := range ops {
		aAt[i], bAt[i] = aLine, bLine
		if op.t != '+' {
			aLine++
		}
		if op.t != '-' {
			bLine++
		}
	}

	for _, r := range ranges {
		s := r.s - unifiedCtx
		if s < 0 {
			s = 0
		}
		e := r.e + unifiedCtx
		if e > len(ops) {
			e = len(ops)
		}
		var aCount, bCount int
		for _, op := range ops[s:e] {
			if op.t != '+' {
				aCount++
			}
			if op.t != '-' {
				bCount++
			}
		}
		aStart, bStart := aAt[s], bAt[s]
		// A side with no lines in the hunk states the line BEFORE the hunk,
		// the unified-diff convention for pure insertions/deletions.
		if aCount == 0 {
			aStart--
		}
		if bCount == 0 {
			bStart--
		}
		fmt.Fprintf(w, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
		for _, op := range ops[s:e] {
			fmt.Fprintf(w, "%c%s\n", op.t, op.s)
		}
	}
}
