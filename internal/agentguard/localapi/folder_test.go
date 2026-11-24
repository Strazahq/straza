package localapi

import (
	"strings"
	"testing"
)

// TestCleanFolder pins the bound both sides of the local API apply to a
// session's folder: a value with a NUL character or bytes that are not
// UTF-8 is dropped, and a longer one is cut from the front on a rune
// boundary, so its end stays and it stays valid UTF-8.
func TestCleanFolder(t *testing.T) {
	a := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"a short folder", "~/src/straza", "~/src/straza"},
		{"exactly the bound", "/" + a(FolderMax-1), "/" + a(FolderMax-1)},
		{"one byte over keeps the end", "/" + a(FolderMax), a(FolderMax)},
		{"a two-byte rune on the cut", "é" + a(FolderMax-1), a(FolderMax - 1)},
		{"a three-byte rune on the cut", "€" + a(FolderMax-1), a(FolderMax - 1)},
		{"a rune that ends at the cut is kept", "x€" + a(FolderMax-3), "€" + a(FolderMax-3)},
		{"a hostile 2000 bytes", "/" + a(1999), a(FolderMax)},
		{"a NUL character", "/home/kim\x00/x", ""},
		{"bytes that are not UTF-8", "/home/\xff\xfe/x", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanFolder(tc.in)
			if got != tc.want {
				t.Fatalf("CleanFolder = %q (%d bytes), want %q (%d bytes)", got, len(got), tc.want, len(tc.want))
			}
		})
	}
}
